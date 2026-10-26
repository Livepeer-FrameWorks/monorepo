package cmd

import (
	"context"
	"strings"
	"testing"
	"time"
)

// slowScopesTransition has scopes pending scopes whose Check each takes checkTime and fails on an ended context; with
// stallAt set, the Check of that scope blocks until its context ends.
type slowScopesTransition struct {
	fakeTransition
	scopes    int
	checkTime time.Duration
	stallAt   string
}

func (s slowScopesTransition) Scopes(*reconcileEnv) ([]ReconcileScope, error) {
	out := make([]ReconcileScope, 0, s.scopes)
	for i := 0; i < s.scopes; i++ {
		out = append(out, ReconcileScope{Label: "cell-" + string(rune('a'+i))})
	}
	return out, nil
}

func (s slowScopesTransition) Check(ctx context.Context, _ *reconcileEnv, scope ReconcileScope) ReconcileCheck {
	if scope.Label == s.stallAt {
		<-ctx.Done()
		return ReconcileCheck{Status: ReconcileBlocked, Detail: ctx.Err().Error()}
	}
	time.Sleep(s.checkTime)
	// A real check reads its authority over RPC, which fails on an ended context.
	if err := ctx.Err(); err != nil {
		return ReconcileCheck{Status: ReconcileBlocked, Detail: err.Error()}
	}
	return ReconcileCheck{Status: ReconcilePending}
}

func shortReleaseTransitionStallWindow(t *testing.T, d time.Duration) {
	t.Helper()
	orig := releaseTransitionStallWindow
	t.Cleanup(func() { releaseTransitionStallWindow = orig })
	releaseTransitionStallWindow = d
}

// A reconciliation over more scopes than fit in its stall window completes while each scope makes progress.
func TestReleaseTransitionRunsPastStallWindowWhileScopesProgress(t *testing.T) {
	shortReleaseTransitionStallWindow(t, 80*time.Millisecond)
	cmd, _ := commandWithOutput()
	tr := slowScopesTransition{fakeTransition: fakeTransition{id: "slow"}, scopes: 6, checkTime: 30 * time.Millisecond}
	if err := runReleaseTransition(cmd, &reconcileEnv{operatorJWT: "jwt"}, tr, false); err != nil {
		t.Fatalf("reconciliation over 6 scopes of 30ms each under an 80ms stall window failed: %v", err)
	}
}

// A scope whose check hangs is cancelled after the stall window with an error naming the reconciliation and the last
// scope that made progress.
func TestReleaseTransitionCancelsStalledScope(t *testing.T) {
	shortReleaseTransitionStallWindow(t, 80*time.Millisecond)
	cmd, _ := commandWithOutput()
	tr := slowScopesTransition{fakeTransition: fakeTransition{id: "slow"}, scopes: 3, checkTime: time.Millisecond, stallAt: "cell-c"}
	err := runReleaseTransition(cmd, &reconcileEnv{operatorJWT: "jwt"}, tr, false)
	if err == nil || !strings.Contains(err.Error(), "reconciliation slow stalled") || !strings.Contains(err.Error(), "applied and verified cell-b") {
		t.Fatalf("err = %v, want a stall naming the reconciliation and its last progress", err)
	}
}
