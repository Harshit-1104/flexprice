package types

type ContractStatus string

const (
	ContractStatusDraft     ContractStatus = "draft"
	ContractStatusActive    ContractStatus = "active"
	ContractStatusEnded     ContractStatus = "ended"
	ContractStatusCancelled ContractStatus = "cancelled"
)

// RevRecEntityType is the kind of entity a revrec record links to or is sourced from.
type RevRecEntityType string

const (
	RevRecEntityTypeSubscription RevRecEntityType = "subscription"
)
