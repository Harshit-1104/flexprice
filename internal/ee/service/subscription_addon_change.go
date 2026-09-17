package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/addonassociation"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
)

type AddonChangeService interface {
	Execute(ctx context.Context, req AddonChangeRequest) (*addonChangeConfig, *SettleProrationResult, error)

	Preview(ctx context.Context, req AddonChangeRequest) (*addonChangeConfig, *SettleProrationResult, error)

	Resolve(ctx context.Context, req AddonChangeRequest) (*addonChangeConfig, error)

	// Apply persists a resolved config in its own transaction and settles it.
	Apply(ctx context.Context, config *addonChangeConfig) (*SettleProrationResult, error)

	Persist(ctx context.Context, config *addonChangeConfig) error

	Settle(ctx context.Context, config *addonChangeConfig, mode SettleMode) (*SettleProrationResult, error)
}

// AddonAdd is one attach in a batch. Existing is set only on a checkout-completion replay,
// where the association already exists as pending and is activated rather than created.
type AddonAdd struct {
	Request  *dto.AddAddonToSubscriptionRequest
	Existing *addonassociation.AddonAssociation
}

type AddonChangeRequest struct {
	Subscription *subscription.Subscription
	Adds         []AddonAdd
	Removes      []*dto.RemoveAddonRequest
}

func (r AddonChangeRequest) Validate() error {
	if r.Subscription == nil {
		return ierr.NewError("subscription is required for an addon change").
			Mark(ierr.ErrValidation)
	}

	if len(r.Adds) == 0 && len(r.Removes) == 0 {
		return ierr.NewError("addon change requires at least one add or remove").
			WithHint("Provide at least one addon to add or remove").
			Mark(ierr.ErrValidation)
	}

	for _, add := range r.Adds {
		if add.Request == nil {
			return ierr.NewError("addon add requires a request").
				Mark(ierr.ErrValidation)
		}
	}

	return nil
}

type addonChangeConfig struct {
	sub      *subscription.Subscription
	attaches []*addonAttachParams
	detaches []*addonDetachParams

	grants *GrantChangeConfig

	quote *LineItemProrationSummary

	periodStart time.Time
	idemKey     string
	reason      string
}

func (c *addonChangeConfig) getSubscription() *subscription.Subscription {
	if c == nil {
		return nil
	}
	return c.sub
}

func (c *addonChangeConfig) getAttaches() []*addonAttachParams {
	if c == nil {
		return nil
	}
	return c.attaches
}

func (c *addonChangeConfig) getDetaches() []*addonDetachParams {
	if c == nil {
		return nil
	}
	return c.detaches
}

func (c *addonChangeConfig) getGrants() *GrantChangeConfig {
	if c == nil {
		return nil
	}
	return c.grants
}

func (c *addonChangeConfig) getQuote() *LineItemProrationSummary {
	if c == nil {
		return nil
	}
	return c.quote
}

func (c *addonChangeConfig) getPeriodStart() time.Time {
	if c == nil {
		return time.Time{}
	}
	return c.periodStart
}

func (c *addonChangeConfig) getIdempotencyKey() string {
	if c == nil {
		return ""
	}
	return c.idemKey
}

func (c *addonChangeConfig) getReason() string {
	if c == nil {
		return ""
	}
	return c.reason
}

func (c *addonChangeConfig) getAssociations() []*addonassociation.AddonAssociation {
	associations := make([]*addonassociation.AddonAssociation, 0, len(c.getAttaches())+len(c.getDetaches()))
	for _, attach := range c.getAttaches() {
		associations = append(associations, attach.getAssociation())
	}
	for _, detach := range c.getDetaches() {
		associations = append(associations, detach.getAssociation())
	}

	return associations
}

func (c *addonChangeConfig) getCreatedLineItems() []*subscription.SubscriptionLineItem {
	lineItems := []*subscription.SubscriptionLineItem{}
	for _, attach := range c.getAttaches() {
		lineItems = append(lineItems, attach.getLineItems()...)
	}

	return lineItems
}

func (c *addonChangeConfig) getEndedLineItems() []*subscription.SubscriptionLineItem {
	lineItems := []*subscription.SubscriptionLineItem{}
	for _, detach := range c.getDetaches() {
		lineItems = append(lineItems, detach.getLineItems()...)
	}
	return lineItems
}

type addonChangeService struct {
	ServiceParams
	sub *subscriptionService
}

func NewAddonChangeService(params ServiceParams) AddonChangeService {
	return &addonChangeService{
		ServiceParams: params,
		sub:           &subscriptionService{ServiceParams: params},
	}
}

func (s *addonChangeService) Resolve(ctx context.Context, req AddonChangeRequest) (*addonChangeConfig, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	sub := req.Subscription
	config := &addonChangeConfig{sub: sub}

	for _, remove := range req.Removes {
		params, err := s.sub.createAddonDetachParams(ctx, sub, remove)
		if err != nil {
			return nil, err
		}

		config.detaches = append(config.detaches, params)
		if config.reason == "" {
			config.reason = params.getReason()
		}
	}

	// Each attach validates its commitments against the line items resolved before it, so a
	// batch is checked as the shape it will leave behind rather than one addon at a time.
	originalLineItems := sub.LineItems
	for _, add := range req.Adds {
		addReq := *add.Request
		addReq.SkipEntityValidation = true

		params, err := s.sub.createAddonAttachParams(ctx, sub, &addReq, add.Existing)
		if err != nil {
			sub.LineItems = originalLineItems
			return nil, err
		}

		config.attaches = append(config.attaches, params)
		sub.LineItems = lo.Flatten([][]*subscription.SubscriptionLineItem{sub.LineItems, params.getLineItems()})
	}
	sub.LineItems = originalLineItems

	grants, err := newSubscriptionGrantService(s.ServiceParams).Resolve(ctx, s.grantChangeRequest(config))
	if err != nil {
		return nil, err
	}
	config.grants = grants

	if err := s.quote(ctx, config); err != nil {
		return nil, err
	}

	return config, nil
}

func (s *addonChangeService) grantChangeRequest(config *addonChangeConfig) GrantChangeRequest {
	sub := config.getSubscription()
	req := GrantChangeRequest{Sub: sub}

	for _, detach := range config.getDetaches() {
		req.Removed = append(req.Removed, GrantSource{
			ChangeType:    grantChangeTypeFor(sub, detach.getEffectiveDate()),
			EffectiveDate: detach.getEffectiveDate(),
			Origin:        grantProrationSourceAddonDetach,
			AddonID:       detach.getAssociation().AddonID,
		})
	}

	for _, attach := range config.getAttaches() {
		req.Incoming = append(req.Incoming, GrantSource{
			ChangeType:    grantChangeTypeFor(sub, attach.getEffectiveDate()),
			EffectiveDate: attach.getEffectiveDate(),
			EndDate:       attach.getAssociation().EndDate,
			Behavior:      attach.getRequest().ProrationBehavior,
			Origin:        grantProrationSourceAddonAttach,
			AddonID:       attach.getRequest().AddonID,
		})
	}

	return req
}

// prorationGroup is the batch's entries sharing one effective date: proration is priced against
// the remaining period, so entries landing on different dates cannot be computed together.
type prorationGroup struct {
	effectiveDate time.Time
	entries       []LineItemProrationEntry
}

func (s *addonChangeService) quote(ctx context.Context, config *addonChangeConfig) error {
	groups, err := s.prorationGroups(ctx, config)
	if err != nil {
		return err
	}

	config.quote = emptyProrationSummary(config.getSubscription())
	if len(groups) == 0 {
		config.periodStart = config.getSubscription().CurrentPeriodEnd
		return nil
	}

	config.periodStart = groups[0].effectiveDate

	prorationSvc := NewLineItemProrationService(s.ServiceParams)
	for _, group := range groups {
		summary, err := prorationSvc.Compute(ctx, LineItemProrationRequest{
			Subscription:  config.getSubscription(),
			Entries:       group.entries,
			EffectiveDate: group.effectiveDate,
			Behavior:      types.ProrationBehaviorCreateProrations,
			Reason:        config.getReason(),
		})
		if err != nil {
			return err
		}

		config.quote.Merge(summary)
	}

	config.idemKey = s.idempotencyKey(config)
	return nil
}

func (s *addonChangeService) prorationGroups(ctx context.Context, config *addonChangeConfig) ([]prorationGroup, error) {
	byDate := map[int64]*prorationGroup{}

	add := func(effectiveDate time.Time, entries []LineItemProrationEntry) {
		key := effectiveDate.UTC().UnixNano()
		if byDate[key] == nil {
			byDate[key] = &prorationGroup{effectiveDate: effectiveDate}
		}
		byDate[key].entries = append(byDate[key].entries, entries...)
	}

	for _, detach := range config.getDetaches() {
		if detach.getBehavior() != types.ProrationBehaviorCreateProrations {
			continue
		}

		entries, err := s.sub.buildAddonProrationEntries(ctx, detach.getLineItems(), types.ProrationActionRemoveItem)
		if err != nil {
			return nil, err
		}
		add(detach.getEffectiveDate(), entries)
	}

	for _, attach := range config.getAttaches() {
		if attach.getRequest().ProrationBehavior != types.ProrationBehaviorCreateProrations {
			continue
		}

		entries, err := s.sub.buildAddonProrationEntries(ctx, attach.getLineItems(), types.ProrationActionAddItem)
		if err != nil {
			return nil, err
		}
		add(attach.getEffectiveDate(), entries)
	}

	groups := lo.Values(byDate)
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].effectiveDate.Before(groups[j].effectiveDate)
	})

	return lo.Map(groups, func(g *prorationGroup, _ int) prorationGroup { return *g }), nil
}

// idempotencyKey reproduces the per-path keys the single-addon flows stamp today: a removal-only
// change credits the wallet under its raw key, everything else hashes into a charge key.
func (s *addonChangeService) idempotencyKey(config *addonChangeConfig) string {
	if len(config.getAttaches()) == 0 && len(config.getDetaches()) == 1 {
		return config.getDetaches()[0].prorationIdempotencyKey()
	}

	sources := make([]string, 0, len(config.getAttaches())+len(config.getDetaches()))
	for _, attach := range config.getAttaches() {
		sources = append(sources, attach.prorationIdempotencyKey())
	}
	for _, detach := range config.getDetaches() {
		sources = append(sources, detach.prorationIdempotencyKey())
	}
	sort.Strings(sources)

	return prorationChargeInvoiceKey(LineItemProrationRequest{
		Subscription:   config.getSubscription(),
		EffectiveDate:  config.getPeriodStart(),
		IdempotencyKey: strings.Join(sources, ","),
	})
}

// Persist writes the resolved config and raises no money. It opens no transaction of its own:
// the caller's must already be open, so the whole batch commits or rolls back together.
func (s *addonChangeService) Persist(ctx context.Context, config *addonChangeConfig) error {
	if config == nil {
		return ierr.NewError("addon change config is required").
			Mark(ierr.ErrValidation)
	}

	// Removals run first so a swap frees its entitlement slot before the additions claim it.
	if err := s.persistRemovals(ctx, config); err != nil {
		return err
	}

	if err := s.persistAttaches(ctx, config); err != nil {
		return err
	}

	return newSubscriptionGrantService(s.ServiceParams).Apply(ctx, config.getGrants())
}

func (s *addonChangeService) persistRemovals(ctx context.Context, config *addonChangeConfig) error {
	// Each removal carries its own date and reason, so cancellation batches by both.
	type cancellation struct {
		effectiveDate time.Time
		reason        string
	}

	idsByCancellation := map[cancellation][]string{}
	for _, detach := range config.getDetaches() {
		key := cancellation{effectiveDate: detach.getEffectiveDate(), reason: detach.getReason()}
		idsByCancellation[key] = append(idsByCancellation[key], detach.getAssociation().ID)
	}

	for key, ids := range idsByCancellation {
		if err := s.AddonAssociationRepo.CancelBulk(ctx, ids, key.effectiveDate, key.reason); err != nil {
			return err
		}
	}

	for _, detach := range config.getDetaches() {
		detach.association = addonassociation.NewAddonAssociationBuilder(detach.getAssociation()).
			WithCancellation(detach.getEffectiveDate(), detach.getReason()).
			Build()

		deleteReq := dto.DeleteSubscriptionLineItemRequest{EffectiveFrom: lo.ToPtr(detach.getEffectiveDate())}
		for _, lineItem := range detach.getLineItems() {
			if _, err := s.sub.deleteSubscriptionLineItem(ctx, lineItem.ID, deleteReq); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *addonChangeService) persistAttaches(ctx context.Context, config *addonChangeConfig) error {
	sub := config.getSubscription()

	toCreate := []*addonassociation.AddonAssociation{}
	toActivate := []string{}

	for _, attach := range config.getAttaches() {
		req := attach.getRequest()

		// Overrides mint subscription-scoped prices and repoint the line items at them, so they
		// must land before the line items are written.
		if len(req.OverrideLineItems) > 0 {
			if err := s.sub.ProcessSubscriptionPriceOverrides(
				ctx, sub, req.OverrideLineItems, attach.getLineItems(), attach.getPriceMap(),
			); err != nil {
				return err
			}
		}

		association := attach.getAssociation()
		if attach.isReplayAttach() {
			association.AddonStatus = types.AddonStatusActive
			toActivate = append(toActivate, association.ID)
			continue
		}

		toCreate = append(toCreate, association)
	}

	if err := s.AddonAssociationRepo.CreateBulk(ctx, toCreate); err != nil {
		return err
	}

	if err := s.AddonAssociationRepo.ActivateBulk(ctx, toActivate); err != nil {
		return err
	}

	for _, attach := range config.getAttaches() {
		if err := s.sub.createBucketPricesForLineItems(ctx, sub, attach.getLineItems(), attach.getBucketCfgs()); err != nil {
			return err
		}

		for _, lineItem := range attach.getLineItems() {
			if err := s.SubscriptionLineItemRepo.Create(ctx, lineItem); err != nil {
				return err
			}
		}
	}

	return nil
}

// Settle raises the one netted document the batch owes: an invoice when the net is a charge,
// a wallet credit when it is a refund, nothing when it is zero.
func (s *addonChangeService) Settle(
	ctx context.Context,
	config *addonChangeConfig,
	mode SettleMode,
) (*SettleProrationResult, error) {
	if config == nil {
		return nil, ierr.NewError("addon change config is required").
			Mark(ierr.ErrValidation)
	}

	sub := config.getSubscription()

	// A preview writes nothing, so it carries no key to deduplicate against.
	idempotencyKey := config.getIdempotencyKey()
	if mode == SettleModePreview {
		idempotencyKey = ""
	}

	req := NewSettleProrationRequest(
		sub, config.getQuote(), config.getPeriodStart(), sub.CurrentPeriodEnd,
		"Subscription update", idempotencyKey, mode,
	)
	req.Reason = config.getReason()
	req.AttemptPayment = true

	return NewLineItemProrationService(s.ServiceParams).Settle(ctx, req)
}

func (s *addonChangeService) Execute(
	ctx context.Context,
	req AddonChangeRequest,
) (*addonChangeConfig, *SettleProrationResult, error) {
	config, err := s.Resolve(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	settled, err := s.Apply(ctx, config)
	if err != nil {
		return nil, nil, err
	}

	return config, settled, nil
}

func (s *addonChangeService) Apply(ctx context.Context, config *addonChangeConfig) (*SettleProrationResult, error) {
	if err := s.DB.WithTx(ctx, func(ctx context.Context) error {
		return s.Persist(ctx, config)
	}); err != nil {
		return nil, err
	}

	return s.settleExecuted(ctx, config), nil
}

func (s *addonChangeService) Preview(
	ctx context.Context,
	req AddonChangeRequest,
) (*addonChangeConfig, *SettleProrationResult, error) {
	config, err := s.Resolve(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	settled, err := s.Settle(ctx, config, SettleModePreview)
	if err != nil {
		return nil, nil, err
	}

	return config, settled, nil
}

// settleExecuted settles a batch that is already committed, so a failure here cannot undo it —
// it is logged and the change stands unbilled. D2 moves settlement inside the transaction.
func (s *addonChangeService) settleExecuted(ctx context.Context, config *addonChangeConfig) *SettleProrationResult {
	settled, err := s.Settle(ctx, config, SettleModeIssue)
	if err != nil {
		s.Logger.Error(ctx, "failed to settle addon change; the change was persisted and is UNBILLED for this period",
			"error", err,
			"subscription_id", config.getSubscription().ID,
			"attached", len(config.getAttaches()),
			"detached", len(config.getDetaches()),
			"period_start", config.getPeriodStart(),
			"idempotency_key", config.getIdempotencyKey(),
		)
		return nil
	}

	return settled
}
