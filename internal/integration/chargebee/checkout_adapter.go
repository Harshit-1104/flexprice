package chargebee

import (
	"context"
	"fmt"
	"time"

	paymentSourceEnum "github.com/chargebee/chargebee-go/v3/models/paymentsource/enum"
	transactionModel "github.com/chargebee/chargebee-go/v3/models/transaction"
	transactionEnum "github.com/chargebee/chargebee-go/v3/models/transaction/enum"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

type CheckoutAdapter struct {
	Client      ChargebeeClient
	CustomerSvc *CustomerService
	InvoiceSvc  *InvoiceService
	Logger      *logger.Logger
}

// mirrorInvoice creates (or reuses) the Chargebee ad-hoc invoice that mirrors a
// Flexprice invoice, and returns the Chargebee customer id and invoice id.
//
// Only the off-session charge path needs this: collect_payment's invoice_allocations
// must point at a Chargebee invoice for the charged amount to match what we quoted.
// Hosted checkout does not mirror — that page creates its own invoice.
//
// flexPaymentID doubles as the correlation key (stamped as po_number, matching the
// hosted page) and as the idempotency-key seed.
func (a *CheckoutAdapter) mirrorInvoice(
	ctx context.Context,
	flexCustomerID, flexInvoiceID, currency string,
	amount decimal.Decimal,
	flexPaymentID string,
) (chargebeeCustomerID string, chargebeeInvoiceID string, err error) {
	cust, err := a.CustomerSvc.EnsureCustomerSyncedToChargebee(ctx, flexCustomerID)
	if err != nil {
		return "", "", err
	}
	chargebeeCustomerID = cust.Metadata["chargebee_customer_id"]
	if chargebeeCustomerID == "" {
		chargebeeCustomerID, err = a.CustomerSvc.GetChargebeeCustomerID(ctx, flexCustomerID)
		if err != nil {
			return "", "", err
		}
	}

	// Reuse an already-mirrored invoice so a retry does not create a second one.
	if existing, mapErr := a.InvoiceSvc.getExistingChargebeeMapping(ctx, flexInvoiceID); mapErr == nil && existing != nil {
		return chargebeeCustomerID, existing.ProviderEntityID, nil
	}

	amountMinor := amountToMinorUnits(amount, currency)
	inv, err := a.Client.CreateAdHocInvoice(
		ctx,
		chargebeeCustomerID,
		currency,
		amountMinor,
		fmt.Sprintf("Flexprice invoice %s", flexInvoiceID),
		flexPaymentID,
		// Chargebee scopes chargebee-idempotency-key GLOBALLY, not per endpoint:
		// reusing one key across two different calls fails with 422
		// "already been used for a different request". Namespace per operation.
		idempotencyScoped(flexPaymentID, "invoice"),
	)
	if err != nil {
		return "", "", err
	}
	chargebeeInvoiceID = inv.Id
	if chargebeeInvoiceID == "" {
		return "", "", missingPayload("invoice id")
	}

	// Record the mapping so the payment_succeeded webhook can find its way back
	// to the Flexprice invoice.
	if err := a.InvoiceSvc.LinkInvoiceMapping(ctx, flexInvoiceID, chargebeeInvoiceID); err != nil {
		a.Logger.Error(ctx, "failed to map chargebee invoice",
			"error", err, "flexprice_invoice_id", flexInvoiceID, "chargebee_invoice_id", chargebeeInvoiceID)
	}

	a.Logger.Info(ctx, "mirrored flexprice invoice to chargebee",
		"flexprice_invoice_id", flexInvoiceID,
		"chargebee_invoice_id", chargebeeInvoiceID,
		"amount_minor", amountMinor,
		"currency", currency)

	return chargebeeCustomerID, chargebeeInvoiceID, nil
}

// CreatePaymentLink returns a Chargebee-hosted checkout page for an exact ad-hoc
// amount.
func (a *CheckoutAdapter) CreatePaymentLink(
	ctx context.Context,
	req interfaces.CheckoutProviderRequest,
) (*interfaces.CheckoutProviderResponse, error) {
	return a.hostedCheckout(ctx, req.CustomerID, req.PaymentID, req.InvoiceID, req.Currency, req.Amount, req.SuccessURL)
}

// CreateAuthorizationLink is the same hosted page.
func (a *CheckoutAdapter) CreateAuthorizationLink(
	ctx context.Context,
	req interfaces.AuthorizationLinkRequest,
) (*interfaces.CheckoutProviderResponse, error) {
	return a.hostedCheckout(ctx, req.CustomerID, req.PaymentID, req.InvoiceID, req.Currency, req.Amount, req.SuccessURL)
}

func (a *CheckoutAdapter) hostedCheckout(
	ctx context.Context,
	flexCustomerID, flexPaymentID, flexInvoiceID, currency string,
	amount decimal.Decimal,
	successURL string,
) (*interfaces.CheckoutProviderResponse, error) {
	cust, err := a.CustomerSvc.EnsureCustomerSyncedToChargebee(ctx, flexCustomerID)
	if err != nil {
		return nil, err
	}
	cbCustomerID := cust.Metadata["chargebee_customer_id"]
	if cbCustomerID == "" {
		if cbCustomerID, err = a.CustomerSvc.GetChargebeeCustomerID(ctx, flexCustomerID); err != nil {
			return nil, err
		}
	}

	cfg, err := a.Client.GetChargebeeConfig(ctx)
	if err != nil {
		return nil, err
	}

	page, err := a.Client.CreateCheckoutOneTimePage(
		ctx,
		cbCustomerID,
		currency,
		amountToMinorUnits(amount, currency),
		fmt.Sprintf("Flexprice invoice %s", flexInvoiceID),
		successURL,
		cfg.GatewayAccountID,
		flexPaymentID,
	)
	if err != nil {
		return nil, err
	}
	if page.Url == "" {
		return nil, missingPayload("hosted page url")
	}

	a.Logger.Info(ctx, "created chargebee hosted checkout page",
		"hosted_page_id", page.Id,
		"flexprice_invoice_id", flexInvoiceID,
		"flexprice_payment_id", flexPaymentID,
		"expires_at", page.ExpiresAt)

	resp := &interfaces.CheckoutProviderResponse{
		ProviderSessionID: page.Id,
		NextAction: types.PaymentAction{
			Type: types.PaymentActionTypeCheckoutURL,
			URL:  page.Url,
		},
		ProviderMetadata: map[string]string{
			"chargebee_customer_id": cbCustomerID,
			"chargebee_hosted_page": page.Id,
		},
	}
	if page.ExpiresAt > 0 {
		resp.ExpiresAt = lo.ToPtr(time.Unix(page.ExpiresAt, 0).UTC())
	}
	return resp, nil
}

// TryAutoChargingSavedMethod charges the customer's stored card off-session.
//
// charged=false (with a nil error) means "no usable saved method" so the caller
// can fall back. Chargebee signals that as HTTP 400 payment_method_not_present,
// which we translate rather than propagate.
func (a *CheckoutAdapter) TryAutoChargingSavedMethod(
	ctx context.Context,
	req interfaces.AuthorizationLinkRequest,
) (*interfaces.CheckoutProviderResponse, bool, error) {
	cbCustomerID, cbInvoiceID, err := a.mirrorInvoice(
		ctx, req.CustomerID, req.InvoiceID, req.Currency, req.Amount, req.PaymentID)
	if err != nil {
		return nil, false, err
	}

	// Resolve the source explicitly. customers/{id}/collect_payment does NOT fall
	// back to the customer's primary source — it rejects with
	// "Either of card or tmpToken input should be specified". (The invoice-scoped
	// collect_payment DOES fall back, but it cannot pin invoice_allocations, which
	// is what makes the charged amount match the amount we quoted.)
	sourceID, err := a.resolvePaymentSourceID(ctx, cbCustomerID)
	if err != nil {
		return nil, false, err
	}
	if sourceID == "" {
		a.Logger.Info(ctx, "chargebee auto-charge: no saved payment source",
			"customer_id", req.CustomerID, "invoice_id", req.InvoiceID)
		return nil, false, nil
	}

	txn, err := a.Client.CollectPayment(ctx, collectParams(req, cbCustomerID, cbInvoiceID, sourceID))
	if err != nil {
		if IsNoPaymentMethod(err) {
			a.Logger.Info(ctx, "chargebee auto-charge: no valid card on file",
				"customer_id", req.CustomerID, "invoice_id", req.InvoiceID)
			return nil, false, nil
		}
		return nil, false, err
	}

	resp := a.responseFromCollect(ctx, txn, cbInvoiceID)

	// collect_payment is synchronous, so the returned status is the outcome — but
	// "settled" is only one of three. A declined charge that came back 200 must fail
	// the checkout rather than leave it pending until expiry.
	if classifyTransaction(txn.Status) == transactionFailed {
		return nil, false, ierr.NewError("chargebee off-session charge did not succeed").
			WithHintf("The saved payment method was declined (transaction status %s)", txn.Status).
			WithReportableDetails(map[string]any{
				"transaction_status":   txn.Status,
				"chargebee_invoice_id": cbInvoiceID,
				"invoice_id":           req.InvoiceID,
			}).
			Mark(ierr.ErrHTTPClient)
	}

	return resp, true, nil
}

// collectParams builds the off-session charge. CustomerPresent is what declares the
// transaction customer-initiated at the card network; it must reflect whether the
// customer is really there, not what is convenient.
func collectParams(req interfaces.AuthorizationLinkRequest, cbCustomerID, cbInvoiceID, sourceID string) CollectPaymentParams {
	return CollectPaymentParams{
		ChargebeeCustomerID: cbCustomerID,
		ChargebeeInvoiceID:  cbInvoiceID,
		AmountMinor:         amountToMinorUnits(req.Amount, req.Currency),
		PaymentSourceID:     sourceID,
		CustomerPresent:     req.CustomerPresent,
		IdempotencyKey:      idempotencyScoped(req.PaymentID, "collect"),
	}
}

// resolvePaymentSourceID returns the customer's primary vaulted source, falling
// back to the first valid one. Empty string means the customer has no usable card.
func (a *CheckoutAdapter) resolvePaymentSourceID(ctx context.Context, cbCustomerID string) (string, error) {
	sources, err := a.Client.ListPaymentSources(ctx, cbCustomerID)
	if err != nil {
		return "", err
	}
	if len(sources) == 0 {
		return "", nil
	}

	primary := ""
	if cust, err := a.Client.RetrieveCustomer(ctx, cbCustomerID); err == nil {
		primary = cust.PrimaryPaymentSourceId
	}

	first := ""
	for _, src := range sources {
		if src == nil || src.Status != paymentSourceEnum.StatusValid {
			continue
		}
		if src.Id == primary {
			return src.Id, nil
		}
		if first == "" {
			first = src.Id
		}
	}
	return first, nil
}

// responseFromCollect normalizes a collect_payment transaction.
//
// IdAtGateway is stored verbatim: it is NOT always a Stripe ch_* id — cards
// vaulted through different flows land on different gateway accounts, producing
// cb_* ids from Chargebee's own test gateway.
func (a *CheckoutAdapter) responseFromCollect(
	ctx context.Context,
	txn *transactionModel.Transaction,
	cbInvoiceID string,
) *interfaces.CheckoutProviderResponse {
	a.Logger.Info(ctx, "chargebee collect_payment settled",
		"chargebee_invoice_id", cbInvoiceID,
		"transaction_id", txn.Id,
		"status", txn.Status,
		"id_at_gateway", txn.IdAtGateway,
		"initiator_type", txn.InitiatorType)

	meta := map[string]string{
		"chargebee_invoice_id": cbInvoiceID,
		"transaction_status":   string(txn.Status),
	}
	if txn.IdAtGateway != "" {
		meta["id_at_gateway"] = txn.IdAtGateway
	}
	if txn.PaymentSourceId != "" {
		meta["payment_source_id"] = txn.PaymentSourceId
	}

	return &interfaces.CheckoutProviderResponse{
		ProviderSessionID:       cbInvoiceID,
		ProviderPaymentIntentID: txn.Id,
		ProviderMetadata:        meta,
		// No NextAction: the charge is settled off-session, nothing is asked of
		// the customer.
	}
}

// amountToMinorUnits shifts the decimal to the currency's minor unit. Going via
// float64 (as convertAmountToSmallestUnit does) can misround a value money must
// carry exactly.
func amountToMinorUnits(amount decimal.Decimal, currency string) int64 {
	return amount.Shift(types.GetCurrencyPrecision(currency)).Round(0).IntPart()
}

// idempotencyScoped namespaces an idempotency key per Chargebee operation.
// Chargebee treats the key as global, so the same key on two different endpoints
// is rejected with 422 unable_to_process_request.
func idempotencyScoped(key, op string) string {
	if key == "" {
		return ""
	}
	return key + ":" + op
}

// transactionOutcome is the three-way reading of a Chargebee transaction status.
// Treating everything non-success as failure is wrong: ACH and SEPA sit in
// in_progress for days and settle normally.
type transactionOutcome int

const (
	transactionSettled transactionOutcome = iota
	transactionPending
	transactionFailed
)

func classifyTransaction(status transactionEnum.Status) transactionOutcome {
	switch status {
	case transactionEnum.StatusSuccess:
		return transactionSettled
	case transactionEnum.StatusFailure, transactionEnum.StatusLateFailure,
		transactionEnum.StatusTimeout, transactionEnum.StatusVoided:
		return transactionFailed
	default:
		// in_progress, needs_attention, and anything Chargebee adds later: the
		// webhook settles it, and session expiry (plus the late-payment refund)
		// bounds the wait.
		return transactionPending
	}
}
