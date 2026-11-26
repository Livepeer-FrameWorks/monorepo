package handlers

// AssignmentPhaseEffects states the financial effects actually staged by CommitTx.
// EUR amounts are cents; invoice presentment retains the original currency.
type AssignmentPhaseEffects struct {
	StatementSettlementEUR     int64  `json:"statement_settlement_eur_cents"`
	StatementFeesEUR           int64  `json:"statement_fees_eur_cents"`
	InvoiceDueEUR              int64  `json:"invoice_due_eur_cents"`
	InvoiceWalletCreditEUR     int64  `json:"invoice_wallet_credit_eur_cents"`
	InvoicePresentmentMinor    int64  `json:"invoice_presentment_minor"`
	InvoicePresentmentCurrency string `json:"invoice_presentment_currency"`
	InvoiceStatus              string `json:"invoice_status"`
	CollectionCarryEUR         int64  `json:"collection_carry_eur_cents"`
	UnpaidAdvanceReductionEUR  int64  `json:"unpaid_advance_reduction_eur_cents"`
	PaidAdvanceForfeitureEUR   int64  `json:"paid_advance_forfeiture_eur_cents"`
	CashRefundMinor            int64  `json:"cash_refund_minor"`
}

func (c *PrepaidPhaseClose) AssignmentEffects() AssignmentPhaseEffects {
	if c == nil || !c.closed {
		return AssignmentPhaseEffects{}
	}
	return AssignmentPhaseEffects{StatementSettlementEUR: c.result.details.SettledWithStatementCents, StatementFeesEUR: c.result.details.PeriodFeesCents}
}
func (c *PostpaidPhaseClose) AssignmentEffects() AssignmentPhaseEffects {
	if c == nil || !c.closed {
		return AssignmentPhaseEffects{}
	}
	return AssignmentPhaseEffects{InvoiceDueEUR: c.result.total.Shift(2).Round(0).IntPart(), InvoiceWalletCreditEUR: c.result.credit.Shift(2).Round(0).IntPart(), InvoicePresentmentMinor: c.result.presentment.OriginalMinor, InvoicePresentmentCurrency: c.result.presentment.OriginalCurrency, InvoiceStatus: c.result.status, CollectionCarryEUR: c.carriedCents, UnpaidAdvanceReductionEUR: c.baseFeeDisposition.reducedCents, PaidAdvanceForfeitureEUR: c.baseFeeDisposition.forfeitedCents}
}
