package service

import (
	"context"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
)

func (s *customerPortalService) ListPaymentMethods(ctx context.Context, req *dto.ListSavedPaymentMethodsRequest) (*dto.SavedPaymentMethodsResponse, error) {
	customerID, err := s.portalCustomerID(ctx)
	if err != nil {
		return nil, err
	}

	var requested []types.PaymentGatewayType
	if req != nil {
		requested = req.Providers
	}
	gateways, err := s.methodManagementProviders(ctx, customerID, requested)
	if err != nil {
		return nil, err
	}

	resp := &dto.SavedPaymentMethodsResponse{
		Providers: make([]*dto.ProviderSavedPaymentMethods, 0, len(gateways)),
	}
	for _, gw := range gateways {
		resp.Providers = append(resp.Providers, s.readSavedMethods(ctx, customerID, gw))
	}
	
	return resp, nil
}

func (s *customerPortalService) AddPaymentMethod(ctx context.Context, req *dto.PortalAddPaymentMethodRequest) (*dto.AddPaymentMethodResponse, error) {
	return nil, ierr.NewError("adding a payment method is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}

func (s *customerPortalService) DeletePaymentMethod(ctx context.Context, paymentMethodID string) (*dto.SavedPaymentMethodsResponse, error) {
	return nil, ierr.NewError("deleting a payment method is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}

func (s *customerPortalService) SetDefaultPaymentMethod(ctx context.Context, paymentMethodID string) (*dto.SavedPaymentMethodsResponse, error) {
	return nil, ierr.NewError("changing the default payment method is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}

// methodManagementProviders returns the gateways to fan out over. A named provider
// is validated through the resolver so "not connected" and "cannot manage methods"
// stay distinguishable; naming none means every capable provider.
func (s *customerPortalService) methodManagementProviders(
	ctx context.Context,
	customerID string,
	requested []types.PaymentGatewayType,
) ([]types.PaymentGatewayType, error) {
	resolver := NewPaymentProviderResolver(s.ServiceParams)

	if len(requested) > 0 {
		out := make([]types.PaymentGatewayType, 0, len(requested))
		for _, gw := range lo.Uniq(requested) {
			resolved, err := resolver.ResolveProvider(ctx, customerID, types.IntegrationCapabilityPaymentMethodManagement, gw)
			if err != nil {
				return nil, err
			}
			out = append(out, resolved)
		}
		return out, nil
	}

	providers, err := resolver.ListProviders(ctx, customerID)
	if err != nil {
		return nil, err
	}
	capable := lo.Filter(providers, func(p interfaces.ProviderCapabilities, _ int) bool {
		return lo.ContainsBy(p.Capabilities, func(c types.IntegrationCapability) bool {
			return c.Type == types.IntegrationCapabilityPaymentMethodManagement
		})
	})
	return lo.Map(capable, func(p interfaces.ProviderCapabilities, _ int) types.PaymentGatewayType {
		return p.Gateway
	}), nil
}

// readSavedMethods never returns an error: one unreachable gateway must not blank
// out the others, and an empty Items with no Error means "nothing saved" — a
// distinction the UI needs and a bare empty list cannot make.
func (s *customerPortalService) readSavedMethods(
	ctx context.Context,
	customerID string,
	gw types.PaymentGatewayType,
) *dto.ProviderSavedPaymentMethods {
	group := &dto.ProviderSavedPaymentMethods{
		Provider: gw,
		Items:    []*dto.SavedPaymentMethod{},
	}

	provider, err := s.IntegrationFactory.GetSavedMethodProvider(ctx, gw, s.customerService)
	if err != nil {
		s.Logger.Error(ctx, "saved method provider unavailable",
			"error", err, "customer_id", customerID, "provider", gw)
		group.Error = &dto.ProviderError{Message: "This payment provider is currently unavailable"}
		return group
	}

	methods, err := provider.ListSavedMethods(ctx, customerID)
	if err != nil {
		s.Logger.Error(ctx, "failed to list saved payment methods",
			"error", err, "customer_id", customerID, "provider", gw)
		group.Error = &dto.ProviderError{Message: "Could not read saved payment methods from this provider"}
		return group
	}

	autoCharge := lo.Contains(gatewayCapabilities[gw], types.IntegrationCapabilityAutoCharge)
	for _, m := range methods {
		group.Items = append(group.Items, toSavedPaymentMethod(m, gw, autoCharge))
	}
	return group
}

func toSavedPaymentMethod(
	m interfaces.ProviderPaymentMethod,
	gw types.PaymentGatewayType,
	providerAutoCharges bool,
) *dto.SavedPaymentMethod {
	status := types.PaymentMethodStatusInactive
	if m.Active {
		status = types.PaymentMethodStatusActive
	}

	// GatewayAccountID is deliberately not mapped — it is a routing detail.
	out := &dto.SavedPaymentMethod{
		ID:            m.GatewayMethodID,
		Provider:      gw,
		Type:          m.Method,
		Status:        status,
		IsDefault:     m.IsDefault,
		CanAutoCharge: m.Active && providerAutoCharges,
	}
	if m.Card != nil {
		out.Card = &dto.SavedCardDetails{
			Brand:    m.Card.Brand,
			Last4:    m.Card.Last4,
			ExpMonth: m.Card.ExpMonth,
			ExpYear:  m.Card.ExpYear,
		}
	}
	return out
}
