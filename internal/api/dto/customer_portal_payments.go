package dto

import (
	"time"

	"github.com/flexprice/flexprice/internal/types"
	"github.com/shopspring/decimal"
)

// Checkout params for customer portal specific apis with limited capabilities.
type PortalCheckoutParams struct {
	RedirectionParams
	PaymentProvider *types.CheckoutPaymentProvider `json:"payment_provider,omitempty"`
	UseSavedMethod  bool                           `json:"use_saved_method,omitempty"`
	IdempotencyKey  *string                        `json:"idempotency_key,omitempty"`
	Metadata        map[string]string              `json:"metadata,omitempty"`
}

type PortalTopUpWalletRequest struct {
	CreditsToAdd   decimal.Decimal       `json:"credits_to_add" swaggertype:"string"`
	Amount         decimal.Decimal       `json:"amount,omitempty" swaggertype:"string"`
	IdempotencyKey *string               `json:"idempotency_key" binding:"required"`
	Description    string                `json:"description,omitempty"`
	Checkout       *PortalCheckoutParams `json:"checkout,omitempty"`
}

type PortalCheckoutSessionResponse struct {
	ID                string                        `json:"id"`
	CheckoutStatus    types.CheckoutStatus          `json:"checkout_status"`
	PaymentProvider   types.CheckoutPaymentProvider `json:"payment_provider"`
	PaymentAction     *types.PaymentAction          `json:"payment_action,omitempty"`
	CheckoutInvoiceID *string                       `json:"checkout_invoice_id,omitempty"`
	CheckoutPaymentID *string                       `json:"checkout_payment_id,omitempty"`
	ExpiresAt         time.Time                     `json:"expires_at"`
	CompletedAt       *time.Time                    `json:"completed_at,omitempty"`
	CancelledAt       *time.Time                    `json:"cancelled_at,omitempty"`
	FailureReason     *string                       `json:"failure_reason,omitempty"`
}

type PortalTopUpWalletResponse struct {
	WalletTransaction *WalletTransactionResponse     `json:"wallet_transaction"`
	InvoiceID         *string                        `json:"invoice_id,omitempty"`
	Wallet            *WalletResponse                `json:"wallet"`
	CheckoutSession   *PortalCheckoutSessionResponse `json:"checkout_session,omitempty"`
}

type PortalAddPaymentMethodRequest struct {
	PaymentProvider *types.PaymentGatewayType `json:"payment_provider,omitempty"`
	RedirectionParams
	SetDefault bool `json:"set_default,omitempty"`
}

type PortalUpdateAutoTopupRequest struct {
	Enabled   bool             `json:"enabled"`
	Threshold *decimal.Decimal `json:"threshold,omitempty" swaggertype:"string"`
	Amount    *decimal.Decimal `json:"amount,omitempty" swaggertype:"string"`
	Cooldown  *types.Duration  `json:"cooldown,omitempty"`
}

type PortalPayInvoiceRequest struct {
	RedirectionParams
	PaymentProvider *types.PaymentGatewayType `json:"payment_provider,omitempty"`
	UseSavedMethod  bool                      `json:"use_saved_method,omitempty"`
	IdempotencyKey  *string                   `json:"idempotency_key,omitempty"`
}

type PortalPayInvoiceResponse struct {
	PaymentID     string               `json:"payment_id"`
	InvoiceID     string               `json:"invoice_id"`
	Status        types.PaymentStatus  `json:"status"`
	Amount        decimal.Decimal      `json:"amount" swaggertype:"string"`
	Currency      string               `json:"currency"`
	PaymentAction *types.PaymentAction `json:"payment_action,omitempty"`
}
