package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/domain/price"
	"github.com/flexprice/flexprice/internal/domain/proration"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	"github.com/flexprice/flexprice/internal/domain/wallet"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/idempotency"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
)

type LineItemProrationEntry struct {
	LineItem    *subscription.SubscriptionLineItem
	Price       *price.Price
	Action      types.ProrationAction
	OldQuantity decimal.Decimal // non-zero only for quantity changes
	NewQuantity decimal.Decimal // zero for remove; equals line item qty for add
}

type LineItemProrationRequest struct {
	Subscription   *subscription.Subscription
	Entries        []LineItemProrationEntry
	EffectiveDate  time.Time
	Behavior       types.ProrationBehavior
	Reason         string // shown in wallet credit description
	IdempotencyKey string
}

type LineItemProrationSummary struct {
	ChargeLineItems   []dto.CreateInvoiceLineItemRequest
	TotalChargeAmount decimal.Decimal

	// Negative amounts. Netting callers put these on the charge invoice; Apply
	// ignores them and pays TotalCreditAmount to the wallet instead.
	CreditLineItems   []dto.CreateInvoiceLineItemRequest
	TotalCreditAmount decimal.Decimal

	Currency  string
	IsPreview bool
}

// emptyProrationSummary is the zero quote for callers that resolved to "nothing to prorate".
func emptyProrationSummary(sub *subscription.Subscription) *LineItemProrationSummary {
	summary := &LineItemProrationSummary{
		TotalChargeAmount: decimal.Zero,
		TotalCreditAmount: decimal.Zero,
	}

	if sub != nil {
		summary.Currency = sub.Currency
	}

	return summary
}

// Merge folds other summaries into s. Per-entry effective dates force one Compute per
// distinct date, and the batch settles as one document.
func (s *LineItemProrationSummary) Merge(others ...*LineItemProrationSummary) *LineItemProrationSummary {
	for _, other := range others {
		if other == nil {
			continue
		}

		s.ChargeLineItems = append(s.ChargeLineItems, other.ChargeLineItems...)
		s.CreditLineItems = append(s.CreditLineItems, other.CreditLineItems...)
		s.TotalChargeAmount = s.TotalChargeAmount.Add(other.TotalChargeAmount)
		s.TotalCreditAmount = s.TotalCreditAmount.Add(other.TotalCreditAmount)
	}

	return s
}

func (s *LineItemProrationSummary) NetAmount() decimal.Decimal {
	if s == nil {
		return decimal.Zero
	}
	return s.TotalChargeAmount.Sub(s.TotalCreditAmount)
}

type SettleMode int

const (
	SettleModePreview SettleMode = iota // quotes, writes nothing
	SettleModeIssue                     // real invoice / real wallet top-up
	SettleModeDraft                     // pay-first: a DRAFT invoice to collect against
)

type SettleProrationRequest struct {
	Subscription *subscription.Subscription
	Quote        *LineItemProrationSummary

	// Invoice-level service window. Must span every line on the document: a batch has no
	// single effective date to derive it from, and coupon applicability reads it.
	PeriodStart time.Time
	PeriodEnd   time.Time

	// DisplayName titles the document and is required. BillingPeriod defaults to the
	// subscription's own period.
	DisplayName   string
	BillingPeriod types.BillingPeriod

	IdempotencyKey string
	Reason         string
	Mode           SettleMode

	// AttemptPayment collects the invoice in-line. Callers that settle inside a transaction
	// leave it false and attempt after commit.
	AttemptPayment bool
}

// NewSettleProrationRequest builds a settlement over an already-computed quote. Reason and
// BillingPeriod are optional; BillingPeriod falls back to the subscription's own.
func NewSettleProrationRequest(
	sub *subscription.Subscription,
	quote *LineItemProrationSummary,
	periodStart time.Time,
	periodEnd time.Time,
	displayName string,
	idempotencyKey string,
	mode SettleMode,
) *SettleProrationRequest {
	return &SettleProrationRequest{
		Subscription:   sub,
		Quote:          quote,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		DisplayName:    displayName,
		IdempotencyKey: idempotencyKey,
		Mode:           mode,
	}
}

type SettleProrationResult struct {
	Changed []dto.ChangedInvoice
	Draft   *dto.InvoiceResponse // set only for SettleModeDraft
}

type LineItemProrationService interface {
	Compute(ctx context.Context, req LineItemProrationRequest) (*LineItemProrationSummary, error)

	// Settle raises the quote's net as ONE document: a netted invoice when net > 0, a wallet
	// credit when net < 0, nothing at zero. The mode keeps preview, pay-later and pay-first
	// from drifting.
	Settle(ctx context.Context, req *SettleProrationRequest) (*SettleProrationResult, error)
}

type lineItemProrationService struct {
	params ServiceParams
}

func NewLineItemProrationService(params ServiceParams) LineItemProrationService {
	return &lineItemProrationService{params: params}
}

func (s *lineItemProrationService) Compute(ctx context.Context, req LineItemProrationRequest) (*LineItemProrationSummary, error) {
	sub := req.Subscription
	prorationSvc := NewProrationService(s.params)

	customerTimezone := sub.Timezone
	if customerTimezone == "" {
		customerTimezone = types.DefaultTimezone
	}

	summary := &LineItemProrationSummary{
		Currency:          sub.Currency,
		IsPreview:         req.Behavior == types.ProrationBehaviorNone,
		TotalChargeAmount: decimal.Zero,
		TotalCreditAmount: decimal.Zero,
	}

	billed := s.creditBasisForInvoiceLineItems(ctx, req)

	for _, entry := range req.Entries {
		item := entry.LineItem
		p := entry.Price

		if item.PriceType == types.PRICE_TYPE_USAGE {
			continue
		}

		// Onetime addons (EndDate set) are non-refundable.
		if entry.Action == types.ProrationActionRemoveItem && !item.EndDate.IsZero() {
			continue
		}

		// Arrear items are billed by the regular invoice at the end of each period. Charging
		// for them here would bill the same period twice, since the period-end invoice only
		// deduplicates against other subscription invoices, not against this one-off.
		if entry.Action == types.ProrationActionAddItem && p.InvoiceCadence == types.InvoiceCadenceArrear {
			continue
		}

		// A line item is priced against its own cadence, so a monthly addon on a quarterly
		// subscription is quoted as one partial month plus the whole months that follow —
		// the same lines the opening invoice would have raised. Same-cadence items yield a
		// single window equal to the subscription period, which is the previous behaviour.
		windows, err := splitInvoicePeriodByLineItemCadence(sub.CurrentPeriodStart, sub.CurrentPeriodEnd, item, sub)
		if err != nil {
			return nil, err
		}

		originalPaid, creditsIssued := creditBasis(item, p, billed)

		for _, w := range windows {
			if !w.End.After(req.EffectiveDate) {
				continue
			}

			params, skip := s.buildProrationParams(ctx, sub, entry, req, customerTimezone, w, originalPaid, creditsIssued)
			if skip {
				continue
			}

			result, err := prorationSvc.CalculateProration(ctx, params)
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}

			if result.NetAmount.GreaterThan(decimal.Zero) {
				lineItem := s.buildChargeLineItem(sub, entry, result.NetAmount, params.ProrationDate, w.End, p)
				summary.ChargeLineItems = append(summary.ChargeLineItems, lineItem)
				summary.TotalChargeAmount = summary.TotalChargeAmount.Add(result.NetAmount)
			} else if result.NetAmount.LessThan(decimal.Zero) {
				lineItem := s.buildChargeLineItem(sub, entry, result.NetAmount, params.ProrationDate, w.End, p)
				summary.CreditLineItems = append(summary.CreditLineItems, lineItem)
				summary.TotalCreditAmount = summary.TotalCreditAmount.Add(result.NetAmount.Abs())
				// Later windows may only credit what this line item has not been credited yet.
				creditsIssued = creditsIssued.Add(result.NetAmount.Abs())
			}
		}
	}

	return summary, nil
}

func (s *lineItemProrationService) Settle(ctx context.Context, req *SettleProrationRequest) (*SettleProrationResult, error) {
	if req == nil {
		return nil, ierr.NewError("settlement request is required").Mark(ierr.ErrValidation)
	}
	if req.DisplayName == "" {
		return nil, ierr.NewError("settlement display name is required").
			WithHint("Every proration document must be titled by its caller").
			Mark(ierr.ErrValidation)
	}

	result := &SettleProrationResult{Changed: make([]dto.ChangedInvoice, 0, 1)}
	net := req.Quote.NetAmount()

	switch {
	case net.IsPositive():
		invoiceReq := buildNettedProrationInvoiceRequest(req)

		switch req.Mode {
		case SettleModePreview:
			inv, err := NewInvoiceService(s.params).CreatePreviewInvoice(ctx, invoiceReq)
			if err != nil {
				return nil, err
			}
			result.Changed = append(result.Changed, dto.ChangedInvoice{
				Action:  dto.ChangedInvoiceActionCreated,
				Status:  dto.ChangedInvoiceStatusPreview,
				Invoice: inv,
			})

		case SettleModeDraft:
			invoiceReq.SourceType = types.InvoiceSourceTypeCheckout
			inv, skipped, err := NewInvoiceService(s.params).CreateComputedDraftInvoice(ctx, invoiceReq)
			if err != nil {
				return nil, err
			}
			if skipped {
				return nil, ierr.NewError("draft invoice was skipped").
					WithHint("Expected a non-zero invoice amount").
					WithReportableDetails(map[string]any{"invoice_id": inv.GetId()}).
					Mark(ierr.ErrValidation)
			}
			result.Draft = inv

		default:
			inv, err := s.issueInvoice(ctx, invoiceReq, req.AttemptPayment)
			if err != nil {
				return nil, err
			}
			result.Changed = append(result.Changed, dto.ChangedInvoice{
				ID:      inv.ID,
				Action:  dto.ChangedInvoiceActionCreated,
				Status:  dto.ChangedInvoiceStatusFromPaymentStatus(inv.PaymentStatus),
				Invoice: inv,
			})
		}

	case net.IsNegative():
		if req.Mode == SettleModeDraft {
			return nil, ierr.NewError("no proration charge to collect via checkout").
				WithHint("Expected a positive proration charge").
				Mark(ierr.ErrValidation)
		}

		credit, err := s.creditWallet(ctx, req, net.Abs())
		if err != nil {
			return nil, err
		}
		result.Changed = append(result.Changed, credit)

	case req.Mode == SettleModeDraft:
		return nil, ierr.NewError("no proration charge to collect via checkout").
			WithHint("Expected a positive proration charge").
			Mark(ierr.ErrValidation)
	}

	return result, nil
}

func (s *lineItemProrationService) creditWallet(
	ctx context.Context,
	req *SettleProrationRequest,
	amount decimal.Decimal,
) (dto.ChangedInvoice, error) {
	sub := req.Subscription

	if req.Mode == SettleModePreview {
		return walletCreditChangedInvoice(&dto.WalletTransactionResponse{
			Transaction: &wallet.Transaction{
				CustomerID:        sub.GetInvoicingCustomerID(),
				Amount:            amount,
				Currency:          sub.Currency,
				TransactionReason: types.TransactionReasonSubscriptionCredit,
			},
		}, dto.ChangedInvoiceStatusPreview), nil
	}

	walletTx, err := NewWalletService(s.params).TopUpWalletForProratedCharge(
		ctx, sub.GetInvoicingCustomerID(), amount, sub.Currency, req.IdempotencyKey,
	)
	if err != nil {
		s.params.Logger.Error(ctx, "failed to issue wallet credit for proration", "error", err)
		return dto.ChangedInvoice{}, err
	}

	return walletCreditChangedInvoice(walletTx, dto.ChangedInvoiceStatusWalletIssued), nil
}

// buildNettedProrationInvoiceRequest puts charge lines AND credit lines on one document.
// Credit lines already carry negative amounts, so they subtract naturally.
func buildNettedProrationInvoiceRequest(req *SettleProrationRequest) dto.CreateInvoiceRequest {
	sub, quote := req.Subscription, req.Quote

	lineItems := make([]dto.CreateInvoiceLineItemRequest, 0,
		len(quote.ChargeLineItems)+len(quote.CreditLineItems))
	lineItems = append(lineItems, quote.ChargeLineItems...)
	lineItems = append(lineItems, quote.CreditLineItems...)

	billingPeriod := string(req.BillingPeriod)
	if billingPeriod == "" {
		billingPeriod = string(sub.BillingPeriod)
	}
	net := quote.NetAmount()
	periodStart, periodEnd := req.PeriodStart, req.PeriodEnd

	return dto.CreateInvoiceRequest{
		CustomerID:     sub.GetInvoicingCustomerID(),
		SubscriptionID: &sub.ID,
		InvoiceType:    types.InvoiceTypeOneOff,
		Currency:       sub.Currency,
		BillingReason:  types.InvoiceBillingReasonSubscriptionUpdate,
		AmountDue:      net,
		Total:          net,
		Subtotal:       net,
		PeriodStart:    &periodStart,
		PeriodEnd:      &periodEnd,
		BillingPeriod:  &billingPeriod,
		LineItems:      lineItems,
		IdempotencyKey: &req.IdempotencyKey,
		Metadata:       types.WithCollapsedInvoiceDisplayName(nil, req.DisplayName),
	}
}

// Cap removal credits at amounts actually billed (list price never binds).
func (s *lineItemProrationService) creditBasisForInvoiceLineItems(
	ctx context.Context,
	req LineItemProrationRequest,
) map[string]*invoice.BilledAmounts {
	lineItemIDs := make([]string, 0, len(req.Entries))
	for _, entry := range req.Entries {
		if entry.Action == types.ProrationActionRemoveItem && entry.LineItem != nil {
			lineItemIDs = append(lineItemIDs, entry.LineItem.ID)
		}
	}
	if len(lineItemIDs) == 0 {
		return nil
	}

	billed, err := s.params.InvoiceLineItemRepo.GetBilledAmountsBySubscriptionLineItem(
		ctx, lineItemIDs, req.EffectiveDate,
	)
	if err != nil {
		s.params.Logger.Info(ctx, "failed to read billed amounts for credit basis, falling back to list price",
			"error", err,
			"subscription_id", req.Subscription.ID)
		return nil
	}

	return billed
}

func (s *lineItemProrationService) buildProrationParams(
	ctx context.Context,
	sub *subscription.Subscription,
	entry LineItemProrationEntry,
	req LineItemProrationRequest,
	customerTimezone string,
	window periodWindow,
	originalPaid decimal.Decimal,
	creditsIssued decimal.Decimal,
) (proration.ProrationParams, bool) {
	item := entry.LineItem
	p := entry.Price
	periodEnd := window.End.Add(-time.Second)

	// Windows that start after the change are charged or credited in full.
	prorationDate := req.EffectiveDate
	if window.Start.After(prorationDate) {
		prorationDate = window.Start
	}

	// The fraction is of one whole period of this line item. A window clipped short by a
	// calendar stub keeps the full period as its divisor, so the quote matches what the
	// invoice would charge for the same days.
	windowDuration := window.End.Sub(window.Start)
	periodStart := window.End.Add(-fullPeriodDuration(
		ctx, s.params.Logger, item, window.Start, customerTimezone, windowDuration,
	))

	base := proration.ProrationParams{
		SubscriptionID:     sub.ID,
		LineItemID:         item.ID,
		PlanPayInAdvance:   p.InvoiceCadence == types.InvoiceCadenceAdvance,
		CurrentPeriodStart: periodStart,
		CurrentPeriodEnd:   periodEnd,
		ProrationDate:      prorationDate,
		ProrationBehavior:  req.Behavior,
		ProrationStrategy:  types.StrategySecondBased,
		Currency:           sub.Currency,
		PlanDisplayName:    item.DisplayName,
		Timezone:           customerTimezone,
	}

	switch entry.Action {
	case types.ProrationActionAddItem:
		base.Action = types.ProrationActionAddItem
		base.NewPriceID = item.PriceID
		base.NewQuantity = item.Quantity
		base.NewPricePerUnit = p.Amount

	case types.ProrationActionRemoveItem:
		base.Action = types.ProrationActionRemoveItem
		base.OldPriceID = item.PriceID
		base.OldQuantity = item.Quantity
		base.OldPricePerUnit = p.Amount
		base.CancellationType = types.CancellationTypeImmediate
		base.CancellationReason = req.Reason
		base.RefundEligible = true
		base.OriginalAmountPaid, base.PreviousCreditsIssued = originalPaid, creditsIssued

	default:
		return proration.ProrationParams{}, true
	}

	return base, false
}

func (s *lineItemProrationService) buildChargeLineItem(
	sub *subscription.Subscription,
	entry LineItemProrationEntry,
	amount decimal.Decimal,
	effectiveDate time.Time,
	periodEnd time.Time,
	p *price.Price,
) dto.CreateInvoiceLineItemRequest {
	item := entry.LineItem
	priceID := item.PriceID
	priceType := string(p.Type)
	displayName := item.DisplayName
	subscriptionLineItemID := item.ID

	qty := item.Quantity
	if entry.Action == types.ProrationActionAddItem && !entry.NewQuantity.IsZero() {
		qty = entry.NewQuantity
	}

	var description string
	if entry.Action == types.ProrationActionAddItem {
		description = fmt.Sprintf("Proration charge: %s × %s %s %s/unit (%s – %s)",
			qty.String(), displayName,
			strings.ToUpper(sub.Currency), p.Amount.String(),
			effectiveDate.Format("2 Jan 2006"), periodEnd.Format("2 Jan 2006"))
	} else {
		description = fmt.Sprintf("Proration credit: %s × %s %s %s/unit (%s – %s)",
			qty.String(), displayName,
			strings.ToUpper(sub.Currency), p.Amount.String(),
			effectiveDate.Format("2 Jan 2006"), periodEnd.Format("2 Jan 2006"))
	}

	return dto.CreateInvoiceLineItemRequest{
		PriceID:                &priceID,
		PriceType:              &priceType,
		DisplayName:            &displayName,
		Amount:                 amount,
		Quantity:               qty,
		PeriodStart:            &effectiveDate,
		PeriodEnd:              &periodEnd,
		SubscriptionLineItemID: &subscriptionLineItemID,
		Metadata:               types.Metadata{"description": description},
	}
}

func prorationChargeInvoiceKey(req LineItemProrationRequest) string {
	source := req.IdempotencyKey
	if source == "" {
		ids := make([]string, 0, len(req.Entries))
		for _, entry := range req.Entries {
			ids = append(ids, entry.LineItem.ID)
			ids = append(ids, string(entry.Action))
		}
		sort.Strings(ids)
		source = strings.Join(ids, ",")
	}

	return idempotency.NewGenerator().GenerateKey(idempotency.ScopeProrationCharge, map[string]interface{}{
		"subscription_id": req.Subscription.ID,
		"effective_date":  req.EffectiveDate.UTC().Format(time.RFC3339Nano),
		"source":          source,
	})
}

func (s *lineItemProrationService) issueInvoice(
	ctx context.Context,
	req dto.CreateInvoiceRequest,
	attemptPayment bool,
) (*dto.InvoiceResponse, error) {
	invoiceSvc := NewInvoiceService(s.params)

	inv, err := invoiceSvc.CreateInvoice(ctx, req)
	if err != nil {
		s.params.Logger.Error(ctx, "failed to create proration charge invoice", "error", err)
		return nil, err
	}
	if !attemptPayment {
		return inv, nil
	}

	if err := invoiceSvc.AttemptPayment(ctx, inv.ID); err != nil {
		s.params.Logger.Info(ctx, "failed to attempt payment for proration charge invoice",
			"error", err, "invoice_id", inv.ID)
	}

	if latest, err := s.params.InvoiceRepo.Get(ctx, inv.ID); err == nil && latest != nil {
		inv.InvoiceStatus = latest.InvoiceStatus
		inv.PaymentStatus = latest.PaymentStatus
		inv.AmountPaid = latest.AmountPaid
		inv.AmountRemaining = latest.AmountRemaining
	}

	return inv, nil
}
