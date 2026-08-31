package service

import (
	"context"
	"strings"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/wallet"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
)

// TopUpWallet starts a pay-first wallet top-up.
//
// use_saved_method selects collection_method=charge_automatically so the adapter
// charges off-session; otherwise the adapter returns a redirect action.
func (s *customerPortalService) TopUpWallet(ctx context.Context, walletID string, req *dto.PortalTopUpWalletRequest) (*dto.PortalTopUpWalletResponse, error) {
	if req == nil {
		return nil, ierr.NewError("request is required").Mark(ierr.ErrValidation)
	}

	w, err := s.authorizeWallet(ctx, walletID)
	if err != nil {
		return nil, err
	}
	if err := s.validateTopupAmount(ctx, w, req.CreditsToAdd); err != nil {
		return nil, err
	}

	// Everything the portal customer must not choose is pinned here, not taken
	// from the request: reason (so they cannot grant themselves free credits),
	// expiry (none), priority (nil -> consumed after prioritized grants), and
	// the provider config.
	walletReq := &dto.TopUpWalletRequest{
		CreditsToAdd:      req.CreditsToAdd,
		Amount:            req.Amount,
		TransactionReason: types.TransactionReasonPurchasedCreditInvoiced,
		IdempotencyKey:    req.IdempotencyKey,
		Description:       req.Description,
	}
	if walletReq.Description == "" {
		walletReq.Description = "Wallet top-up from customer portal"
	}

	if req.Checkout != nil {
		if req.Checkout.PaymentProvider == nil {
			return nil, ierr.NewError("payment_provider is required").
				WithHint("Specify the payment provider to check out with").
				Mark(ierr.ErrValidation)
		}

		collectionMethod := types.CollectionMethodSendInvoice
		if req.Checkout.UseSavedMethod {
			collectionMethod = types.CollectionMethodChargeAutomatically
		}

		idempotencyKey := req.Checkout.IdempotencyKey
		if idempotencyKey == nil {
			idempotencyKey = req.IdempotencyKey
		}

		walletReq.Checkout = &dto.CheckoutParams{
			PaymentParams: dto.PaymentParams{
				PaymentProvider: *req.Checkout.PaymentProvider,
				PaymentProviderConfig: &types.CheckoutPaymentProviderConfig{
					CollectionMethod: collectionMethod,
					CustomerPresent: true,
				},
			},
			RedirectionParams: req.Checkout.RedirectionParams,
			IdempotencyKey:    idempotencyKey,
			Metadata:          req.Checkout.Metadata,
		}
	}

	walletSvc := NewWalletService(s.ServiceParams)
	topupResp, err := walletSvc.TopUpWallet(ctx, walletID, walletReq)
	if err != nil {
		return nil, err
	}

	resp := &dto.PortalTopUpWalletResponse{
		WalletTransaction: topupResp.WalletTransaction,
		InvoiceID:         topupResp.InvoiceID,
		Wallet:            topupResp.Wallet,
	}

	// No checkout requested: invoiced / pay-later behavior, nothing more to do.
	if req.Checkout == nil {
		return resp, nil
	}

	if topupResp.CheckoutSession == nil {
		return nil, ierr.NewError("top-up did not produce a checkout session").
			WithHint("Expected a pay-first checkout session").
			Mark(ierr.ErrInternal)
	}

	resp.CheckoutSession = toPortalCheckoutSession(topupResp.CheckoutSession)
	return resp, nil
}

func (s *customerPortalService) UpdateAutoTopup(ctx context.Context, walletID string, req *dto.PortalUpdateAutoTopupRequest) (*dto.WalletResponse, error) {
	return nil, ierr.NewError("auto top-up is not available yet").
		WithHint("This endpoint is not implemented yet").
		Mark(ierr.ErrNotImplemented)
}

// validateTopupAmount rejects a credit amount that converts to less than the
// tenant's per-currency floor, which the gateway would otherwise reject only
// after the customer has been redirected.
func (s *customerPortalService) validateTopupAmount(ctx context.Context, w *wallet.Wallet, creditsToAdd decimal.Decimal) error {
	if creditsToAdd.LessThanOrEqual(decimal.Zero) {
		return ierr.NewError("credits must be greater than zero").
			WithHint("Enter a credit amount greater than zero").
			Mark(ierr.ErrValidation)
	}

	minAmount, err := s.minTopupAmount(ctx, w.Currency)
	if err != nil {
		return err
	}
	if !minAmount.IsPositive() {
		return nil
	}

	amount := NewWalletService(s.ServiceParams).GetCurrencyAmountFromCredits(creditsToAdd, w.TopupConversionRate)
	if amount.LessThan(minAmount) {
		return ierr.NewError("top-up amount is below the minimum").
			WithHintf("Minimum top-up for %s is %s", strings.ToUpper(w.Currency), minAmount.String()).
			WithReportableDetails(map[string]any{
				"currency":       w.Currency,
				"minimum_amount": minAmount.String(),
				"amount":         amount.String(),
			}).
			Mark(ierr.ErrValidation)
	}
	return nil
}

func (s *customerPortalService) minTopupAmount(ctx context.Context, currency string) (decimal.Decimal, error) {
	settingsSvc := NewSettingsService(s.ServiceParams).(*settingsService)
	cfg, err := GetSetting[types.WalletTopupConfig](settingsSvc, ctx, types.SettingKeyWalletTopupConfig)
	if err != nil {
		return decimal.Zero, err
	}

	return cfg.MinTopupAmount(currency), nil
}
