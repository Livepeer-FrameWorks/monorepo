package control

import "testing"

// decideReconcileAction is the startup reconciliation matrix. Every documented
// row is asserted here so a regression that mis-transitions a segment (the kind
// that silently corrupts chapter placement or loses media) is caught.
func TestDecideReconcileAction(t *testing.T) {
	cases := []struct {
		name                        string
		status                      string
		present, hasPDT, pdtMatches bool
		want                        reconcileAction
	}{
		// uploaded / deleted_local: never transitioned, regardless of presence.
		{"uploaded + present", "uploaded", true, true, true, reconcileNoop},
		{"uploaded + missing", "uploaded", false, false, false, reconcileNoop},
		{"deleted_local + present", "deleted_local", true, true, true, reconcileNoop},
		{"deleted_local + missing", "deleted_local", false, false, false, reconcileNoop},

		// orphan_unreachable: reconcile to ledger only when the file is present.
		{"orphan + present", "orphan_unreachable", true, false, false, reconcileDeleteOrphan},
		{"orphan + missing", "orphan_unreachable", false, false, false, reconcileNoop},

		// pending / failed_upload: upload if present, else drop pre-upload.
		{"pending + present", "pending", true, false, false, reconcileUpload},
		{"pending + missing", "pending", false, false, false, reconcileDropPreUpload},
		{"failed_upload + present", "failed_upload", true, false, false, reconcileUpload},
		{"failed_upload + missing", "failed_upload", false, false, false, reconcileDropPreUpload},

		// lost_local: heal only on present + matching PDT.
		{"lost_local + present + match", "lost_local", true, true, true, reconcileHeal},
		{"lost_local + present + no PDT", "lost_local", true, false, false, reconcileSkipUnhealable},
		{"lost_local + present + mismatch", "lost_local", true, true, false, reconcileSkipUnhealable},
		{"lost_local + missing", "lost_local", false, false, false, reconcileNoop},

		// No ledger row: fabricate only with trustworthy PDT.
		{"no row + present + PDT", "", true, true, true, reconcileInsertUpload},
		{"no row + present + no PDT", "", true, false, false, reconcileSkipNoPDT},
		{"no row + missing + PDT", "", false, true, true, reconcileInsertDrop},
		{"no row + missing + no PDT", "", false, false, false, reconcileSkipNoPDT},
		{"unknown status falls through to no-row branch", "weird_status", true, true, true, reconcileInsertUpload},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideReconcileAction(tc.status, tc.present, tc.hasPDT, tc.pdtMatches)
			if got != tc.want {
				t.Errorf("decideReconcileAction(%q, present=%v, hasPDT=%v, pdtMatches=%v) = %d, want %d",
					tc.status, tc.present, tc.hasPDT, tc.pdtMatches, got, tc.want)
			}
		})
	}
}
