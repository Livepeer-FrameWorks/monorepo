package billing

import "errors"

// CheckoutRecoveryResult reports provider actions accepted during one recovery.
// RefundRequested means the provider accepted a refund; bank settlement may follow.
type CheckoutRecoveryResult struct {
	Cancelled             bool
	RefundRequested       bool
	RefundAlreadyComplete bool
	RefundSettlements     []RefundSettlement
	ReturnedByChargeback  bool
}

// Outcome names the provider result without claiming that a pending refund settled.
func (r CheckoutRecoveryResult) Outcome() string {
	if r.ReturnedByChargeback {
		outcome := "chargeback_reconciled"
		if r.RefundRequested {
			outcome += "_refund_requested"
		} else if r.RefundAlreadyComplete {
			outcome = "refund_and_" + outcome
		}
		if r.Cancelled {
			outcome = "cancelled_" + outcome
		}
		return outcome
	}
	if r.RefundRequested {
		if !r.Cancelled {
			return "refund_requested"
		}
		return "cancelled_refund_requested"
	}
	if r.RefundAlreadyComplete {
		if !r.Cancelled {
			return "refund_reconciled"
		}
		return "cancelled_refund_reconciled"
	}
	if r.Cancelled {
		return "cancelled"
	}
	return "expired"
}

// RefundSettlement tracks provider-confirmed return of an unactivated purchase.
// TenantID is assigned from the locked local purchase, never from a webhook body.
type RefundSettlement struct {
	Provider      string `json:"provider"`
	TenantID      string `json:"tenant_id"`
	ReferenceKind string `json:"reference_kind"`
	ReferenceID   string `json:"reference_id"`
	InvoiceID     string `json:"invoice_id,omitempty"`
	PaymentID     string `json:"payment_id"`
	ChargeID      string `json:"charge_id,omitempty"`
	AmountCents   int64  `json:"amount_cents"`
	Currency      string `json:"currency"`
}

// SettledRefund is a bank-confirmed provider receipt in its original currency.
type SettledRefund struct {
	ObjectType  string
	ID          string
	PaymentID   string
	ChargeID    string
	AmountCents int64
	Currency    string
}

// ErrRefundSettling means the provider accepted a refund whose bank settlement
// has not covered the compensated purchase yet.
var ErrRefundSettling = errors.New("provider refund is settling")
