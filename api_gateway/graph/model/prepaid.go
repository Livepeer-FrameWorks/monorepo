package model

import (
	"time"
)

// BalanceTransaction represents a single prepaid balance transaction.
// Used for audit trail of balance changes (topups, deductions, adjustments).
type BalanceTransaction struct {
	ID                string    `json:"id"`
	TenantID          string    `json:"tenantId"`
	AmountCents       int       `json:"amountCents"`
	BalanceAfterCents int       `json:"balanceAfterCents"`
	TransactionType   string    `json:"transactionType"`
	Description       *string   `json:"description"`
	ReferenceID       *string   `json:"referenceId"`
	ReferenceType     *string   `json:"referenceType"`
	CreatedAt         time.Time `json:"createdAt"`
}

// PrepaidBalance represents a tenant's prepaid balance.
// We can use the proto type directly since it's not in a union.
// But we need this for connections.
type PrepaidBalance struct {
	ID                       string    `json:"id"`
	TenantID                 string    `json:"tenantId"`
	BalanceCents             int       `json:"balanceCents"`
	Currency                 string    `json:"currency"`
	LowBalanceThresholdCents int       `json:"lowBalanceThresholdCents"`
	IsLowBalance             bool      `json:"isLowBalance"`
	DrainRateCentsPerHour    int       `json:"drainRateCentsPerHour"`
	CreatedAt                time.Time `json:"createdAt"`
	UpdatedAt                time.Time `json:"updatedAt"`
}
