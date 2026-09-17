package service

import (
	"context"
	"fmt"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
)

// SubscriptionGrantService owns credit grants and entitlement grants for any subscription
// change — addon attach/detach, plan change, future flows — so a change touching several
// sources at once resolves and writes them as one batch instead of one pass per source.
type SubscriptionGrantService interface {
	// Resolve computes every credit grant the change implies. Reads only, so it is safe
	// to call outside a transaction and from preview.
	Resolve(ctx context.Context, req GrantChangeRequest) (*GrantChangeConfig, error)

	// Apply writes the resolved config. Must run inside the caller's transaction.
	Apply(ctx context.Context, cfg *GrantChangeConfig) error
}

// GrantSource is one contributor to the change, with its own dates and provenance.
type GrantSource struct {
	// StartDate anchors this source's grant chain.
	StartDate time.Time
	// EndDate caps recurring grants at a time-bounded (onetime) addon's boundary.
	EndDate  *time.Time
	Behavior types.ProrationBehavior
	Origin   grantProrationSource
	// AddonID is credit-grant provenance: templates are read from it and removal
	// targets the grants it materialized.
	AddonID string
}

// GrantChangeRequest describes a subscription change as the sources joining it and the
// sources leaving it.
type GrantChangeRequest struct {
	Sub *subscription.Subscription

	Incoming []GrantSource
	Removed  []GrantSource
}

// GrantChangeConfig is a resolved GrantChangeRequest: what Apply will write, and nothing
// that still needs a read to determine.
type GrantChangeConfig struct {
	sub *subscription.Subscription

	creditGroups  []creditGrantGroup
	cancellations []creditGrantCancellation
}

// creditGrantGroup is the set of grant requests sharing one anchoring, so
// materializeCreditGrants runs once per group rather than once per source.
type creditGrantGroup struct {
	requests  []dto.CreateCreditGrantRequest
	startDate time.Time
	endDate   *time.Time
	proration *dto.FirstPeriodProration
}

type creditGrantCancellation struct {
	addonIDs      []string
	effectiveDate time.Time
}

type subscriptionGrantService struct {
	ServiceParams
}

func NewSubscriptionGrantService(params ServiceParams) SubscriptionGrantService {
	return newSubscriptionGrantService(params)
}

func newSubscriptionGrantService(params ServiceParams) *subscriptionGrantService {
	return &subscriptionGrantService{ServiceParams: params}
}

func (s *subscriptionGrantService) Resolve(ctx context.Context, req GrantChangeRequest) (*GrantChangeConfig, error) {
	if req.Sub == nil {
		return nil, ierr.NewError("subscription is required to resolve grant changes").
			Mark(ierr.ErrValidation)
	}

	cfg := &GrantChangeConfig{sub: req.Sub}

	creditGroups, err := s.resolveCreditGrantGroups(ctx, req.Sub, req.Incoming)
	if err != nil {
		return nil, err
	}

	cfg.creditGroups = creditGroups
	cfg.cancellations = resolveCreditGrantCancellations(req.Removed)

	return cfg, nil
}

func (s *subscriptionGrantService) Apply(ctx context.Context, cfg *GrantChangeConfig) error {
	if cfg == nil {
		return nil
	}

	creditGrantService := NewCreditGrantService(s.ServiceParams)
	for _, group := range cfg.creditGroups {
		if err := creditGrantService.CreateSubscriptionGrants(ctx, dto.CreateSubscriptionGrantsRequest{
			Subscription:         cfg.sub,
			Grants:               group.requests,
			StartDate:            group.startDate,
			EndDate:              group.endDate,
			FirstPeriodProration: group.proration,
		}); err != nil {
			return err
		}
	}

	for _, cancellation := range cfg.cancellations {
		if err := creditGrantService.CancelFutureSubscriptionGrants(ctx, dto.CancelFutureSubscriptionGrantsRequest{
			SubscriptionID: cfg.sub.ID,
			AddonIDs:       cancellation.addonIDs,
			EffectiveDate:  lo.ToPtr(cancellation.effectiveDate),
		}); err != nil {
			return err
		}
	}

	return nil
}

// resolveCreditGrantGroups clones each incoming addon's ADDON-scoped credit grant templates
// into SUBSCRIPTION-scoped requests, then groups the sources that share an anchoring so the
// whole batch materializes in as few passes as there are distinct anchorings.
func (s *subscriptionGrantService) resolveCreditGrantGroups(
	ctx context.Context,
	sub *subscription.Subscription,
	incoming []GrantSource,
) ([]creditGrantGroup, error) {
	addonIDs := lo.Compact(lo.Map(incoming, func(src GrantSource, _ int) string { return src.AddonID }))
	if len(addonIDs) == 0 {
		return nil, nil
	}

	templates, err := NewCreditGrantService(s.ServiceParams).GetCreditGrantsByAddonIDs(ctx, lo.Uniq(addonIDs))
	if err != nil {
		return nil, err
	}

	groups := make([]creditGrantGroup, 0, len(incoming))
	index := make(map[string]int, len(incoming))
	for _, src := range incoming {
		grants := templates[src.AddonID]
		if src.AddonID == "" || len(grants) == 0 {
			continue
		}

		s.Logger.Info(ctx, "addon has credit grants",
			"addon_id", src.AddonID,
			"subscription_id", sub.ID,
			"credit_grants_count", len(grants))

		proration := s.addonCreditGrantProration(ctx, sub, src.StartDate, src.Behavior)
		key := creditGrantGroupKey(src, proration)
		if i, ok := index[key]; ok {
			groups[i].requests = append(groups[i].requests, creditGrantRequestsFromAddon(sub, src.AddonID, grants)...)
			continue
		}

		index[key] = len(groups)
		groups = append(groups, creditGrantGroup{
			requests:  creditGrantRequestsFromAddon(sub, src.AddonID, grants),
			startDate: src.StartDate,
			endDate:   src.EndDate,
			proration: proration,
		})
	}

	return groups, nil
}

// creditGrantGroupKey identifies the anchoring materializeCreditGrants pins for a source:
// two sources may share a pass only when all three agree.
func creditGrantGroupKey(src GrantSource, proration *dto.FirstPeriodProration) string {
	end := ""
	if src.EndDate != nil {
		end = src.EndDate.UTC().String()
	}
	prorationKey := ""
	if proration != nil {
		prorationKey = fmt.Sprintf("%v|%v|%v|%s|%s",
			proration.PeriodStart, proration.PeriodEnd, proration.ProrationDate,
			proration.Strategy, proration.Source)
	}

	return fmt.Sprintf("%v|%s|%s", src.StartDate.UTC(), end, prorationKey)
}

func creditGrantRequestsFromAddon(
	sub *subscription.Subscription,
	addonID string,
	templates []*dto.CreditGrantResponse,
) []dto.CreateCreditGrantRequest {
	return lo.Map(templates, func(cg *dto.CreditGrantResponse, _ int) dto.CreateCreditGrantRequest {
		return dto.CreateCreditGrantRequest{
			Name:                   cg.Name,
			Scope:                  types.CreditGrantScopeSubscription,
			Credits:                cg.Credits,
			Cadence:                cg.Cadence,
			ExpirationType:         cg.ExpirationType,
			Priority:               cg.Priority,
			SubscriptionID:         lo.ToPtr(sub.ID),
			AddonID:                lo.ToPtr(addonID), // provenance for targeted removal
			Period:                 cg.Period,
			ExpirationDuration:     cg.ExpirationDuration,
			ExpirationDurationUnit: cg.ExpirationDurationUnit,
			Metadata:               cg.Metadata,
			PeriodCount:            cg.PeriodCount,
			ConversionRate:         cg.ConversionRate,
			TopupConversionRate:    cg.TopupConversionRate,
		}
	})
}

// resolveCreditGrantCancellations collapses the removed sources into one cancellation per
// effective date, since CancelFutureSubscriptionGrants already scopes by a slice of addons.
func resolveCreditGrantCancellations(removed []GrantSource) []creditGrantCancellation {
	cancellations := make([]creditGrantCancellation, 0, len(removed))
	index := make(map[time.Time]int, len(removed))

	for _, src := range removed {
		if src.AddonID == "" {
			continue
		}
		if i, ok := index[src.StartDate]; ok {
			cancellations[i].addonIDs = append(cancellations[i].addonIDs, src.AddonID)
			continue
		}

		index[src.StartDate] = len(cancellations)
		cancellations = append(cancellations, creditGrantCancellation{
			addonIDs:      []string{src.AddonID},
			effectiveDate: src.StartDate,
		})
	}

	return cancellations
}

// addonCreditGrantProration resolves the billing period containing startDate so a
// mid-cycle grant can be scaled to the part of that period it actually covers.
// Returns nil when proration does not apply, in which case the grant keeps its full
// credits and its natural anchoring.
//
// Never returns an error: proration is an enhancement to the attach, so an
// unresolvable period downgrades to today's behaviour instead of rejecting the addon.
func (s *subscriptionGrantService) addonCreditGrantProration(
	ctx context.Context,
	sub *subscription.Subscription,
	startDate time.Time,
	behavior types.ProrationBehavior,
) *dto.FirstPeriodProration {
	if behavior != types.ProrationBehaviorCreateProrations {
		return nil
	}

	p, err := types.FindPeriodForDate(&types.FindPeriodForDateParams{
		Target:           startDate,
		KnownPeriodStart: sub.CurrentPeriodStart,
		KnownPeriodEnd:   sub.CurrentPeriodEnd,
		Anchor:           sub.BillingAnchor,
		PeriodCount:      sub.BillingPeriodCount,
		BillingPeriod:    sub.BillingPeriod,
		Timezone:         sub.Timezone,
	})
	if err != nil {
		// FindPeriodForDate only walks forward, so a start date in an already-closed
		// period cannot be resolved. Grant in full rather than blocking the attach.
		s.Logger.Info(ctx, "skipping credit grant proration; could not resolve billing period for addon start",
			"subscription_id", sub.ID,
			"start_date", startDate,
			"current_period_start", sub.CurrentPeriodStart,
			"error", err.Error())
		return nil
	}

	if !startDate.After(p.Start) {
		return nil
	}

	// A grant anchored past the subscription end fails CreateCreditGrant validation,
	// and the grant would be capped to that end anyway.
	if sub.EndDate != nil && p.End.After(lo.FromPtr(sub.EndDate)) {
		return nil
	}

	return &dto.FirstPeriodProration{
		PeriodStart:   p.Start,
		PeriodEnd:     p.End,
		ProrationDate: startDate,
		Strategy:      types.StrategySecondBased,
		Source:        grantProrationSourceAddonAttach.String(),
	}
}
