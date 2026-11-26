package grpc

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestGetTenantPrimaryUserPrioritizesOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta(`-- name: GetTenantPrimaryUser :one`)).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "first_name", "last_name", "free_tier_eligible"}).
			AddRow("user-owner", "info@frameworks.network", "FrameWorks", "Operator", true))

	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	resp, err := server.GetTenantPrimaryUser(serviceCtx(), &commodorepb.GetTenantPrimaryUserRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantPrimaryUser: %v", err)
	}
	if !resp.GetFreeTierEligible() {
		t.Fatal("verified billing identity eligibility was lost")
	}
	if resp.GetEmail() != "info@frameworks.network" {
		t.Fatalf("email = %q, want info@frameworks.network", resp.GetEmail())
	}
	if resp.GetName() != "FrameWorks Operator" {
		t.Fatalf("name = %q, want FrameWorks Operator", resp.GetName())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
