package chargebee

import (
	"context"

	"github.com/chargebee/chargebee-go/v3/actions/customer"
	"github.com/chargebee/chargebee-go/v3/actions/paymentsource"
	"github.com/chargebee/chargebee-go/v3/actions/transaction"
	"github.com/chargebee/chargebee-go/v3/enum"
	"github.com/chargebee/chargebee-go/v3/filter"
	customerModel "github.com/chargebee/chargebee-go/v3/models/customer"
	paymentSourceModel "github.com/chargebee/chargebee-go/v3/models/paymentsource"
	transactionModel "github.com/chargebee/chargebee-go/v3/models/transaction"
	transactionEnum "github.com/chargebee/chargebee-go/v3/models/transaction/enum"
	"github.com/samber/lo"
)

// CollectPaymentParams drives customers/{id}/collect_payment.
type CollectPaymentParams struct {
	ChargebeeCustomerID string
	ChargebeeInvoiceID  string
	AmountMinor         int64
	// PaymentSourceID charges an already-vaulted card (saved-card path).
	PaymentSourceID string
	// CustomerPresent marks the transaction customer-initiated (CIT). Omitting it
	// defaults to merchant-initiated. CIT is correct when the customer is at the
	// keyboard, and is the mode in which an issuer may challenge with 3DS rather
	// than decline outright.
	CustomerPresent bool
	IdempotencyKey  string
}

// CollectPayment pays a specific Chargebee invoice. invoice_allocations pins the
// payment to OUR invoice — this is what makes the charged amount equal the amount
// our UI quoted. (collect_now, by contrast, is customer+currency scoped and would
// sweep every open invoice.)
func (c *Client) CollectPayment(ctx context.Context, p CollectPaymentParams) (*transactionModel.Transaction, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	params := &customerModel.CollectPaymentRequestParams{
		InvoiceAllocations: []*customerModel.CollectPaymentInvoiceAllocationParams{{
			InvoiceId:        p.ChargebeeInvoiceID,
			AllocationAmount: lo.ToPtr(p.AmountMinor),
		}},
		PaymentSourceId: p.PaymentSourceID,
	}
	if p.CustomerPresent {
		params.PaymentInitiator = enum.PaymentInitiatorCustomer
	}

	req := customer.CollectPayment(p.ChargebeeCustomerID, params)
	if p.IdempotencyKey != "" {
		req = req.SetIdempotencyKey(p.IdempotencyKey)
	}

	res, err := req.RequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to collect payment at Chargebee")
	}
	if res.Transaction == nil {
		return nil, missingPayload("transaction")
	}
	return res.Transaction, nil
}

// ListPaymentSources returns the customer's vaulted payment sources.
func (c *Client) ListPaymentSources(ctx context.Context, chargebeeCustomerID string) ([]*paymentSourceModel.PaymentSource, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	res, err := paymentsource.List(&paymentSourceModel.ListRequestParams{
		CustomerId: &filter.StringFilter{Is: chargebeeCustomerID},
		// Chargebee pages at 10 by default; ask for more so the customer's primary
		// source cannot fall off the first page and get silently skipped.
		Limit: lo.ToPtr(int32(50)),
	}).ListRequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to list Chargebee payment sources")
	}

	out := make([]*paymentSourceModel.PaymentSource, 0, len(res.List))
	for _, entry := range res.List {
		if entry.PaymentSource != nil {
			out = append(out, entry.PaymentSource)
		}
	}
	return out, nil
}

// RetrieveCustomer fetches the Chargebee customer, chiefly for PrimaryPaymentSourceId.
func (c *Client) RetrieveCustomer(ctx context.Context, chargebeeCustomerID string) (*customerModel.Customer, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	res, err := customer.Retrieve(chargebeeCustomerID).RequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to retrieve Chargebee customer")
	}
	if res.Customer == nil {
		return nil, missingPayload("customer")
	}
	return res.Customer, nil
}

// RetrieveTransaction fetches a transaction, chiefly to see what has already been
// refunded against it.
func (c *Client) RetrieveTransaction(ctx context.Context, transactionID string) (*transactionModel.Transaction, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	res, err := transaction.Retrieve(transactionID).RequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to retrieve Chargebee transaction")
	}
	if res.Transaction == nil {
		return nil, missingPayload("transaction")
	}
	return res.Transaction, nil
}

// RefundTransaction refunds amountMinor against a settled transaction. Chargebee
// scopes the idempotency key globally, so callers must namespace it per operation.
func (c *Client) RefundTransaction(ctx context.Context, transactionID string, amountMinor int64, idempotencyKey string) (*transactionModel.Transaction, error) {
	env, err := c.env(ctx)
	if err != nil {
		return nil, err
	}

	req := transaction.Refund(transactionID, &transactionModel.RefundRequestParams{
		Amount: lo.ToPtr(amountMinor),
	})
	if idempotencyKey != "" {
		req = req.SetIdempotencyKey(idempotencyKey)
	}

	res, err := req.RequestWithEnv(env)
	if err != nil {
		return nil, wrapAPIError(err, "Failed to refund Chargebee transaction")
	}
	if res.Transaction == nil {
		return nil, missingPayload("transaction")
	}
	return res.Transaction, nil
}

// AmountRefunded totals the refunds already linked to a transaction. The API has
// no amount_unrefunded field on this model, so the guard against submitting a
// second refund is computed from linked_refunds.
func AmountRefunded(txn *transactionModel.Transaction) int64 {
	if txn == nil {
		return 0
	}
	var total int64
	for _, refund := range txn.LinkedRefunds {
		if refund == nil || refund.TxnStatus == transactionEnum.StatusFailure {
			continue
		}
		total += refund.TxnAmount
	}
	return total
}
