//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
)

// testRetiredIngestCellSettlesStop drives a closure stop owed to a cell that
// no longer exists: it stays pending and visible until an operator retires the
// cell, then settles on its next attempt, and a repeated retirement keeps the
// first record.
func testRetiredIngestCellSettlesStop(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tenant, user, stream, cell := uuid.NewString(), uuid.NewString(), uuid.NewString(), "removed-"+uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active) VALUES($1,$2,$3,true)`, user, tenant, user+"@retire.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,internal_name,stream_key,playback_id,title,last_ingest_cluster_id) VALUES($1,$2,$3,$4::text,$4::text,$4::text,'Removed cell','`+cell+`')`, stream, tenant, user, "retire-"+stream); err != nil {
		t.Fatal(err)
	}
	queries := commodoredb.New(db)
	target := commodoredb.TenantMediaAuthorityTarget(tenant)
	if err := queries.EnqueueMediaAuthorityObligation(ctx, commodoredb.EnqueueMediaAuthorityObligationParams{
		Lane: commodoredb.MediaAuthorityLaneIngestRevocation, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenant,
		Reason: sharedauthority.ReasonAccountInactive, SourceService: "commodore", SourceEventID: "account-closed-stop:" + tenant,
		NextAttemptAt: sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	registry := &retirementRegistry{instances: []*quartermasterpb.ServiceInstance{}}
	server, _ := newRetirementServer(t, registry, db)
	op := operatorContext(tenant)
	row := commodoredb.ClaimMediaAuthorityObligationsRow{Lane: commodoredb.MediaAuthorityLaneIngestRevocation, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenant}

	if err := server.compileMediaAuthorityObligation(ctx, row); err == nil || !strings.Contains(err.Error(), cell) {
		t.Fatalf("a stop owed to an unretired removed cell settled: %v", err)
	}
	state, err := server.AdminGetTenantAccountState(op, &commodorepb.AdminGetTenantAccountStateRequest{TenantId: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GetPendingIngestStops()) != 1 || strings.Join(state.GetPendingIngestStops()[0].GetCells(), ",") != cell || len(state.GetPendingIngestStops()[0].GetRetiredCells()) != 0 {
		t.Fatalf("pending stop before retirement=%v", state.GetPendingIngestStops())
	}

	retired, err := server.AdminRetireIngestCell(op, &commodorepb.AdminRetireIngestCellRequest{ClusterId: cell, Reason: "cell decommissioned"})
	if err != nil {
		t.Fatal(err)
	}
	if retired.GetAlreadyRetired() || retired.GetPendingIngestStops() != 1 || retired.GetReason() != "cell decommissioned" {
		t.Fatalf("retirement=%v", retired)
	}
	var due bool
	if err = db.QueryRowContext(ctx, `SELECT next_attempt_at <= NOW() FROM commodore.media_authority_refresh_obligations WHERE target_key=$1 AND lane=$2`, target.Key, row.Lane).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("retirement left the stop waiting out its backoff")
	}
	state, err = server.AdminGetTenantAccountState(op, &commodorepb.AdminGetTenantAccountStateRequest{TenantId: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if got := state.GetPendingIngestStops(); len(got) != 1 || strings.Join(got[0].GetRetiredCells(), ",") != cell {
		t.Fatalf("pending stop after retirement=%v", got)
	}
	if err = server.compileMediaAuthorityObligation(ctx, row); err != nil {
		t.Fatalf("a stop owed only to a retired removed cell did not settle: %v", err)
	}

	again, err := server.AdminRetireIngestCell(op, &commodorepb.AdminRetireIngestCellRequest{ClusterId: cell, Reason: "second attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.GetAlreadyRetired() || again.GetReason() != "cell decommissioned" || !again.GetRetiredAt().AsTime().Equal(retired.GetRetiredAt().AsTime()) {
		t.Fatalf("a repeated retirement replaced the first record: %v", again)
	}
}
