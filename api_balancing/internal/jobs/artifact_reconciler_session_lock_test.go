package jobs

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/fakesession"
)

// Each single-flight pass pins a session for its advisory lock. A session that
// cannot answer the lock query is closed, so the next pass gets a working one.
func TestReconcilerPassesDiscardASessionThatCannotTakeTheirLock(t *testing.T) {
	passes := map[string]func(*ArtifactReconciler){
		"projection":          func(r *ArtifactReconciler) { r.reconcileUnderLock() },
		"billing attribution": func(r *ArtifactReconciler) { r.reconcileBillingAttribution() },
		"publication ledger":  func(r *ArtifactReconciler) { r.reconcileFreezePublicationLedgerPass() },
	}
	for name, pass := range passes {
		t.Run(name, func(t *testing.T) {
			db, server := fakesession.New(func(session int, _ string) (bool, error) {
				if session == 1 {
					return false, fakesession.ExpiredSessionError()
				}
				return false, nil
			})
			defer db.Close()
			r := &ArtifactReconciler{db: db, logger: logging.NewLogger()}
			pass(r)
			if !server.Closed(1) {
				t.Fatal("the session that failed the lock query went back to the pool")
			}
			pass(r)
			if server.Opened() != 2 {
				t.Fatalf("opened=%d; want the next pass on a fresh session", server.Opened())
			}
		})
	}
}
