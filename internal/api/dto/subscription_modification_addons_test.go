package dto

import (
	"testing"
	"time"

	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func validAddonsAdd() *AddAddonToSubscriptionRequest {
	return &AddAddonToSubscriptionRequest{
		AddonID:           "addon_123",
		Cadence:           types.AddonCadenceRecurring,
		ProrationBehavior: types.ProrationBehaviorCreateProrations,
	}
}

func validAddonsRemove() *RemoveAddonRequest {
	return &RemoveAddonRequest{
		AddonAssociationID: "addon_assoc_123",
		ProrationBehavior:  types.ProrationBehaviorCreateProrations,
	}
}

func TestSubModifyAddonsParams_Validate(t *testing.T) {
	manyAdds := make([]*AddAddonToSubscriptionRequest, maxAddonBatchEntries+1)
	for i := range manyAdds {
		manyAdds[i] = validAddonsAdd()
	}

	withAdd := func(mutate func(*AddAddonToSubscriptionRequest)) *SubModifyAddonsParams {
		add := validAddonsAdd()
		mutate(add)
		return &SubModifyAddonsParams{Adds: []*AddAddonToSubscriptionRequest{add}}
	}
	withRemove := func(mutate func(*RemoveAddonRequest)) *SubModifyAddonsParams {
		remove := validAddonsRemove()
		mutate(remove)
		return &SubModifyAddonsParams{Removes: []*RemoveAddonRequest{remove}}
	}

	tests := []struct {
		name    string
		params  *SubModifyAddonsParams
		wantErr bool
	}{
		{name: "nil params", params: nil, wantErr: true},
		{name: "no entries", params: &SubModifyAddonsParams{}, wantErr: true},
		{
			name:    "adds only",
			params:  &SubModifyAddonsParams{Adds: []*AddAddonToSubscriptionRequest{validAddonsAdd()}},
			wantErr: false,
		},
		{
			name:    "removes only",
			params:  &SubModifyAddonsParams{Removes: []*RemoveAddonRequest{validAddonsRemove()}},
			wantErr: false,
		},
		{
			name: "mixed swap",
			params: &SubModifyAddonsParams{
				Adds:    []*AddAddonToSubscriptionRequest{validAddonsAdd()},
				Removes: []*RemoveAddonRequest{validAddonsRemove()},
			},
			wantErr: false,
		},
		{
			// Two instances of one addon is a legitimate request.
			name: "same addon added twice",
			params: &SubModifyAddonsParams{
				Adds: []*AddAddonToSubscriptionRequest{validAddonsAdd(), validAddonsAdd()},
			},
			wantErr: false,
		},
		{
			name:    "over the entry cap",
			params:  &SubModifyAddonsParams{Adds: manyAdds},
			wantErr: true,
		},
		{
			name: "duplicate association in removes",
			params: &SubModifyAddonsParams{
				Removes: []*RemoveAddonRequest{validAddonsRemove(), validAddonsRemove()},
			},
			wantErr: true,
		},
		{
			name:    "nil add entry",
			params:  &SubModifyAddonsParams{Adds: []*AddAddonToSubscriptionRequest{nil}},
			wantErr: true,
		},
		{
			name:    "nil remove entry",
			params:  &SubModifyAddonsParams{Removes: []*RemoveAddonRequest{nil}},
			wantErr: true,
		},
		{
			name:    "add missing addon id",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.AddonID = "" }),
			wantErr: true,
		},
		{
			name:    "add invalid proration behavior",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.ProrationBehavior = "always" }),
			wantErr: true,
		},
		{
			name:    "remove missing association id",
			params:  withRemove(func(r *RemoveAddonRequest) { r.AddonAssociationID = "" }),
			wantErr: true,
		},
		{
			name:    "add with change_at immediate",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.ChangeAt = lo.ToPtr(types.ScheduleTypeImmediate) }),
			wantErr: false,
		},
		{
			name:    "remove with change_at end of period",
			params:  withRemove(func(r *RemoveAddonRequest) { r.ChangeAt = lo.ToPtr(types.ScheduleTypePeriodEnd) }),
			wantErr: false,
		},
		{
			name:    "add with unknown change_at",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.ChangeAt = lo.ToPtr(types.ScheduleType("tomorrow")) }),
			wantErr: true,
		},
		{
			name:    "add with empty change_at",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.ChangeAt = lo.ToPtr(types.ScheduleType("")) }),
			wantErr: true,
		},
		{
			// Two ways to say when would let one request contradict itself.
			name: "add with both change_at and start_date",
			params: withAdd(func(a *AddAddonToSubscriptionRequest) {
				a.ChangeAt = lo.ToPtr(types.ScheduleTypeImmediate)
				a.StartDate = lo.ToPtr(time.Now())
			}),
			wantErr: true,
		},
		{
			name: "remove with both change_at and effective_date",
			params: withRemove(func(r *RemoveAddonRequest) {
				r.ChangeAt = lo.ToPtr(types.ScheduleTypePeriodEnd)
				r.EffectiveDate = lo.ToPtr(time.Now())
			}),
			wantErr: true,
		},
		{
			// nil means "use the default"; an explicit zero timestamp is a client bug.
			name:    "explicit zero start date on add",
			params:  withAdd(func(a *AddAddonToSubscriptionRequest) { a.StartDate = lo.ToPtr(time.Time{}) }),
			wantErr: true,
		},
		{
			name:    "explicit zero effective date on remove",
			params:  withRemove(func(r *RemoveAddonRequest) { r.EffectiveDate = lo.ToPtr(time.Time{}) }),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.params.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.True(t, ierr.IsValidation(err), "expected a validation error, got %v", err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// Entries are the single-addon requests, so their server-only fields must not be settable
// from the wire.
func TestSubModifyAddonsParams_ServerOnlyFieldsAreNotClientSettable(t *testing.T) {
	add := validAddonsAdd()
	assert.False(t, add.PreviewOnly)
	assert.False(t, add.SkipEntityValidation)
	assert.False(t, validAddonsRemove().PreviewOnly)
}

func TestExecuteSubscriptionModifyRequest_Addons(t *testing.T) {
	valid := &SubModifyAddonsParams{Adds: []*AddAddonToSubscriptionRequest{validAddonsAdd()}}

	t.Run("params required for type addons", func(t *testing.T) {
		req := ExecuteSubscriptionModifyRequest{Type: SubscriptionModifyTypeAddons}
		assert.Error(t, req.Validate())
	})

	t.Run("valid batch accepted", func(t *testing.T) {
		req := ExecuteSubscriptionModifyRequest{Type: SubscriptionModifyTypeAddons, AddonsParams: valid}
		assert.NoError(t, req.Validate())
	})

	t.Run("checkout accepted when the batch can charge", func(t *testing.T) {
		req := ExecuteSubscriptionModifyRequest{
			Type:         SubscriptionModifyTypeAddons,
			AddonsParams: valid,
			Checkout:     &CheckoutParams{PaymentParams: PaymentParams{PaymentProvider: types.CheckoutPaymentProviderRazorpay}},
		}
		assert.NoError(t, req.Validate())
	})

	// A removes-only batch can only ever credit, so there is nothing to collect.
	t.Run("checkout rejected for a removes-only batch", func(t *testing.T) {
		req := ExecuteSubscriptionModifyRequest{
			Type:         SubscriptionModifyTypeAddons,
			AddonsParams: &SubModifyAddonsParams{Removes: []*RemoveAddonRequest{validAddonsRemove()}},
			Checkout:     &CheckoutParams{PaymentParams: PaymentParams{PaymentProvider: types.CheckoutPaymentProviderRazorpay}},
		}
		err := req.Validate()
		assert.Error(t, err)
		assert.True(t, ierr.IsValidation(err))
	})
}
