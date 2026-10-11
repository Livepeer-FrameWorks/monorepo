//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A Foghorn that observes its cell become ready reports it, and the legacy
// tenants targeting that cell are queued for activation at once rather than on
// the cell's next acknowledgement. Commodore records what the named cell itself
// answers, so a report cannot attest for a cell.
func TestReportCapability_ReadyEnqueuesLegacyTenants_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	now := time.Now().UTC()
	const legacyTenant = "42000000-0000-4000-8000-000000000001"
	if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: legacyTenant, AuthorityVersion: 1, PayloadSchemaVersion: 1, Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"), IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: legacyTenant, AuthorityVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{AuthorityKind: "tenant", AuthorityID: legacyTenant, CellID: "cell-a", AuthorityVersion: 1}); err != nil {
		t.Fatal(err)
	}
	cell := &capabilityCellFake{capability: &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 2}}
	server := newCapabilityProbeServer(t, db, map[string]*capabilityCellFake{"cell-a": cell})
	queued := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_obligations WHERE last_reason = $1 AND tenant_id = $2", cellPlacementCapabilityRefreshReason, legacyTenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	report := &commodorepb.ReportMediaCellPlacementCapabilityRequest{ControlCellId: "cell-a", Capability: readyCellAttestation()}

	if _, err := server.ReportMediaCellPlacementCapability(ctx, report); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthenticated report returned %v, want PermissionDenied", err)
	}
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")

	// The report claims readiness the cell does not attest: the cell's own answer is recorded.
	if _, err := server.ReportMediaCellPlacementCapability(serviceCtx, report); err != nil {
		t.Fatal(err)
	}
	if ready, err := placementCellsReady(ctx, q, []string{"cell-a"}); err != nil || ready || queued() != 0 {
		t.Fatalf("report attested for the cell: ready=%t err=%v queued=%d", ready, err, queued())
	}

	cell.capability = readyCellAttestation()
	if _, err := server.ReportMediaCellPlacementCapability(serviceCtx, report); err != nil {
		t.Fatal(err)
	}
	if ready, err := placementCellsReady(ctx, q, []string{"cell-a"}); err != nil || !ready {
		t.Fatalf("ready cell not recorded: %t %v", ready, err)
	}
	if n := queued(); n != 1 {
		t.Fatalf("newly ready cell queued %d activation(s) for its legacy tenant, want 1", n)
	}

	// A cell nobody can reach is not recorded, and the reporter is told to retry.
	unknown := &commodorepb.ReportMediaCellPlacementCapabilityRequest{ControlCellId: "cell-unknown", Capability: readyCellAttestation()}
	if _, err := server.ReportMediaCellPlacementCapability(serviceCtx, unknown); status.Code(err) != codes.Unavailable {
		t.Fatalf("report for an unreachable cell returned %v, want Unavailable", err)
	}
	if ready, err := placementCellsReady(ctx, q, []string{"cell-unknown"}); err != nil || ready {
		t.Fatalf("unreachable cell recorded as ready: %t %v", ready, err)
	}

	// A cell's Foghorn without the probe never surfaces as Unimplemented, which
	// the reporter reads as a Commodore without this RPC.
	cell.unimplemented = true
	if _, err := server.ReportMediaCellPlacementCapability(serviceCtx, report); status.Code(err) != codes.Unavailable {
		t.Fatalf("report whose read-back was unimplemented returned %v, want Unavailable", err)
	}
}
