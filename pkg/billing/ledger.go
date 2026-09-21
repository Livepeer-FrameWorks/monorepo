package billing

// LedgerCurrency is the currency of every prepaid balance row, usage
// reservation, and admission read. Amounts paid in another currency are
// converted at an ECB reference rate before they reach the ledger.
const LedgerCurrency = "EUR"
