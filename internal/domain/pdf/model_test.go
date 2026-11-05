package pdf

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func marshalDate(t *testing.T, ct CustomTime) string {
	t.Helper()
	b, err := json.Marshal(ct)
	require.NoError(t, err)
	var s string
	require.NoError(t, json.Unmarshal(b, &s))
	return s
}

func TestPeriodDatesFollowBillingTimezone(t *testing.T) {
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
			loc := mustLoad(t, tc.tz)
			start, err := time.Parse(time.RFC3339, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339, tc.end)
			require.NoError(t, err)

			assert.Equal(t, tc.wantStart, marshalDate(t, NewDate(start, loc)))
			assert.Equal(t, tc.wantEnd, marshalDate(t, NewPeriodEnd(end, loc)))
		})
	}
}

func TestNewDateUsesBillingTimezoneDay(t *testing.T) {
	// 31 Oct 20:00 UTC is already 1 Nov in India.
	issued := time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	assert.Equal(t, "2026-11-01", marshalDate(t, NewDate(issued, mustLoad(t, "Asia/Kolkata"))))
	assert.Equal(t, "2026-10-31", marshalDate(t, NewDate(issued, time.UTC)))
}

func TestZeroDatesStayEmpty(t *testing.T) {
	assert.Equal(t, "", marshalDate(t, NewDate(time.Time{}, time.UTC)))
	assert.Equal(t, "", marshalDate(t, NewPeriodEnd(time.Time{}, time.UTC)))
}
