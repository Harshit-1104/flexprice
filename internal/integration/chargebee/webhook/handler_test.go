package webhook

// Tests the routing decision in handlePaymentSucceeded: a payment that belongs to a
// checkout session must complete the session and must NOT also run
// ReconcileInvoicePayment, which double-counts and never credits the wallet.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	domainCheckout "github.com/flexprice/flexprice/internal/domain/checkout"
	"github.com/flexprice/flexprice/internal/domain/entityintegrationmapping"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/integration/chargebee"
	"github.com/flexprice/flexprice/internal/interfaces"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/suite"
)

const (
	testChargebeeInvoiceID = "cb_inv_001"
	testFlexpriceInvoiceID = "inv_flex_001"
	testChargebeeTxnID     = "txn_001"
	testFlexpricePaymentID = "pay_flex_001"
)

// ── entityintegrationmapping.Repository fake: maps one Chargebee invoice ─────

type mappingStore struct {
	entityIDByProviderEntityID map[string]string
}

func (mappingStore) Create(_ context.Context, _ *entityintegrationmapping.EntityIntegrationMapping) error {
	return nil
}
func (mappingStore) Get(_ context.Context, _ string) (*entityintegrationmapping.EntityIntegrationMapping, error) {
	return nil, ierr.NewError("not found").Mark(ierr.ErrNotFound)
}
func (s mappingStore) List(_ context.Context, filter *types.EntityIntegrationMappingFilter) ([]*entityintegrationmapping.EntityIntegrationMapping, error) {
	if len(filter.ProviderEntityIDs) == 0 {
		return nil, nil
	}
	entityID, ok := s.entityIDByProviderEntityID[filter.ProviderEntityIDs[0]]
	if !ok {
		return nil, nil
	}
	return []*entityintegrationmapping.EntityIntegrationMapping{{EntityID: entityID}}, nil
}
func (mappingStore) Count(_ context.Context, _ *types.EntityIntegrationMappingFilter) (int, error) {
	return 0, nil
}
func (mappingStore) Update(_ context.Context, _ *entityintegrationmapping.EntityIntegrationMapping) error {
	return nil
}
func (mappingStore) Delete(_ context.Context, _ *entityintegrationmapping.EntityIntegrationMapping) error {
	return nil
}

// ── interfaces.CheckoutSessionService fake ───────────────────────────────────

type fakeCheckoutSessionService struct {
	interfaces.CheckoutSessionService
	session        *dto.CheckoutSessionResponse
	listErr        error
	completeCalls  []string
	completeErr    error
	cleanupCalls   []string
	cleanupReasons []error
}

func (s *fakeCheckoutSessionService) List(_ context.Context, filter *types.CheckoutSessionFilter) (*dto.ListCheckoutSessionsResponse, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	if s.session == nil || len(filter.CheckoutPaymentIDs) == 0 || filter.CheckoutPaymentIDs[0] != testFlexpricePaymentID {
		return &dto.ListCheckoutSessionsResponse{}, nil
	}
	return &dto.ListCheckoutSessionsResponse{Items: []*dto.CheckoutSessionResponse{s.session}}, nil
}

func (s *fakeCheckoutSessionService) CompleteCheckoutSession(_ context.Context, sessionID string, _ *types.CheckoutProviderResult) error {
	s.completeCalls = append(s.completeCalls, sessionID)
	return s.completeErr
}

func (s *fakeCheckoutSessionService) CleanupCheckoutSession(_ context.Context, sessionID string, reason error) error {
	s.cleanupCalls = append(s.cleanupCalls, sessionID)
	s.cleanupReasons = append(s.cleanupReasons, reason)
	return nil
}

// ── suite ────────────────────────────────────────────────────────────────────

type ChargebeeWebhookCheckoutSuite struct {
	suite.Suite
	ctx         context.Context
	handler     *Handler
	checkoutSvc *fakeCheckoutSessionService
	services    *ServiceDependencies
}

func TestChargebeeWebhookCheckout(t *testing.T) {
	suite.Run(t, new(ChargebeeWebhookCheckoutSuite))
}

func (s *ChargebeeWebhookCheckoutSuite) SetupTest() {
	s.ctx = types.SetTenantID(context.Background(), "tenant_test")
	s.ctx = types.SetEnvironmentID(s.ctx, "env_test")

	log := logger.NewNoopLogger()

	invoiceSvc := &chargebee.InvoiceService{
		InvoiceServiceParams: chargebee.InvoiceServiceParams{
			EntityIntegrationMappingRepo: mappingStore{
				entityIDByProviderEntityID: map[string]string{
					testChargebeeInvoiceID: testFlexpriceInvoiceID,
				},
			},
			Logger: log,
		},
	}

	s.handler = NewHandler(nil, invoiceSvc, log)
	s.checkoutSvc = &fakeCheckoutSessionService{}
	s.services = &ServiceDependencies{CheckoutSessionService: s.checkoutSvc}
}

func (s *ChargebeeWebhookCheckoutSuite) seedSession(status types.CheckoutStatus) *dto.CheckoutSessionResponse {
	session := &dto.CheckoutSessionResponse{
		CheckoutSession: &domainCheckout.CheckoutSession{
			ID:             "cs_001",
			CheckoutStatus: status,
		},
	}
	s.checkoutSvc.session = session
	return session
}

func (s *ChargebeeWebhookCheckoutSuite) event(eventType ChargebeeEventType) *ChargebeeWebhookEvent {
	content, err := json.Marshal(map[string]any{
		"transaction": map[string]any{"id": testChargebeeTxnID, "amount": 10000, "currency_code": "USD"},
		"invoice":     map[string]any{"id": testChargebeeInvoiceID, "po_number": testFlexpricePaymentID},
	})
	s.Require().NoError(err)
	return &ChargebeeWebhookEvent{ID: "ev_001", EventType: string(eventType), Content: content}
}

// The whole point of Phase 4: a checkout payment completes the session.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_PendingSessionIsCompleted() {
	session := s.seedSession(types.CheckoutStatusPending)

	handled, err := s.handler.handleCheckoutSessionForPayment(s.ctx, testFlexpricePaymentID, testFlexpriceInvoiceID, testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().NoError(err)
	s.True(handled, "a checkout payment must not fall through to invoice reconciliation")
	s.Equal([]string{session.ID}, s.checkoutSvc.completeCalls)
}

// At-least-once delivery: the second webhook must be a quiet no-op, not an error.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_AlreadyCompletedIsNotAnError() {
	s.seedSession(types.CheckoutStatusPending)
	s.checkoutSvc.completeErr = ierr.NewError("already completed").Mark(ierr.ErrAlreadyExists)

	handled, err := s.handler.handleCheckoutSessionForPayment(s.ctx, testFlexpricePaymentID, testFlexpriceInvoiceID, testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().NoError(err)
	s.True(handled)
}

// An ordinary invoice payment has no session and must reconcile as before.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_NoSessionFallsThrough() {
	handled, err := s.handler.handleCheckoutSessionForPayment(s.ctx, "pay_no_session", testFlexpriceInvoiceID, testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().NoError(err)
	s.False(handled, "a standalone payment must still reach ReconcileInvoicePayment")
	s.Empty(s.checkoutSvc.completeCalls)
}

// A late payment on a terminal session must not be completed. Phase 6 refunds it.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_ExpiredSessionIsNotCompleted() {
	s.seedSession(types.CheckoutStatusExpired)

	handled, err := s.handler.handleCheckoutSessionForPayment(s.ctx, testFlexpricePaymentID, testFlexpriceInvoiceID, testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().NoError(err)
	s.True(handled, "must still not fall through: reconciling would credit an archived invoice")
	s.Empty(s.checkoutSvc.completeCalls)
}

// A lookup failure must not silently fall through to the double-counting path.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_LookupErrorDoesNotFallThrough() {
	s.checkoutSvc.listErr = ierr.NewError("db down").Mark(ierr.ErrDatabase)

	handled, err := s.handler.handleCheckoutSessionForPayment(s.ctx, testFlexpricePaymentID, testFlexpriceInvoiceID, testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().Error(err)
	s.False(handled)
	s.Empty(s.checkoutSvc.completeCalls)
}

func (s *ChargebeeWebhookCheckoutSuite) TestPaymentFailed_CleansUpPendingSession() {
	session := s.seedSession(types.CheckoutStatusPending)

	s.Require().NoError(s.handler.HandleWebhookEvent(s.ctx, s.event(EventPaymentFailed), "env_test", s.services))

	s.Equal([]string{session.ID}, s.checkoutSvc.cleanupCalls)
	s.Require().Len(s.checkoutSvc.cleanupReasons, 1)
	s.Require().NotNil(s.checkoutSvc.cleanupReasons[0], "a failure must be recorded as failed, not expired")
	s.Contains(s.checkoutSvc.cleanupReasons[0].Error(), testChargebeeTxnID)
}

// Cleanup archives the session's invoice and payment, so it must never run against
// a session that already settled.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentFailed_CompletedSessionUntouched() {
	s.seedSession(types.CheckoutStatusCompleted)

	s.Require().NoError(s.handler.HandleWebhookEvent(s.ctx, s.event(EventPaymentFailed), "env_test", s.services))

	s.Empty(s.checkoutSvc.cleanupCalls)
}

func (s *ChargebeeWebhookCheckoutSuite) TestUnhandledEventTypeIsIgnored() {
	s.seedSession(types.CheckoutStatusPending)

	s.Require().NoError(s.handler.HandleWebhookEvent(s.ctx, s.event(EventInvoiceUpdated), "env_test", s.services))

	s.Empty(s.checkoutSvc.completeCalls)
	s.Empty(s.checkoutSvc.cleanupCalls)
}

// A hosted checkout page creates its own Chargebee invoice with no entity mapping.
// po_number carries the Flexprice payment id — verified live against the test site.
func (s *ChargebeeWebhookCheckoutSuite) TestPaymentSucceeded_HostedPageRoutesByPONumber() {
	session := s.seedSession(types.CheckoutStatusPending)

	handled, err := s.handler.handleCheckoutSessionForPayment(
		s.ctx, testFlexpricePaymentID, "", testChargebeeInvoiceID, testChargebeeTxnID, s.services)

	s.Require().NoError(err)
	s.True(handled, "an unmapped hosted-page invoice must still find its session")
	s.Equal([]string{session.ID}, s.checkoutSvc.completeCalls)
}

// End to end through HandleWebhookEvent: no invoice mapping exists, so routing must
// come from po_number alone.
func (s *ChargebeeWebhookCheckoutSuite) TestHostedPageWebhook_UnmappedInvoiceCompletesSession() {
	session := s.seedSession(types.CheckoutStatusPending)

	content, err := json.Marshal(map[string]any{
		"transaction": map[string]any{"id": testChargebeeTxnID, "amount": 1000, "currency_code": "USD"},
		"invoice":     map[string]any{"id": "cb_inv_unmapped", "po_number": testFlexpricePaymentID},
	})
	s.Require().NoError(err)

	s.Require().NoError(s.handler.HandleWebhookEvent(s.ctx, &ChargebeeWebhookEvent{
		ID: "ev_hosted", EventType: string(EventPaymentSucceeded), Content: content,
	}, "env_test", s.services))

	s.Equal([]string{session.ID}, s.checkoutSvc.completeCalls)
}
