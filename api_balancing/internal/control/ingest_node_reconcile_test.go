package control

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestRuntimeNameMatchesStream(t *testing.T) {
	for _, tc := range []struct {
		runtime, internal string
		want              bool
	}{
		{"live+abc", "abc", true},
		{"abc", "abc", true},
		{"live+abcd", "abc", false},
		{"live+other", "abc", false},
		{"+abc", "abc", false},
		{"", "abc", false},
		{"live+abc", "", false},
	} {
		if got := runtimeNameMatchesStream(tc.runtime, tc.internal); got != tc.want {
			t.Errorf("runtimeNameMatchesStream(%q, %q) = %v, want %v", tc.runtime, tc.internal, got, tc.want)
		}
	}
}

func TestReconcileNodeIngestSessionsRequiresDatabaseAndScope(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })
	if _, err := ReconcileNodeIngestSessions(context.Background(), "node", nil, time.Now(), nil, nil, logging.NewLogger()); err == nil {
		t.Fatal("reconcile without a database must fail rather than report success")
	}
}
