package dvrpolicy

// ChapterFailureLedgerInconsistent identifies a source-ledger validation failure
// without asserting that the recorded source bytes have been lost.
const ChapterFailureLedgerInconsistent = "ledger_inconsistent"

// LedgerInconsistentError rejects a remux whose requested timeline is not
// contiguous. The ledger requires repair or an explicit operator decision;
// replaying the same request cannot establish whether source media is missing.
type LedgerInconsistentError struct {
	Detail string
}

func (e *LedgerInconsistentError) Error() string {
	return "LEDGER_INCONSISTENT: " + e.Detail
}
