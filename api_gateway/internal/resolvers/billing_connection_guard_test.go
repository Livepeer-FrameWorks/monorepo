package resolvers

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// DoGetInvoicesConnection has two pre-client branches that must hold regardless
// of downstream wiring: demo mode returns a fully-built synthetic connection,
// and a non-demo request without a tenant is rejected BEFORE the Purser client
// is dereferenced. The nil-Clients resolver makes the ordering assertion real —
// if the tenant guard regressed, the second test would panic instead of error.
func TestDoGetInvoicesConnection_DemoMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	r := &Resolver{Logger: logging.NewLogger()} // Clients deliberately nil

	conn, err := r.DoGetInvoicesConnection(ctx, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("demo mode returned error: %v", err)
	}
	if conn == nil {
		t.Fatal("demo mode returned nil connection")
	}
	if len(conn.Edges) != len(conn.Nodes) {
		t.Fatalf("edge/node parity broken: %d edges vs %d nodes", len(conn.Edges), len(conn.Nodes))
	}
	if len(conn.Edges) > 0 {
		if conn.PageInfo == nil || conn.PageInfo.StartCursor == nil || conn.PageInfo.EndCursor == nil {
			t.Fatal("expected start/end cursors when edges are present")
		}
	}
}

func TestDoGetInvoicesConnection_TenantGuardBeforeClient(t *testing.T) {
	r := &Resolver{Logger: logging.NewLogger()} // Clients nil: a deref would panic

	conn, err := r.DoGetInvoicesConnection(context.Background(), nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected tenant-context error for empty tenant, got nil")
	}
	if conn != nil {
		t.Fatalf("expected nil connection on guard failure, got %+v", conn)
	}
}
