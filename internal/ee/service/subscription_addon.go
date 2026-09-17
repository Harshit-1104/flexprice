package service

import (
	"context"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/addonassociation"
	domainCheckout "github.com/flexprice/flexprice/internal/domain/checkout"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/shopspring/decimal"
)

// AttachAddon attaches an addon and settles the proration it raises.
func (s *subscriptionService) AttachAddon(
	ctx context.Context,
	sub *subscription.Subscription,
	req *dto.AddAddonToSubscriptionRequest,
	checkout *dto.CheckoutParams,
) (*dto.AddonChangeResult, error) {
	// Deliberately above the spine: the batch API does not inherit this guard, or the swap it
	// rejects could never be expressed.
	if !req.SkipEntityValidation {
		if err := s.validateEntitlementCompatibility(ctx, sub.ID, req.AddonID); err != nil {
			return nil, err
		}
	}

	changeSvc := NewAddonChangeService(s.ServiceParams)
	changeReq := AddonChangeRequest{
		Subscription: sub,
		Adds:         []AddonAdd{{Request: req}},
	}

	if req.PreviewOnly {
		config, settled, err := changeSvc.Preview(ctx, changeReq)
		if err != nil {
			return nil, err
		}

		return attachChangeResult(config, settled), nil
	}

	if checkout != nil {
		if err := validateAddonCheckout(sub, req, checkout); err != nil {
			return nil, err
		}

		config, err := changeSvc.Resolve(ctx, changeReq)
		if err != nil {
			return nil, err
		}

		if config.getQuote().TotalChargeAmount.GreaterThan(decimal.Zero) {
			resp, err := s.settleAddAddonPayFirst(ctx, config.getAttaches()[0], config.getQuote(), checkout)
			if err != nil {
				return nil, err
			}

			return &dto.AddonChangeResult{
				Association:     resp.AddonAssociation,
				CheckoutSession: resp.CheckoutSession,
				Invoice:         resp.Invoice,
			}, nil
		}

		// Zero or negative net → nothing to collect, so fall through and attach immediately.
		settled, err := changeSvc.Apply(ctx, config)
		if err != nil {
			return nil, err
		}

		return attachChangeResult(config, settled), nil
	}

	config, settled, err := changeSvc.Execute(ctx, changeReq)
	if err != nil {
		return nil, err
	}

	return attachChangeResult(config, settled), nil
}

func validateAddonCheckout(
	sub *subscription.Subscription,
	req *dto.AddAddonToSubscriptionRequest,
	checkout *dto.CheckoutParams,
) error {
	if err := checkout.Validate(); err != nil {
		return err
	}

	if len(req.OverrideLineItems) > 0 || len(req.LineItemCommitments) > 0 {
		return ierr.NewError("override_line_items and line_item_commitments are not supported with checkout").
			WithHint("Attach without checkout to use price overrides or line item commitments").
			WithReportableDetails(map[string]interface{}{
				"subscription_id": sub.ID,
				"addon_id":        req.AddonID,
			}).
			Mark(ierr.ErrValidation)
	}

	if sub.SubscriptionStatus != types.SubscriptionStatusActive {
		return ierr.NewError("subscription status does not allow a payment-gated addon attach").
			WithHint("Checkout is only supported for active subscriptions").
			WithReportableDetails(map[string]interface{}{
				"subscription_id":     sub.ID,
				"subscription_status": sub.SubscriptionStatus,
			}).
			Mark(ierr.ErrValidation)
	}

	return nil
}

func attachChangeResult(config *addonChangeConfig, settled *SettleProrationResult) *dto.AddonChangeResult {
	attach := config.getAttaches()[0]

	return &dto.AddonChangeResult{
		Association:      attach.getAssociation(),
		CreatedLineItems: attach.getLineItems(),
		ChangedInvoices:  settled.GetChanged(),
		EffectiveDate:    attach.getEffectiveDate(),
	}
}

func (s *subscriptionService) settleAddAddonPayFirst(
	ctx context.Context,
	params *addonAttachParams,
	summary *LineItemProrationSummary,
	checkout *dto.CheckoutParams,
) (*dto.AddAddonToSubscriptionResponse, error) {
	if params == nil || summary == nil || checkout == nil {
		return nil, ierr.NewError("pay-first addon attach requires a plan, proration and checkout").
			Mark(ierr.ErrValidation)
	}

	sub := params.getSubscription()
	req := params.getRequest()

	checkoutParams := &types.AddAddonParams{
		SubscriptionID: sub.ID,
		Addons: []types.AddAddonRef{{
			AssociationID:     params.getAssociation().ID,
			AddonID:           req.AddonID,
			Cadence:           req.Cadence,
			ProrationBehavior: req.ProrationBehavior,
			StartDate:         params.getRequestedStart(),
		}},
	}
	if err := checkoutParams.Validate(); err != nil {
		return nil, err
	}

	existing, err := s.getAnyPendingAddonCheckoutSession(ctx, sub.CustomerID, sub.ID)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, ierr.NewError("a pending checkout session already exists for this subscription").
			WithHint("Complete or cancel the existing checkout before starting another payment-gated change").
			WithReportableDetails(map[string]any{
				"subscription_id":     sub.ID,
				"checkout_session_id": existing[0].ID,
			}).
			Mark(ierr.ErrAlreadyExists)
	}

	pendingAssociation := params.getAssociation()
	pendingAssociation.AddonStatus = types.AddonStatusPending
	if err := s.AddonAssociationRepo.Create(ctx, pendingAssociation); err != nil {
		return nil, err
	}

	// The draft locks exactly what pay-later would have billed.
	drafted, err := NewLineItemProrationService(s.ServiceParams).Settle(ctx, NewSettleProrationRequest(
		sub, summary, params.getEffectiveDate(), sub.CurrentPeriodEnd,
		"Subscription update", params.prorationIdempotencyKey(), SettleModeDraft,
	))
	if err != nil {
		s.Logger.Error(ctx, "failed to create draft proration invoice for payment-gated addon attach",
			"error", err,
			"subscription_id", sub.ID,
			"association_id", pendingAssociation.ID,
		)
		s.archivePendingAddonAssociation(ctx, pendingAssociation, err)
		return nil, err
	}
	draftInvoice := drafted.Draft

	checkoutSvc := NewCheckoutSessionService(s.ServiceParams)
	sessionResp, err := checkoutSvc.StartPayFirstCheckoutSession(ctx, &dto.PayFirstCheckoutRequest{
		CustomerID: sub.CustomerID,
		Action:     types.CheckoutActionAddAddon,
		Configuration: types.CheckoutConfiguration{
			AddAddonParams: checkoutParams,
		},
		DraftInvoice: &draftInvoice.Invoice,
		Checkout:     checkout,
	})
	if err != nil {
		s.archivePendingAddonAssociation(ctx, pendingAssociation, err)
		return nil, err
	}

	latestInvoice, invErr := NewInvoiceService(s.ServiceParams).GetInvoice(ctx, draftInvoice.ID)
	if invErr != nil {
		latestInvoice = draftInvoice
	}

	return &dto.AddAddonToSubscriptionResponse{
		AddonAssociation: pendingAssociation,
		CheckoutSession:  sessionResp,
		Invoice:          latestInvoice,
	}, nil
}

func (s *subscriptionService) getAnyPendingAddonCheckoutSession(
	ctx context.Context,
	customerID string,
	subscriptionID string,
) ([]*domainCheckout.CheckoutSession, error) {
	filter := &types.CheckoutSessionFilter{
		QueryFilter: types.NewNoLimitPublishedQueryFilter(),
		CustomerIDs: []string{customerID},
		Actions: []types.CheckoutAction{
			types.CheckoutActionModifySubscription,
			types.CheckoutActionAddAddon,
		},
		CheckoutStatuses: []types.CheckoutStatus{
			types.CheckoutStatusInitiated,
			types.CheckoutStatusPending,
		},
		Configuration: &types.CheckoutConfigurationFilter{SubscriptionID: subscriptionID},
	}
	filter.Limit = lo.ToPtr(1)

	return s.CheckoutSessionRepo.List(ctx, filter)
}

func (s *subscriptionService) archivePendingAddonAssociation(
	ctx context.Context,
	association *addonassociation.AddonAssociation,
	cause error,
) {
	if err := s.AddonAssociationRepo.Delete(ctx, association.ID); err != nil {
		s.Logger.Error(ctx, "failed to archive pending addon association after pay-first failure",
			"error", err,
			"association_id", association.ID,
			"original_error", cause,
		)
	}
}

// applyAddAddonParams activates the pending associations a completed checkout session gated.
//
// The charge is NOT recomputed — it is already locked on the session's draft invoice, and
// settling is finalizeCheckoutInvoiceAndPayment's job. Credit-grant proration is not
// suppressed, so grants scale exactly as they would have pay-later.
func (s *subscriptionService) applyAddAddonCheckoutParams(ctx context.Context, params *types.AddAddonParams) error {
	if err := params.Validate(); err != nil {
		return err
	}

	sub, lineItems, err := s.SubRepo.GetWithLineItems(ctx, params.SubscriptionID)
	if err != nil {
		return err
	}
	sub.LineItems = lineItems

	for _, ref := range params.Addons {
		if err := s.applyAddAddonRef(ctx, sub, ref); err != nil {
			return err
		}
	}

	return nil
}

func (s *subscriptionService) applyAddAddonRef(
	ctx context.Context,
	sub *subscription.Subscription,
	ref types.AddAddonRef,
) error {
	association, err := s.AddonAssociationRepo.GetByID(ctx, ref.AssociationID)
	if err != nil {
		return err
	}

	switch association.AddonStatus {
	case types.AddonStatusActive:
		s.Logger.Info(ctx, "addon association already active, skipping checkout replay",
			"association_id", association.ID,
			"addon_id", ref.AddonID,
			"subscription_id", sub.ID,
		)
		return nil
	case types.AddonStatusPending:
		// The state we expect; fall through and activate.
	default:
		return ierr.NewError("addon association is not pending activation").
			WithHint("The addon was cancelled or removed while its checkout was outstanding").
			WithReportableDetails(map[string]any{
				"association_id":  association.ID,
				"addon_id":        ref.AddonID,
				"subscription_id": sub.ID,
				"addon_status":    association.AddonStatus,
			}).
			Mark(ierr.ErrValidation)
	}

	req := &dto.AddAddonToSubscriptionRequest{
		AddonID:              ref.AddonID,
		Cadence:              ref.Cadence,
		StartDate:            lo.ToPtr(ref.StartDate),
		ProrationBehavior:    ref.ProrationBehavior,
		Metadata:             association.Metadata,
		SkipEntityValidation: true,
	}

	changeSvc := NewAddonChangeService(s.ServiceParams)
	config, err := changeSvc.Resolve(ctx, AddonChangeRequest{
		Subscription: sub,
		Adds:         []AddonAdd{{Request: req, Existing: association}},
	})
	if err != nil {
		return err
	}

	// No settlement: the charge is already locked on the session's draft invoice.
	return s.DB.WithTx(ctx, func(ctx context.Context) error {
		return changeSvc.Persist(ctx, config)
	})
}

// DetachAddon removes an addon and credits back the unused prepaid time it paid for. It is a
// one-entry adapter over AddonChangeService.
func (s *subscriptionService) DetachAddon(
	ctx context.Context,
	req *dto.RemoveAddonRequest,
	subscriptionId string,
) (*dto.AddonChangeResult, error) {
	sub, err := s.subscriptionForDetach(ctx, req.AddonAssociationID, subscriptionId)
	if err != nil {
		return nil, err
	}

	changeSvc := NewAddonChangeService(s.ServiceParams)
	changeReq := AddonChangeRequest{
		Subscription: sub,
		Removes:      []*dto.RemoveAddonRequest{req},
	}

	if req.PreviewOnly {
		config, settled, err := changeSvc.Preview(ctx, changeReq)
		if err != nil {
			return nil, err
		}

		detach := config.getDetaches()[0]
		// The cancelled association Persist would write, built but not saved.
		cancelled := addonassociation.NewAddonAssociationBuilder(detach.getAssociation()).
			WithCancellation(detach.getEffectiveDate(), detach.getReason()).
			Build()

		return &dto.AddonChangeResult{
			Association:     cancelled,
			EndedLineItems:  detach.getLineItems(),
			ChangedInvoices: settled.GetChanged(),
			EffectiveDate:   detach.getEffectiveDate(),
		}, nil
	}

	config, settled, err := changeSvc.Execute(ctx, changeReq)
	if err != nil {
		return nil, err
	}

	detach := config.getDetaches()[0]

	return &dto.AddonChangeResult{
		Association:     detach.getAssociation(),
		EndedLineItems:  detach.getLineItems(),
		ChangedInvoices: settled.GetChanged(),
		EffectiveDate:   detach.getEffectiveDate(),
	}, nil
}

// subscriptionForDetach resolves the subscription a removal targets. The deprecated
// DELETE /subscriptions/addon route carries only the association, so it is read back off that.
func (s *subscriptionService) subscriptionForDetach(
	ctx context.Context,
	associationID string,
	subscriptionID string,
) (*subscription.Subscription, error) {
	if subscriptionID == "" {
		association, err := s.AddonAssociationRepo.GetByID(ctx, associationID)
		if err != nil {
			return nil, err
		}
		subscriptionID = association.EntityID
	}

	return s.SubRepo.Get(ctx, subscriptionID)
}

// createAddonDetachParams resolves everything a removal needs — validations, the association,
// the line items still to close and the effective date — and writes NOTHING.
func (s *subscriptionService) createAddonDetachParams(
	ctx context.Context,
	sub *subscription.Subscription,
	req *dto.RemoveAddonRequest,
) (*addonDetachParams, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	association, err := s.AddonAssociationRepo.GetByID(ctx, req.AddonAssociationID)
	if err != nil {
		return nil, err
	}

	if association.EntityID != sub.ID {
		return nil, ierr.NewError("addon association does not belong to this subscription").
			WithHint("The addon association belongs to a different subscription").
			WithReportableDetails(map[string]interface{}{
				"addon_association_id": association.ID,
				"subscription_id":      sub.ID,
			}).
			Mark(ierr.ErrValidation)
	}

	if association.AddonStatus == types.AddonStatusPending {
		return nil, ierr.NewError("addon attach is pending payment").
			WithHint("Complete or cancel the pending checkout for this addon first").
			WithReportableDetails(map[string]interface{}{
				"addon_association_id": association.ID,
				"addon_id":             association.AddonID,
			}).
			Mark(ierr.ErrValidation)
	}

	if association.EndDate != nil {
		return nil, ierr.NewError("addon is already scheduled to be removed").
			WithHint("This addon is already marked for removal").
			WithReportableDetails(map[string]interface{}{
				"addon_association_id": association.ID,
				"end_date":             association.EndDate,
			}).
			Mark(ierr.ErrValidation)
	}

	lineItemFilter := types.NewSubscriptionLineItemFilter()
	lineItemFilter.SubscriptionIDs = []string{association.EntityID}
	lineItemFilter.EntityIDs = []string{association.AddonID}
	lineItemFilter.EntityType = lo.ToPtr(types.SubscriptionLineItemEntityTypeAddon)
	lineItemFilter.AddonAssociationIDs = []string{association.ID}

	lineItems, err := s.SubscriptionLineItemRepo.List(ctx, lineItemFilter)
	if err != nil {
		return nil, err
	}

	// Onetime addons have EndDate set on ALL their line items — they are already scheduled to end.
	// We check ALL items: if any item has no EndDate (recurring), the addon is cancellable.
	// This handles the case where a previous association was cancelled at period-end (EndDate set)
	// while a new recurring association was added on top (EndDate zero).
	var onetimeEndDate time.Time
	allOnetime := len(lineItems) > 0
	for _, li := range lineItems {
		if li.EndDate.IsZero() {
			allOnetime = false
			break
		}
		onetimeEndDate = li.EndDate
	}
	if allOnetime {
		return nil, ierr.NewError("addon is already scheduled to end").
			WithHintf("This addon is already scheduled to end at %s", onetimeEndDate.Format("2 Jan 2006")).
			WithReportableDetails(map[string]interface{}{
				"addon_association_id": association.ID,
				"expires_at":           onetimeEndDate,
			}).
			Mark(ierr.ErrValidation)
	}

	// Keep only line items that are NOT already scheduled to end.
	// Line items from a previous association cancelled at period-end have EndDate set
	// and must be excluded — they are already handled and must not be re-processed.
	var activeLineItems []*subscription.SubscriptionLineItem
	for _, li := range lineItems {
		if li.EndDate.IsZero() {
			activeLineItems = append(activeLineItems, li)
		}
	}

	effectiveEndDate := sub.CurrentPeriodEnd
	if req.EffectiveDate != nil {
		effectiveEndDate = *req.EffectiveDate
		if effectiveEndDate.Before(sub.CurrentPeriodStart) || effectiveEndDate.After(sub.CurrentPeriodEnd) {
			return nil, ierr.NewError("effective_date is outside the current billing period").
				WithHint("effective_date must be between the subscription's current period start and end").
				WithReportableDetails(map[string]any{
					"effective_date":       effectiveEndDate,
					"current_period_start": sub.CurrentPeriodStart,
					"current_period_end":   sub.CurrentPeriodEnd,
				}).
				Mark(ierr.ErrValidation)
		}
	}

	return &addonDetachParams{
		subscription:  sub,
		association:   association,
		lineItems:     activeLineItems,
		effectiveDate: effectiveEndDate,
		behavior:      req.ProrationBehavior,
		reason:        req.Reason,
	}, nil
}
