package service

import (
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
)

// Settle is additive in this PR — no caller uses it yet — so these tests are what prove the
// netted path works before C3/C4/C5 route the four settlement sites onto it.

// mixedQuote is the swap that nets to zero today: 13.33 charged and 13.33 credited.
func (s *LineItemProrationServiceSuite) mixedQuote(effectiveDate time.Time) (*LineItemProrationSummary, *LineItemProrationRequest) {
	outgoing := s.secondLineItem()
	s.recordBilled(outgoing.ID, decimal.NewFromInt(20))

	req := LineItemProrationRequest{
		Subscription:   s.subCopyWithPeriod(s.td.periodStart, s.td.periodEnd),
		EffectiveDate:  effectiveDate,
		Behavior:       types.ProrationBehaviorCreateProrations,
		IdempotencyKey: "settle_mixed",
		Entries: []LineItemProrationEntry{
			{
				LineItem:    s.td.lineItem,
				Price:       s.td.fixedPrice,
				Action:      types.ProrationActionAddItem,
				NewQuantity: s.td.lineItem.Quantity,
			},
			{LineItem: outgoing, Price: s.td.fixedPrice, Action: types.ProrationActionRemoveItem},
		},
	}

	quote, err := s.svc.Compute(s.GetContext(), req)
	s.Require().NoError(err)
	return quote, &req
}

// applyViaSettle is what the production callers now do: Compute, then Settle the net as one
// document. The key convention mirrors theirs — an addition invoices under the hashed key, a
// removal credits the wallet under the caller's raw key.
func (s *LineItemProrationServiceSuite) applyViaSettle(
	req LineItemProrationRequest,
) ([]dto.ChangedInvoice, error) {
	if req.Behavior != types.ProrationBehaviorCreateProrations {
		return nil, nil
	}

	ctx := s.GetContext()
	quote, err := s.svc.Compute(ctx, req)
	if err != nil {
		return nil, err
	}

	key := prorationChargeInvoiceKey(req)
	if quote.NetAmount().IsNegative() {
		key = req.IdempotencyKey
	}

	settleReq := NewSettleProrationRequest(
		req.Subscription, quote, req.EffectiveDate, req.Subscription.CurrentPeriodEnd,
		"Subscription update", key, SettleModeIssue,
	)
	settleReq.Reason = req.Reason
	settleReq.AttemptPayment = true

	settled, err := s.svc.Settle(ctx, settleReq)
	if err != nil {
		return nil, err
	}

	return settled.Changed, nil
}

func (s *LineItemProrationServiceSuite) settleReq(
	quote *LineItemProrationSummary,
	effectiveDate time.Time,
	mode SettleMode,
) *SettleProrationRequest {
	return &SettleProrationRequest{
		Subscription:   s.subCopyWithPeriod(s.td.periodStart, s.td.periodEnd),
		Quote:          quote,
		PeriodStart:    effectiveDate,
		PeriodEnd:      s.td.periodEnd,
		DisplayName:    "Subscription update",
		IdempotencyKey: "settle_test_key",
		Mode:           mode,
	}
}

func (s *LineItemProrationServiceSuite) TestSettle_NetCharge_IssuesOneNettedInvoice() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	// Charge more than is credited so the batch nets positive.
	quote.TotalChargeAmount = quote.TotalChargeAmount.Add(decimal.NewFromInt(10))
	quote.ChargeLineItems[0].Amount = quote.ChargeLineItems[0].Amount.Add(decimal.NewFromInt(10))

	invoicesBefore := s.invoiceCount()
	walletBefore := s.walletBalance()

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModeIssue))
	s.Require().NoError(err)

	s.Require().Len(result.Changed, 1, "one document for the batch, never two")
	s.Equal(invoicesBefore+1, s.invoiceCount())
	s.Equal(walletBefore.String(), s.walletBalance().String(), "a net charge never touches the wallet")

	inv := result.Changed[0].Invoice
	s.Require().NotNil(inv)
	s.Equal("10.00", inv.AmountDue.StringFixed(2), "billed the net, not the gross charge")
	s.Len(inv.LineItems, 2, "charge AND credit lines ride the same document")
}

func (s *LineItemProrationServiceSuite) TestSettle_NetCredit_PaysWalletAndRaisesNoInvoice() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	quote.TotalCreditAmount = quote.TotalCreditAmount.Add(decimal.NewFromInt(10))

	invoicesBefore := s.invoiceCount()

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModeIssue))
	s.Require().NoError(err)

	s.Require().Len(result.Changed, 1)
	s.Equal(invoicesBefore, s.invoiceCount(), "a net credit is never invoiced")
	s.Equal("10.00", s.walletBalance().StringFixed(2), "the wallet receives the net, not the gross credit")
	s.Equal(dto.ChangedInvoiceStatusWalletIssued, result.Changed[0].Status)
}

func (s *LineItemProrationServiceSuite) TestSettle_ZeroNet_WritesNothing() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	s.Require().Equal("0.00", quote.NetAmount().StringFixed(2))

	invoicesBefore := s.invoiceCount()

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModeIssue))
	s.Require().NoError(err)

	s.Empty(result.Changed, "a swap that nets to zero settles nothing")
	s.Nil(result.Draft)
	s.Equal(invoicesBefore, s.invoiceCount())
	s.Equal("0.00", s.walletBalance().StringFixed(2))
}

func (s *LineItemProrationServiceSuite) TestSettle_Preview_QuotesWithoutWriting() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	quote.TotalChargeAmount = quote.TotalChargeAmount.Add(decimal.NewFromInt(10))
	quote.ChargeLineItems[0].Amount = quote.ChargeLineItems[0].Amount.Add(decimal.NewFromInt(10))

	invoicesBefore := s.invoiceCount()

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModePreview))
	s.Require().NoError(err)

	s.Require().Len(result.Changed, 1)
	s.Equal(dto.ChangedInvoiceStatusPreview, result.Changed[0].Status)
	s.Equal(invoicesBefore, s.invoiceCount(), "preview persists no invoice")
	s.Equal("0.00", s.walletBalance().StringFixed(2))

	s.Require().NotNil(result.Changed[0].Invoice)
	s.Equal("10.00", result.Changed[0].Invoice.AmountDue.StringFixed(2),
		"the quoted net is what execute would bill")
}

func (s *LineItemProrationServiceSuite) TestSettle_PreviewNetCredit_QuotesWalletWithoutPaying() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	quote.TotalCreditAmount = quote.TotalCreditAmount.Add(decimal.NewFromInt(10))

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModePreview))
	s.Require().NoError(err)

	s.Require().Len(result.Changed, 1)
	s.Equal(dto.ChangedInvoiceStatusPreview, result.Changed[0].Status)
	s.Equal("0.00", s.walletBalance().StringFixed(2), "preview pays nothing out")
}

func (s *LineItemProrationServiceSuite) TestSettle_Draft_LocksTheNetForCheckout() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	quote.TotalChargeAmount = quote.TotalChargeAmount.Add(decimal.NewFromInt(10))
	quote.ChargeLineItems[0].Amount = quote.ChargeLineItems[0].Amount.Add(decimal.NewFromInt(10))

	result, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModeDraft))
	s.Require().NoError(err)

	s.Empty(result.Changed)
	s.Require().NotNil(result.Draft)
	s.Equal(types.InvoiceStatusDraft, result.Draft.InvoiceStatus)
	s.Equal("10.00", result.Draft.AmountDue.StringFixed(2),
		"the customer is asked for exactly what pay-later would have billed")
}

// Pay-first has nothing to collect when the batch does not net positive, so the caller must
// fall through and apply immediately rather than open a checkout.
func (s *LineItemProrationServiceSuite) TestSettle_Draft_RejectsNonPositiveNet() {
	ctx := s.GetContext()
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)

	_, err := s.svc.Settle(ctx, s.settleReq(quote, effectiveDate, SettleModeDraft))
	s.Require().Error(err)
}

func (s *LineItemProrationServiceSuite) TestSettle_RejectsMissingDisplayName() {
	effectiveDate := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)

	quote, _ := s.mixedQuote(effectiveDate)
	req := s.settleReq(quote, effectiveDate, SettleModeIssue)
	req.DisplayName = ""

	_, err := s.svc.Settle(s.GetContext(), req)
	s.Require().Error(err)
}

func (s *LineItemProrationServiceSuite) TestMerge_SumsBucketsAndLines() {
	a := &LineItemProrationSummary{
		ChargeLineItems:   []dto.CreateInvoiceLineItemRequest{{Amount: decimal.NewFromInt(10)}},
		TotalChargeAmount: decimal.NewFromInt(10),
		TotalCreditAmount: decimal.Zero,
	}
	b := &LineItemProrationSummary{
		CreditLineItems:   []dto.CreateInvoiceLineItemRequest{{Amount: decimal.NewFromInt(-4)}},
		TotalChargeAmount: decimal.Zero,
		TotalCreditAmount: decimal.NewFromInt(4),
	}

	merged := emptyProrationSummary(s.td.sub).Merge(a, nil, b)

	s.Len(merged.ChargeLineItems, 1)
	s.Len(merged.CreditLineItems, 1)
	s.Equal("10", merged.TotalChargeAmount.String())
	s.Equal("4", merged.TotalCreditAmount.String())
	s.Equal("6", merged.NetAmount().String())
}
