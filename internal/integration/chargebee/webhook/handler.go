package webhook

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/integration/chargebee"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// ServiceDependencies is the set of services a webhook needs to settle a payment.
type ServiceDependencies = interfaces.ServiceDependencies

// Handler handles Chargebee webhook events
type Handler struct {
	client     chargebee.ChargebeeClient
	invoiceSvc *chargebee.InvoiceService
	logger     *logger.Logger
}

// NewHandler creates a new Chargebee webhook handler
func NewHandler(
	client chargebee.ChargebeeClient,
	invoiceSvc *chargebee.InvoiceService,
	logger *logger.Logger,
) *Handler {
	return &Handler{
		client:     client,
		invoiceSvc: invoiceSvc,
		logger:     logger,
	}
}

// HandleWebhookEvent processes a Chargebee webhook event
func (h *Handler) HandleWebhookEvent(ctx context.Context, event *ChargebeeWebhookEvent, environmentID string, services *ServiceDependencies) error {
	h.logger.Info(ctx, "processing Chargebee webhook event",
		"event_type", event.EventType,
		"event_id", event.ID,
		"environment_id", environmentID,
		"occurred_at", timestampToTime(event.OccurredAt),
	)

	eventType := ChargebeeEventType(event.EventType)

	switch eventType {
	case EventPaymentSucceeded:
		return h.handlePaymentSucceeded(ctx, event, services)
	case EventPaymentFailed:
		return h.handlePaymentFailed(ctx, event, services)
	default:
		h.logger.Info(ctx, "unhandled Chargebee webhook event type", "type", event.EventType)
		return nil // Not an error, just unhandled
	}
}

// handlePaymentSucceeded handles payment_succeeded webhook
func (h *Handler) handlePaymentSucceeded(ctx context.Context, event *ChargebeeWebhookEvent, services *ServiceDependencies) error {
	content, ok := h.parseContent(ctx, event)
	if !ok {
		return nil
	}

	transaction := content.Transaction
	invoice := content.Invoice

	h.logger.Info(ctx, "received payment_succeeded webhook",
		"chargebee_transaction_id", transaction.ID,
		"chargebee_invoice_id", invoice.ID,
	)

	// A hosted checkout page creates its own Chargebee invoice, which has no entity
	// mapping — po_number carries the Flexprice payment id for those.
	flexpricePaymentID := invoice.PONumber
	flexpriceInvoiceID, err := h.invoiceSvc.GetFlexPriceInvoiceIDByChargebeeInvoiceID(ctx, invoice.ID)
	if err != nil {
		if flexpricePaymentID == "" {
			h.logger.Error(ctx, "failed to get FlexPrice invoice ID for Chargebee invoice and neither payment ID",
				"error", err,
				"chargebee_invoice_id", invoice.ID,
				"event_id", event.ID)
			return nil // Don't fail webhook processing
		}
		flexpriceInvoiceID = ""
	}

	h.logger.Info(ctx, "resolved flexprice entities for payment",
		"flexprice_invoice_id", flexpriceInvoiceID,
		"flexprice_payment_id", flexpricePaymentID,
		"chargebee_invoice_id", invoice.ID,
		"chargebee_transaction_id", transaction.ID)

	// A checkout session owns settlement for its own invoice. ReconcileInvoicePayment
	// below writes the invoice directly, bypassing the wallet_transaction_id hook that
	// credits a purchased-credit top-up, and it adds to AmountPaid where the checkout
	// path sets it absolutely — running both overpays and never credits. So route to
	// exactly one of them.
	handled, err := h.handleCheckoutSessionForPayment(ctx, flexpricePaymentID, flexpriceInvoiceID, invoice.ID, transaction.ID, services)
	if err != nil {
		return nil // Already logged; don't fail webhook processing
	}
	if handled {
		return nil
	}

	if flexpriceInvoiceID == "" {
		h.logger.Info(ctx, "no checkout session and no mapped invoice for chargebee payment, ignoring",
			"chargebee_invoice_id", invoice.ID,
			"flexprice_payment_id", flexpricePaymentID,
			"event_id", event.ID)
		return nil
	}

	// Convert amount from cents to standard unit
	paymentAmount := decimal.NewFromInt(transaction.Amount).Div(decimal.NewFromInt(100))

	// Process payment via service method
	err = h.invoiceSvc.ProcessChargebeePaymentFromWebhook(
		ctx,
		flexpriceInvoiceID,
		transaction.ID,
		invoice.ID,
		paymentAmount,
		transaction.CurrencyCode,
		transaction.PaymentMethod,
	)
	if err != nil {
		h.logger.Error(ctx, "failed to process Chargebee payment",
			"error", err,
			"flexprice_invoice_id", flexpriceInvoiceID,
			"chargebee_transaction_id", transaction.ID)
		return nil // Don't fail webhook processing
	}

	h.logger.Info(ctx, "successfully processed payment_succeeded webhook",
		"flexprice_invoice_id", flexpriceInvoiceID,
		"chargebee_invoice_id", invoice.ID,
		"chargebee_transaction_id", transaction.ID,
		"payment_amount", paymentAmount.String())

	return nil
}

// handlePaymentFailed terminates the checkout session a failed payment belongs to,
// releasing its idempotency key and the per-wallet pending guard. A failure outside
// a checkout session is left to the invoice's own dunning.
func (h *Handler) handlePaymentFailed(ctx context.Context, event *ChargebeeWebhookEvent, services *ServiceDependencies) error {
	content, ok := h.parseContent(ctx, event)
	if !ok {
		return nil
	}

	flexpriceInvoiceID, err := h.invoiceSvc.GetFlexPriceInvoiceIDByChargebeeInvoiceID(ctx, content.Invoice.ID)
	if err != nil {
		if content.Invoice.PONumber == "" {
			h.logger.Info(ctx, "no FlexPrice invoice for failed Chargebee payment, ignoring",
				"error", err,
				"chargebee_invoice_id", content.Invoice.ID,
				"event_id", event.ID)
			return nil
		}
		flexpriceInvoiceID = ""
	}

	session, err := h.findCheckoutSessionForPayment(ctx, content.Invoice.PONumber, services)
	if err != nil || session == nil {
		return nil
	}
	if session.CheckoutStatus != types.CheckoutStatusPending {
		return nil
	}

	reason := fmt.Errorf("chargebee payment failed for transaction %s", content.Transaction.ID)
	if err := services.CheckoutSessionService.CleanupCheckoutSession(ctx, session.ID, reason); err != nil {
		h.logger.Error(ctx, "failed to clean up checkout session on payment failure",
			"error", err,
			"session_id", session.ID,
			"flexprice_invoice_id", flexpriceInvoiceID,
			"chargebee_transaction_id", content.Transaction.ID)
	}
	return nil
}

// handleCheckoutSessionForPayment completes the pending checkout session this payment
// belongs to. Returns false when the payment has no checkout session, i.e. it is an
// ordinary invoice payment the caller should reconcile itself.
func (h *Handler) handleCheckoutSessionForPayment(
	ctx context.Context,
	flexpricePaymentID, flexpriceInvoiceID string,
	chargebeeInvoiceID, chargebeeTransactionID string,
	services *ServiceDependencies,
) (bool, error) {
	session, err := h.findCheckoutSessionForPayment(ctx, flexpricePaymentID, services)
	if err != nil {
		h.logger.Error(ctx, "failed to look up checkout session for payment",
			"error", err,
			"flexprice_invoice_id", flexpriceInvoiceID,
			"chargebee_transaction_id", chargebeeTransactionID)
		return false, err
	}
	if session == nil {
		return false, nil
	}

	switch session.CheckoutStatus {
	case types.CheckoutStatusPending:
		err := services.CheckoutSessionService.CompleteCheckoutSession(ctx, session.ID, &types.CheckoutProviderResult{
			ProviderPaymentIntentID: chargebeeTransactionID,
			ProviderMetadata: map[string]string{
				"chargebee_transaction_id": chargebeeTransactionID,
			},
		})
		switch {
		case err == nil:
			h.logger.Info(ctx, "completed checkout session from chargebee webhook",
				"session_id", session.ID,
				"flexprice_invoice_id", flexpriceInvoiceID,
				"chargebee_transaction_id", chargebeeTransactionID)

			if err := h.invoiceSvc.LinkInvoiceMapping(ctx, *session.CheckoutInvoiceID, chargebeeInvoiceID); err != nil {
				h.logger.Error(ctx, "failed to link chargebee invoice to flexprice invoice",
					"error", err,
					"session_id", session.ID,
					"flexprice_invoice_id", *session.CheckoutInvoiceID,
					"chargebee_invoice_id", chargebeeInvoiceID)
			}
		case ierr.IsAlreadyExists(err):
			// At-least-once delivery: another delivery or a reconciler got there first.
			h.logger.Info(ctx, "checkout session already completed",
				"session_id", session.ID,
				"chargebee_transaction_id", chargebeeTransactionID)
		default:
			h.logger.Error(ctx, "failed to complete checkout session",
				"error", err,
				"session_id", session.ID,
				"flexprice_invoice_id", flexpriceInvoiceID,
				"chargebee_transaction_id", chargebeeTransactionID)
		}

	default:
		// Terminal session, money taken: the customer paid after we gave up. Phase 6
		// adds the refund; until then this needs manual reconciliation.
		h.logger.Error(ctx, "payment for a checkout session in a terminal state — manual reconciliation required",
			"error", "session is not pending",
			"session_id", session.ID,
			"session_status", session.CheckoutStatus,
			"flexprice_invoice_id", flexpriceInvoiceID,
			"chargebee_transaction_id", chargebeeTransactionID)
	}

	return true, nil
}

// findCheckoutSessionForPayment returns the session that owns this payment, in any
// status. Nil means the payment did not come from a checkout session.
func (h *Handler) findCheckoutSessionForPayment(
	ctx context.Context,
	flexpricePaymentID string,
	services *ServiceDependencies,
) (*dto.CheckoutSessionResponse, error) {
	if flexpricePaymentID == "" || services == nil || services.CheckoutSessionService == nil {
		return nil, nil
	}

	filter := types.NewDefaultCheckoutSessionFilter()
	filter.CheckoutPaymentIDs = []string{flexpricePaymentID}
	filter.Limit = lo.ToPtr(1)
	filter.Status = lo.ToPtr(types.StatusPublished)

	sessions, err := services.CheckoutSessionService.List(ctx, filter)
	if err != nil {
		return nil, err
	}
	if sessions == nil || len(sessions.Items) == 0 {
		return nil, nil
	}
	return sessions.Items[0], nil
}

// parseContent decodes the event body, requiring both a transaction and an invoice.
func (h *Handler) parseContent(ctx context.Context, event *ChargebeeWebhookEvent) (*ChargebeeWebhookContent, bool) {
	var content ChargebeeWebhookContent
	if err := json.Unmarshal(event.Content, &content); err != nil {
		h.logger.Error(ctx, "failed to parse webhook content",
			"error", err,
			"event_id", event.ID)
		return nil, false
	}

	if content.Transaction == nil {
		h.logger.Info(ctx, "no transaction found in event content",
			"event_id", event.ID, "event_type", event.EventType)
		return nil, false
	}
	if content.Invoice == nil {
		h.logger.Info(ctx, "no invoice found in event content",
			"event_id", event.ID, "event_type", event.EventType)
		return nil, false
	}

	return &content, true
}
