package billing

// CompletedInvoiceRefund binds a full cash return to its original settled payment.
type CompletedInvoiceRefund struct {
	TenantID           string
	InvoiceID          string
	RequestID          string
	PaymentID          string
	ProviderPaymentID  string
	ProviderCustomerID string
	AmountMinor        int64
	Currency           string
	RefundID           string
}
type CompletedInvoiceRefundReceipt struct {
	ID, Status, ChargeID string
	AmountMinor          int64
	Currency             string
}
