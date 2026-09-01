package v1

import (
	"net/http"

	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/ee/service"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

// CustomerPortalHandler handles customer portal API requests
type CustomerPortalHandler struct {
	portalService service.CustomerPortalService
	log           *logger.Logger
}

// NewCustomerPortalHandler creates a new customer portal handler
func NewCustomerPortalHandler(
	portalService service.CustomerPortalService,
	log *logger.Logger,
) *CustomerPortalHandler {
	return &CustomerPortalHandler{
		portalService: portalService,
		log:           log,
	}
}

func (h *CustomerPortalHandler) CreateSession(c *gin.Context) {
	externalID := c.Param("external_id")
	if externalID == "" {
		c.Error(ierr.NewError("external_id is required").Mark(ierr.ErrValidation))
		return
	}
	response, err := h.portalService.CreatePortalSession(c.Request.Context(), externalID)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetCustomer(c *gin.Context) {
	response, err := h.portalService.GetCustomer(c.Request.Context())
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) UpdateCustomer(c *gin.Context) {
	var req dto.UpdateCustomerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.UpdateCustomer(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetUsageSummary(c *gin.Context) {
	var req dto.GetCustomerUsageSummaryRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetUsageSummary(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetSubscriptions(c *gin.Context) {
	var req dto.PortalPaginatedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetSubscriptions(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetInvoices(c *gin.Context) {
	var req dto.PortalPaginatedRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetInvoices(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetInvoice(c *gin.Context) {
	invoiceID := c.Param("id")
	if invoiceID == "" {
		c.Error(ierr.NewError("invoice_id is required").Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetInvoice(c.Request.Context(), invoiceID)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetSubscription(c *gin.Context) {
	subscriptionID := c.Param("id")
	if subscriptionID == "" {
		c.Error(ierr.NewError("subscription_id is required").Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetSubscription(c.Request.Context(), subscriptionID)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetWallets(c *gin.Context) {
	response, err := h.portalService.GetWallets(c.Request.Context())
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetWallet(c *gin.Context) {
	walletID := c.Param("id")
	if walletID == "" {
		c.Error(ierr.NewError("wallet_id is required").Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetWallet(c.Request.Context(), walletID)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetAnalytics(c *gin.Context) {
	var req dto.PortalAnalyticsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetAnalytics(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetCostAnalytics(c *gin.Context) {
	var req dto.PortalCostAnalyticsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	response, err := h.portalService.GetCostAnalytics(c.Request.Context(), req)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

func (h *CustomerPortalHandler) GetInvoicePDF(c *gin.Context) {
	invoiceID := c.Param("id")
	if invoiceID == "" {
		c.Error(ierr.NewError("invoice_id is required").Mark(ierr.ErrValidation))
		return
	}

	url, err := h.portalService.GetInvoicePDFUrl(c.Request.Context(), invoiceID)
	if err != nil {
		h.log.Error(c.Request.Context(), "failed to get invoice pdf url", "error", err, "invoice_id", invoiceID)
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"presigned_url": url})
}

// GetPortalConfig godoc
// @Summary Tenant-authored portal configuration
// @ID portalGetConfig
// @Tags Customer Portal
// @Produce json
// @Success 200 {object} dto.SettingResponse
// @Failure 500 {object} ierr.ErrorResponse
// @x-scope "read"
// @Router /customer/portal/config [get]
func (h *CustomerPortalHandler) GetPortalConfig(c *gin.Context) {
	response, err := h.portalService.GetPortalConfig(c.Request.Context())
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

// GetIntegrations godoc
// @Summary Payment integrations configured for this tenant
// @ID portalGetIntegrations
// @Tags Customer Portal
// @Produce json
// @Success 200 {object} dto.IntegrationsResponse
// @Failure 500 {object} ierr.ErrorResponse
// @x-scope "read"
// @Router /customer/portal/integrations [get]
func (h *CustomerPortalHandler) GetIntegrations(c *gin.Context) {
	resp, err := h.portalService.GetIntegrations(c.Request.Context())
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *CustomerPortalHandler) GetWalletTransactions(c *gin.Context) {
	walletID := c.Param("id")
	if walletID == "" {
		c.Error(ierr.NewError("wallet_id is required").Mark(ierr.ErrValidation))
		return
	}

	var filter types.WalletTransactionFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		h.log.Error(c.Request.Context(), "failed to bind query", "error", err)
		c.Error(ierr.WithError(err).
			WithHint("Invalid filter parameters").
			Mark(ierr.ErrValidation))
		return
	}

	if filter.GetLimit() == 0 {
		filter.Limit = lo.ToPtr(types.GetDefaultFilter().Limit)
	}

	response, err := h.portalService.GetWalletTransactions(c.Request.Context(), walletID, &filter)
	if err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, response)
}

// TopUpWallet godoc
// @Summary Top up a wallet
// @ID portalTopUpWallet
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param id path string true "Wallet ID"
// @Param request body dto.PortalTopUpWalletRequest true "Top-up request"
// @Success 200 {object} dto.PortalTopUpWalletResponse
// @Failure 400 {object} ierr.ErrorResponse
// @x-scope "write"
// @Router /customer/portal/wallets/{id}/top-up [post]
func (h *CustomerPortalHandler) TopUpWallet(c *gin.Context) {
	walletID := c.Param("id")
	if walletID == "" {
		c.Error(ierr.NewError("wallet_id is required").Mark(ierr.ErrValidation))
		return
	}

	var req dto.PortalTopUpWalletRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}

	resp, err := h.portalService.TopUpWallet(c.Request.Context(), walletID, &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// UpdateAutoTopup godoc
// @Summary Configure auto top-up on the customer's wallet
// @ID portalUpdateAutoTopup
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param id path string true "Wallet ID"
// @Param request body dto.PortalUpdateAutoTopupRequest true "Auto top-up configuration"
// @Success 200 {object} dto.WalletResponse
// @Failure 400 {object} ierr.ErrorResponse
// @Failure 403 {object} ierr.ErrorResponse
// @x-scope "write"
// @Router /customer/portal/wallets/{id}/auto-topup [put]
func (h *CustomerPortalHandler) UpdateAutoTopup(c *gin.Context) {
	walletID := c.Param("id")
	if walletID == "" {
		c.Error(ierr.NewError("wallet_id is required").Mark(ierr.ErrValidation))
		return
	}
	var req dto.PortalUpdateAutoTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.UpdateAutoTopup(c.Request.Context(), walletID, &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// PayInvoice godoc
// @Summary Pay an invoice via a provider-hosted payment link
// @ID portalPayInvoice
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param id path string true "Invoice ID"
// @Param request body dto.PortalPayInvoiceRequest true "Payment request"
// @Success 200 {object} dto.PortalPayInvoiceResponse
// @Failure 400 {object} ierr.ErrorResponse
// @Failure 403 {object} ierr.ErrorResponse
// @Failure 404 {object} ierr.ErrorResponse
// @Failure 409 {object} ierr.ErrorResponse
// @x-scope "write"
// @Router /customer/portal/invoices/{id}/pay [post]
func (h *CustomerPortalHandler) PayInvoice(c *gin.Context) {
	invoiceID := c.Param("id")
	if invoiceID == "" {
		c.Error(ierr.NewError("invoice_id is required").Mark(ierr.ErrValidation))
		return
	}
	var req dto.PortalPayInvoiceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.PayInvoice(c.Request.Context(), invoiceID, &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// ListPaymentMethods godoc
// @Summary List the customer's saved payment methods
// @ID portalListPaymentMethods
// @Tags Customer Portal
// @Produce json
// @Param providers query []string false "Providers to read from; omit for all configured" collectionFormat(csv)
// @Success 200 {object} dto.SavedPaymentMethodsResponse
// @Failure 500 {object} ierr.ErrorResponse
// @x-scope "read"
// @Router /customer/portal/payment-methods [get]
func (h *CustomerPortalHandler) ListPaymentMethods(c *gin.Context) {
	var req dto.ListSavedPaymentMethodsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.ListPaymentMethods(c.Request.Context(), &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// AddPaymentMethod godoc
// @Summary Start adding a payment method on a provider-hosted page
// @ID portalAddPaymentMethod
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param request body dto.PortalAddPaymentMethodRequest true "Add payment method request"
// @Success 200 {object} dto.AddPaymentMethodResponse
// @Failure 400 {object} ierr.ErrorResponse
// @x-scope "write"
// @Router /customer/portal/payment-methods [post]
func (h *CustomerPortalHandler) AddPaymentMethod(c *gin.Context) {
	var req dto.PortalAddPaymentMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.AddPaymentMethod(c.Request.Context(), &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// DeletePaymentMethod godoc
// @Summary Remove a saved payment method
// @ID portalDeletePaymentMethod
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param request body dto.PortalDeletePaymentMethodRequest true "Payment method to remove"
// @Success 200 {object} dto.SavedPaymentMethodsResponse
// @Failure 400 {object} ierr.ErrorResponse
// @Failure 403 {object} ierr.ErrorResponse
// @Failure 409 {object} ierr.ErrorResponse
// @x-scope "delete"
// @Router /customer/portal/payment-methods/delete [post]
func (h *CustomerPortalHandler) DeletePaymentMethod(c *gin.Context) {
	var req dto.PortalDeletePaymentMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.DeletePaymentMethod(c.Request.Context(), &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// GetCheckoutSession godoc
// @Summary Read an in-flight or finished checkout session
// @ID portalGetCheckoutSession
// @Tags Customer Portal
// @Produce json
// @Param id path string true "Checkout session ID"
// @Success 200 {object} dto.PortalCheckoutSessionResponse
// @Failure 403 {object} ierr.ErrorResponse
// @Failure 404 {object} ierr.ErrorResponse
// @x-scope "read"
// @Router /customer/portal/checkout-sessions/{id} [get]
func (h *CustomerPortalHandler) GetCheckoutSession(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		c.Error(ierr.NewError("session_id is required").Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.GetCheckoutSession(c.Request.Context(), sessionID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// CancelCheckoutSession godoc
// @Summary Cancel an in-flight checkout session
// @ID portalCancelCheckoutSession
// @Tags Customer Portal
// @Produce json
// @Param id path string true "Checkout session ID"
// @Success 200 {object} dto.PortalCheckoutSessionResponse
// @Failure 400 {object} ierr.ErrorResponse
// @x-scope "delete"
// @Router /customer/portal/checkout-sessions/{id}/cancel [post]
func (h *CustomerPortalHandler) CancelCheckoutSession(c *gin.Context) {
	sessionID := c.Param("id")
	if sessionID == "" {
		c.Error(ierr.NewError("session_id is required").Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.CancelCheckoutSession(c.Request.Context(), sessionID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}

// SetDefaultPaymentMethod godoc
// @Summary Make a saved card the one auto-charge uses
// @ID portalSetDefaultPaymentMethod
// @Tags Customer Portal
// @Accept json
// @Produce json
// @Param request body dto.PortalSetDefaultPaymentMethodRequest true "Payment method to make default"
// @Success 200 {object} dto.SavedPaymentMethodsResponse
// @Failure 400 {object} ierr.ErrorResponse
// @Failure 403 {object} ierr.ErrorResponse
// @Failure 404 {object} ierr.ErrorResponse
// @x-scope "write"
// @Router /customer/portal/payment-methods/default [post]
func (h *CustomerPortalHandler) SetDefaultPaymentMethod(c *gin.Context) {
	var req dto.PortalSetDefaultPaymentMethodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(ierr.WithError(err).Mark(ierr.ErrValidation))
		return
	}
	resp, err := h.portalService.SetDefaultPaymentMethod(c.Request.Context(), &req)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, resp)
}
