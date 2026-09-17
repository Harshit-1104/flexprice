package service

import (
	"context"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/types"
)

func (s *subscriptionModificationService) executeAddonsModification(
	ctx context.Context,
	subscriptionID string,
	params *dto.SubModifyAddonsParams,
) (*dto.SubscriptionModifyResponse, error) {
	sub, err := s.loadSubscriptionWithLineItems(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}

	config, settled, err := NewAddonChangeService(s.serviceParams).
		Execute(ctx, NewAddonChangeRequest(sub, params))
	if err != nil {
		return nil, err
	}

	s.publishSystemEvent(ctx, types.WebhookEventSubscriptionUpdated, subscriptionID)
	triggerHubSpotDealSync(ctx, s.serviceParams, subscriptionID)

	return s.addonModifyResponse(ctx, subscriptionID,
		addonBatchChangedLineItems(config, false), settled.GetChanged(), nil)
}

func (s *subscriptionModificationService) previewAddonsModification(
	ctx context.Context,
	subscriptionID string,
	params *dto.SubModifyAddonsParams,
) (*dto.SubscriptionModifyResponse, error) {
	sub, err := s.loadSubscriptionWithLineItems(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}

	config, settled, err := NewAddonChangeService(s.serviceParams).
		Preview(ctx, NewAddonChangeRequest(sub, params))
	if err != nil {
		return nil, err
	}

	return s.addonModifyResponse(ctx, subscriptionID,
		addonBatchChangedLineItems(config, true), settled.GetChanged(), nil)
}

// Each ended item carries its own detach date: entries in a batch do not share one.
func addonBatchChangedLineItems(config *addonChangeConfig, isPreview bool) []dto.ChangedLineItem {
	items := []dto.ChangedLineItem{}

	for _, attach := range config.getAttaches() {
		for _, li := range attach.getLineItems() {
			items = append(items, changedCreatedLineItem(li, isPreview))
		}
	}

	for _, detach := range config.getDetaches() {
		for _, li := range detach.getLineItems() {
			items = append(items, changedEndedLineItem(li, detach.getEffectiveDate()))
		}
	}

	if len(items) == 0 {
		return nil
	}
	return items
}
