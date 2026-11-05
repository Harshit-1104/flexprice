package dto

import (
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/domain/customer"
	"github.com/flexprice/flexprice/internal/domain/invoice"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvoiceResponse_WithDisplayDates(t *testing.T) {
	at := func(s string) *time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return &v
	}
	newResp := func() *InvoiceResponse {
		return &InvoiceResponse{
			Invoice: invoice.Invoice{
				FinalizedAt: at("2026-09-30T19:00:00Z"), // 1 Oct 00:30 IST
				DueDate:     at("2026-10-15T18:30:00Z"), // 16 Oct 00:00 IST
				PeriodStart: at("2026-08-31T18:30:00Z"), // 1 Sep 00:00 IST
				PeriodEnd:   at("2026-09-30T18:30:00Z"), // 1 Oct 00:00 IST, exclusive
			},
			LineItems: []*InvoiceLineItemResponse{{InvoiceLineItem: invoice.InvoiceLineItem{
				PeriodStart: at("2026-08-31T18:30:00Z"),
				PeriodEnd:   at("2026-09-30T18:30:00Z"),
			}}},
			Customer: &CustomerResponse{Customer: &customer.Customer{Timezone: "America/Los_Angeles"}},
		}
	}

	t.Run("uses the subscription timezone", func(t *testing.T) {
		r := newResp()
		r.Subscription = &SubscriptionResponse{Subscription: &subscription.Subscription{Timezone: "Asia/Kolkata"}}
		r.WithDisplayDates()

		assert.Equal(t, &InvoiceDisplayDates{
			Timezone:    "Asia/Kolkata",
			IssueDate:   "2026-10-01",
			DueDate:     "2026-10-16",
			PeriodStart: "2026-09-01",
			PeriodEnd:   "2026-09-30",
		}, r.DisplayDates)
		assert.Equal(t, &InvoiceDisplayDates{
			Timezone:    "Asia/Kolkata",
			PeriodStart: "2026-09-01",
			PeriodEnd:   "2026-09-30",
		}, r.LineItems[0].DisplayDates)
	})

	t.Run("falls back to the customer timezone without a subscription", func(t *testing.T) {
		r := newResp()
		r.WithDisplayDates()
		assert.Equal(t, "America/Los_Angeles", r.DisplayDates.Timezone)
		assert.Equal(t, "2026-09-30", r.DisplayDates.IssueDate)
	})

	t.Run("prefers issue_date over finalized_at and omits missing dates", func(t *testing.T) {
		r := newResp()
		r.Customer = nil
		r.IssueDate = at("2026-10-02T00:00:00Z")
		r.DueDate = nil
		r.WithDisplayDates()
		assert.Equal(t, "UTC", r.DisplayDates.Timezone)
		assert.Equal(t, "2026-10-02", r.DisplayDates.IssueDate)
		assert.Empty(t, r.DisplayDates.DueDate)
		assert.Equal(t, "2026-09-30", lo.FromPtr(r.DisplayDates).PeriodEnd)
	})
}
