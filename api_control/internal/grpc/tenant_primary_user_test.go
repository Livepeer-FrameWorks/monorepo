package grpc

import (
	"context"
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

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, email, first_name, last_name
		FROM commodore.users
		WHERE tenant_id = $1 AND is_active = true AND email IS NOT NULL AND email <> ''
		ORDER BY
			CASE role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
			created_at ASC
		LIMIT 1
	`)).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "first_name", "last_name"}).
			AddRow("user-owner", "info@frameworks.network", "FrameWorks", "Operator"))

	server := &CommodoreServer{db: db, logger: logging.NewLogger()}
	resp, err := server.GetTenantPrimaryUser(context.Background(), &commodorepb.GetTenantPrimaryUserRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetTenantPrimaryUser: %v", err)
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
