package types

type IntegrationCapability string

const (
	IntegrationCapabilityCheckout   IntegrationCapability = "checkout"
	IntegrationCapabilityAutoCharge IntegrationCapability = "auto_charge"
	IntegrationCapabilitySetDefault IntegrationCapability = "set_default"
)
