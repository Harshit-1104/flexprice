package types

// IntegrationCapabilityType names a payment operation FlexPrice implements for a
// gateway — not what the gateway's own API can do.
type IntegrationCapabilityType string

const (
	IntegrationCapabilityCheckout                IntegrationCapabilityType = "checkout"
	IntegrationCapabilityAutoCharge              IntegrationCapabilityType = "auto_charge"
	IntegrationCapabilitySetDefaultMethod        IntegrationCapabilityType = "set_default_method"
	IntegrationCapabilityPaymentLink             IntegrationCapabilityType = "payment_link"
	IntegrationCapabilityPaymentMethodManagement IntegrationCapabilityType = "payment_method_management"
)

func (t IntegrationCapabilityType) String() string { return string(t) }

// IntegrationCapability is one operation a provider supports, plus how it is
// configured for the tenant.
type IntegrationCapability struct {
	Type IntegrationCapabilityType `json:"type"`
	// IsDefault marks the capability this provider is chosen for when the caller
	// names none. Per capability, because a tenant's default differs by operation.
	IsDefault bool `json:"is_default"`
}
