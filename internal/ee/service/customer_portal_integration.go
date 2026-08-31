package service

import (
	"context"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
)

func (s *customerPortalService) GetIntegrations(ctx context.Context) (*dto.IntegrationsResponse, error) {
	return nil, ierr.NewError("integration discovery is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}
