package service

import (
	"context"

	"github.com/flexprice/flexprice/internal/api/dto"
	ierr "github.com/flexprice/flexprice/internal/errors"
)

func (s *customerPortalService) PayInvoice(ctx context.Context, invoiceID string, req *dto.PortalPayInvoiceRequest) (*dto.PortalPayInvoiceResponse, error) {
	return nil, ierr.NewError("paying an invoice from the portal is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}
