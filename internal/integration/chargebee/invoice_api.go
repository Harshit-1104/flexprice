package chargebee

import (
	"context"
	"strings"

	"github.com/chargebee/chargebee-go/v3/actions/invoice"
	"github.com/chargebee/chargebee-go/v3/enum"
	invoiceModel "github.com/chargebee/chargebee-go/v3/models/invoice"
	"github.com/samber/lo"
)

// CreateAdHocInvoice creates a Chargebee invoice carrying a single ad-hoc charge.
// This is the "mirror" of the Flexprice draft invoice. Ad-hoc is required because
// a wallet top-up has no Price entity, so there is no item_price to reference —
// the existing catalog-based sync path rejects such line items outright.
//
// poNumber is the correlation key the hosted page also carries, so a webhook
// resolves the same way whichever path created the invoice.
func (c *Client) CreateAdHocInvoice(
	ctx context.Context,
	chargebeeCustomerID, currency string,
	amountMinor int64,
	description, poNumber, idempotencyKey string,
) (*invoiceModel.Invoice, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	req := invoice.CreateForChargeItemsAndCharges(&invoiceModel.CreateForChargeItemsAndChargesRequestParams{
		CustomerId:   chargebeeCustomerID,
		CurrencyCode: strings.ToUpper(currency),
		Charges: []*invoiceModel.CreateForChargeItemsAndChargesChargeParams{{
			Amount:      lo.ToPtr(amountMinor),
			Description: description,
		}},
		PoNumber: poNumber,
		// This invoice exists only as the allocation target for the collect_payment
		// call the caller makes next, so Chargebee must not also try to collect it:
		// two charges would race for the same amount. Auto-charge is not lost — it
		// is that explicit call, which is also what pins the amount and declares
		// CIT/MIT.
		AutoCollection: enum.AutoCollectionOff,
	})
	if idempotencyKey != "" {
		req = req.SetIdempotencyKey(idempotencyKey)
	}

	res, err := req.RequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to create Chargebee ad-hoc invoice")
	}
	if res.Invoice == nil {
		return nil, missingPayload("invoice")
	}
	return res.Invoice, nil
}
