package billing

// HoursPerBillingMonth is the fixed month length used to price time-weighted
// storage (365 × 24 / 12). A fixed length keeps a GiB-month price identical
// across calendar months; usage itself is metered to the second.
const HoursPerBillingMonth = 730

// GiBSecondsPerGiBMonth converts the storage_gb_seconds_* ledger unit into the
// GiB-month unit storage is priced in.
const GiBSecondsPerGiBMonth = HoursPerBillingMonth * 3600
