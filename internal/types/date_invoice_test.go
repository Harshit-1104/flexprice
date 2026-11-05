package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvoicePeriodDaysFollowBillingTimezone(t *testing.T) {
	tests := []struct {
		name      string
		tz        string
		start     string
		end       string
		wantStart string
		wantEnd   string
	}{
		{
			name:  "UTC subscription, September",
			tz:    "UTC",
			start: "2026-09-01T00:00:00Z", end: "2026-10-01T00:00:00Z",
			wantStart: "2026-09-01", wantEnd: "2026-09-30",
		},
		{
			name:  "Asia/Kolkata subscription, September",
			tz:    "Asia/Kolkata",
			start: "2026-08-31T18:30:00Z", end: "2026-09-30T18:30:00Z",
			wantStart: "2026-09-01", wantEnd: "2026-09-30",
		},
		{
			name:  "America/Los_Angeles subscription, October",
			tz:    "America/Los_Angeles",
			start: "2026-10-01T07:00:00Z", end: "2026-11-01T07:00:00Z",
			wantStart: "2026-10-01", wantEnd: "2026-10-31",
		},
		{
			name:  "mid-day anchor stays on its own day",
			tz:    "UTC",
			start: "2026-10-29T10:15:00Z", end: "2026-11-29T10:15:00Z",
			wantStart: "2026-10-29", wantEnd: "2026-11-29",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc := LoadTimezone(tc.tz)
			start, err := time.Parse(time.RFC3339, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339, tc.end)
			require.NoError(t, err)

			assert.Equal(t, tc.wantStart, InvoiceDay(start, loc).Format(time.DateOnly))
			assert.Equal(t, tc.wantEnd, InvoicePeriodEndDay(end, loc).Format(time.DateOnly))
		})
	}
}

func TestInvoiceDayUsesBillingTimezone(t *testing.T) {
	// 31 Oct 20:00 UTC is already 1 Nov in India.
	issued := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	assert.Equal(t, "2026-11-01", InvoiceDay(issued, LoadTimezone("Asia/Kolkata")).Format(time.DateOnly))
	assert.Equal(t, "2026-10-31", InvoiceDay(issued, time.UTC).Format(time.DateOnly))
}

func TestInvoiceDaysKeepZeroTimes(t *testing.T) {
	assert.True(t, InvoiceDay(time.Time{}, time.UTC).IsZero())
	assert.True(t, InvoicePeriodEndDay(time.Time{}, time.UTC).IsZero())
}
