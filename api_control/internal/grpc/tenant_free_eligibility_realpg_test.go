//go:build schema_verify

package grpc

import (
	"database/sql"
	"frameworks/api_control/internal/database/commodoredb"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestTenantFreeEligibility_RealPG(t *testing.T) {
	testTenantFreeEligibility(t, startCommodoreRealPG(t))
}
func TestTenantFreeEligibility_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "tenant_free_eligibility", "commodore")
	if !ok {
		t.Skip("requires shared Yugabyte fixture")
	}
	testTenantFreeEligibility(t, db)
}
func testTenantFreeEligibility(t *testing.T, db *sql.DB) {
	t.Helper()
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), freeTierResources: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {}}}}
	other := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO commodore.users(id,tenant_id,email,role,is_active,verified) VALUES($1,$2::uuid,'other-owner@example.test','owner',true,true)`, uuid.NewString(), other); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, role             string
		active, verified, want bool
	}{
		{"verified owner", "owner", true, true, true},
		{"verified admin", "admin", true, true, true},
		{"unverified owner", "owner", true, false, false},
		{"verified member", "member", true, true, false},
		{"inactive owner", "owner", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenant := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO commodore.users(id,tenant_id,email,role,is_active,verified) VALUES($1,$2::uuid,(($2::uuid)::text||'@primary.example.test'),$3,$4,$5),($6,$2::uuid,(($2::uuid)::text||'@member.example.test'),'member',true,false)`, uuid.NewString(), tenant, tc.role, tc.active, tc.verified, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			got, err := server.GetTenantPrimaryUser(serviceCtx(), &commodorepb.GetTenantPrimaryUserRequest{TenantId: tenant})
			if err != nil || got.GetFreeTierEligible() != tc.want {
				t.Fatalf("tenant-scoped Free eligibility=%v err=%v want=%v", got.GetFreeTierEligible(), err, tc.want)
			}
			proof, proofErr := server.GetTenantFreeEligibility(serviceCtx(), &commodorepb.GetTenantFreeEligibilityRequest{TenantId: tenant, StorageLimitBytes: 10 * (1 << 30), RetentionLimitDays: 30})
			if proofErr != nil || proof.GetEligible() != tc.want {
				t.Fatalf("Free resource/identity RPC=%+v err=%v", proof, proofErr)
			}

		})
	}
}

func TestFreeEligibilityOwnerCellsAndPendingCreation_RealPG(t *testing.T) {
	testFreeEligibilityOwnerCells(t, startCommodoreRealPG(t))
}
func TestFreeEligibilityOwnerCellsAndPendingCreation_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "free_owner_cells", "commodore")
	if !ok {
		t.Skip("requires shared Yugabyte fixture")
	}
	testFreeEligibilityOwnerCells(t, db)
}
func testFreeEligibilityOwnerCells(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, other := uuid.NewString(), uuid.NewString()
	for _, row := range []struct{ tenant, cell, hash string }{{tenant, "old-us", "own"}, {other, "other-cell", "foreign"}} {
		if _, err := db.Exec(`INSERT INTO commodore.artifact_creation_intents(tenant_id,kind,artifact_hash,request_id,origin_cluster_id,status) VALUES($1,'vod',$2,$3,$4,'pending')`, row.tenant, row.hash, uuid.NewString(), row.cell); err != nil {
			t.Fatal(err)
		}
	}
	q := commodoredb.New(db)
	cells, err := q.ListTenantMediaOwnerCells(t.Context(), tenant)
	if err != nil || len(cells) != 1 || cells[0] != "old-us" {
		t.Fatalf("owner discovery crossed tenant or lost historical origin: %v %v", cells, err)
	}
	pending, err := q.HasPendingTenantMediaCreation(t.Context(), tenant)
	if err != nil || !pending {
		t.Fatalf("pending creation hidden: %v %v", pending, err)
	}
	if _, err := db.Exec(`INSERT INTO commodore.users(id,tenant_id,email,role,is_active,verified) VALUES($1,$2,'pending-free@example.test','owner',true,true)`, uuid.NewString(), tenant); err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), freeTierResources: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {}}}}
	proof, err := server.GetTenantFreeEligibility(serviceCtx(), &commodorepb.GetTenantFreeEligibilityRequest{TenantId: tenant, StorageLimitBytes: 10 * (1 << 30), RetentionLimitDays: 30})
	if err != nil || proof.GetEligible() || proof.GetReason() != "MEDIA_WORK_PENDING" {
		t.Fatalf("pending accepted work became Free: %+v %v", proof, err)
	}
}
