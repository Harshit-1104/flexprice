package service

import (
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// The `addons` modify type. Batch behaviour is pinned in subscription_addon_change_test.go;
// these cover the dispatch layer — mapping, response shape, preview/execute agreement.

func (s *SubscriptionServiceSuite) modificationService() SubscriptionModificationService {
	return NewSubscriptionModificationService(s.service.(*subscriptionService).ServiceParams)
}

func (s *SubscriptionServiceSuite) addonsRequest(params *dto.SubModifyAddonsParams) dto.ExecuteSubscriptionModifyRequest {
	return dto.ExecuteSubscriptionModifyRequest{
		Type:         dto.SubscriptionModifyTypeAddons,
		AddonsParams: params,
	}
}

func (s *SubscriptionServiceSuite) modifyAdd(addonID string, at time.Time) *dto.AddAddonToSubscriptionRequest {
	return &dto.AddAddonToSubscriptionRequest{
		AddonID:           addonID,
		Cadence:           types.AddonCadenceRecurring,
		ProrationBehavior: types.ProrationBehaviorCreateProrations,
		StartDate:         lo.ToPtr(at),
	}
}

func (s *SubscriptionServiceSuite) modifyRemove(associationID string, at time.Time) *dto.RemoveAddonRequest {
	return &dto.RemoveAddonRequest{
		AddonAssociationID: associationID,
		ProrationBehavior:  types.ProrationBehaviorCreateProrations,
		EffectiveDate:      lo.ToPtr(at),
	}
}

func (s *SubscriptionServiceSuite) changedLineItemsByAction(
	resp *dto.SubscriptionModifyResponse,
	action dto.ChangedLineItemAction,
) []dto.ChangedLineItem {
	return lo.Filter(resp.ChangedResources.LineItems, func(li dto.ChangedLineItem, _ int) bool {
		return li.ChangeAction == action
	})
}

func (s *SubscriptionServiceSuite) TestExecuteAddonsModification_Swap_OneNettedInvoice() {
	ctx := s.GetContext()
	sub := s.monthlyPeriodSubscription()

	s.seedFixedPriceAddon("addon_mod_out", decimal.NewFromInt(30), types.InvoiceCadenceAdvance)
	s.seedFixedPriceAddon("addon_mod_in", decimal.NewFromInt(60), types.InvoiceCadenceAdvance)
	outgoing := s.attachForRemoval("addon_mod_out", 30)

	at := sub.CurrentPeriodStart.Add(15 * 24 * time.Hour)
	resp, err := s.modificationService().Execute(ctx, sub.ID, s.addonsRequest(&dto.SubModifyAddonsParams{
		Adds:    []*dto.AddAddonToSubscriptionRequest{s.modifyAdd("addon_mod_in", at)},
		Removes: []*dto.RemoveAddonRequest{s.modifyRemove(outgoing, at)},
	}))
	s.Require().NoError(err)

	invoices := s.oneOffInvoicesFor(sub.ID)
	s.Require().Len(invoices, 1, "a swap settles as one document through the modify endpoint too")
	s.Empty(s.prorationCredits(), "a net-positive swap must not also credit the wallet")

	s.Require().Len(resp.ChangedResources.Invoices, 1)
	s.Len(s.changedLineItemsByAction(resp, dto.ChangedLineItemActionCreated), 1, "the incoming addon is reported")
	s.Len(s.changedLineItemsByAction(resp, dto.ChangedLineItemActionEnded), 1, "the outgoing addon is reported")
	s.Nil(resp.CheckoutSession, "pay-later carries no checkout session")

	s.Len(s.addonLineItemsFor(sub.ID, "addon_mod_in"), 1)
}

// The single-addon path stamps one date on every ended item, misreporting a batch whose
// entries land on different days.
func (s *SubscriptionServiceSuite) TestExecuteAddonsModification_PerEntryDates_EndedItemsCarryTheirOwnDate() {
	ctx := s.GetContext()
	sub := s.monthlyPeriodSubscription()

	s.seedFixedPriceAddon("addon_mod_d1", decimal.NewFromInt(30), types.InvoiceCadenceAdvance)
	s.seedFixedPriceAddon("addon_mod_d2", decimal.NewFromInt(30), types.InvoiceCadenceAdvance)
	first := s.attachForRemoval("addon_mod_d1", 30)
	second := s.attachForRemoval("addon_mod_d2", 30)

	early := sub.CurrentPeriodStart.Add(5 * 24 * time.Hour)
	late := sub.CurrentPeriodStart.Add(20 * 24 * time.Hour)

	resp, err := s.modificationService().Execute(ctx, sub.ID, s.addonsRequest(&dto.SubModifyAddonsParams{
		Removes: []*dto.RemoveAddonRequest{
			s.modifyRemove(first, early),
			s.modifyRemove(second, late),
		},
	}))
	s.Require().NoError(err)

	ended := s.changedLineItemsByAction(resp, dto.ChangedLineItemActionEnded)
	s.Require().Len(ended, 2)

	dates := lo.Map(ended, func(li dto.ChangedLineItem, _ int) time.Time { return lo.FromPtr(li.EndDate) })
	s.True(lo.SomeBy(dates, func(d time.Time) bool { return d.Equal(early) }), "one item ends on the early date")
	s.True(lo.SomeBy(dates, func(d time.Time) bool { return d.Equal(late) }), "the other ends on the late date")
}

func (s *SubscriptionServiceSuite) TestPreviewAddonsModification_WritesNothingAndQuotesTheExecutedNet() {
	ctx := s.GetContext()
	sub := s.monthlyPeriodSubscription()

	s.seedFixedPriceAddon("addon_mod_p1", decimal.NewFromInt(30), types.InvoiceCadenceAdvance)
	s.seedFixedPriceAddon("addon_mod_p2", decimal.NewFromInt(40), types.InvoiceCadenceAdvance)

	at := sub.CurrentPeriodStart.Add(12 * 24 * time.Hour)
	req := s.addonsRequest(&dto.SubModifyAddonsParams{
		Adds: []*dto.AddAddonToSubscriptionRequest{
			s.modifyAdd("addon_mod_p1", at),
			s.modifyAdd("addon_mod_p2", at),
		},
	})

	previewed, err := s.modificationService().Preview(ctx, sub.ID, req)
	s.Require().NoError(err)

	s.Empty(s.addonLineItemsFor(sub.ID, "addon_mod_p1"), "preview writes no line items")
	s.Empty(s.oneOffInvoicesFor(sub.ID), "preview raises no invoice")
	s.Require().Len(previewed.ChangedResources.Invoices, 1)
	s.Len(s.changedLineItemsByAction(previewed, dto.ChangedLineItemActionCreated), 2)
	for _, li := range previewed.ChangedResources.LineItems {
		s.Equal(previewCreatedLineItemID, li.ID, "preview reports no real line item IDs")
	}

	executed, err := s.modificationService().Execute(ctx, sub.ID, req)
	s.Require().NoError(err)

	s.Require().Len(executed.ChangedResources.Invoices, 1)
	s.True(lo.FromPtr(executed.ChangedResources.Invoices[0].Invoice).AmountDue.
		Equal(lo.FromPtr(previewed.ChangedResources.Invoices[0].Invoice).AmountDue),
		"execute bills exactly what preview quoted")
}

func (s *SubscriptionServiceSuite) TestExecuteAddonsModification_Rollback_LeavesNothingBehind() {
	ctx := s.GetContext()
	sub := s.monthlyPeriodSubscription()

	s.seedFixedPriceAddon("addon_mod_ok", decimal.NewFromInt(30), types.InvoiceCadenceAdvance)

	at := sub.CurrentPeriodStart.Add(10 * 24 * time.Hour)
	_, err := s.modificationService().Execute(ctx, sub.ID, s.addonsRequest(&dto.SubModifyAddonsParams{
		Adds: []*dto.AddAddonToSubscriptionRequest{
			s.modifyAdd("addon_mod_ok", at),
			s.modifyAdd("addon_does_not_exist", at),
		},
	}))
	s.Require().Error(err)

	s.Empty(s.addonLineItemsFor(sub.ID, "addon_mod_ok"),
		"the first attach must roll back with the batch, not persist alone")
	s.Empty(s.oneOffInvoicesFor(sub.ID), "a failed batch raises no invoice")
}

func (s *SubscriptionServiceSuite) TestAddonsModification_InvalidRequestRejected() {
	ctx := s.GetContext()
	sub := s.monthlyPeriodSubscription()

	_, err := s.modificationService().Execute(ctx, sub.ID, s.addonsRequest(&dto.SubModifyAddonsParams{}))
	s.Error(err, "a batch with no entries is rejected")

	_, err = s.modificationService().Execute(ctx, sub.ID, dto.ExecuteSubscriptionModifyRequest{
		Type: dto.SubscriptionModifyTypeAddons,
	})
	s.Error(err, "type addons without addons_params is rejected")
}
