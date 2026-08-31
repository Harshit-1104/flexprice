package service

import (
	"context"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
)

func (s *customerPortalService) ListPaymentMethods(ctx context.Context, req *dto.ListSavedPaymentMethodsRequest) (*dto.SavedPaymentMethodsResponse, error) {
	return nil, ierr.NewError("saved payment methods are not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
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
