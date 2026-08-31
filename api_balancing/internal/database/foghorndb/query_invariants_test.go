package foghorndb

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestSourceProjectionRevisionAllocatorIsKeyScoped(t *testing.T) {
	query := strings.ToLower(nextSourceProjectionRevision)
	for _, required := range []string{"source_projection_revision_counter", "tenant_id", "stream_internal_name", "on conflict", "value + 1"} {
		if !strings.Contains(query, required) {
			t.Fatalf("source allocator must serialize at the tenant/stream comparison key; missing %q", required)
		}
	}
	if strings.Contains(query, "nextval") {
		t.Fatal("source correctness ordering must not depend on a session-cached sequence")
	}
}

func TestSourceProjectionRepairAllocatorRetriesSerializationFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("INSERT INTO foghorn.source_projection_revision_counter").
		WithArgs("10000000-0000-0000-0000-000000000001", "repair-stream", int64(41)).
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectQuery("INSERT INTO foghorn.source_projection_revision_counter").
		WithArgs("10000000-0000-0000-0000-000000000001", "repair-stream", int64(41)).
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(4503599627370497)))

	revision, err := AllocateSourceProjectionRevisionAfter(
		context.Background(), db,
		"10000000-0000-0000-0000-000000000001", "repair-stream", 41,
	)
	if err != nil || revision != 4503599627370497 {
		t.Fatalf("repair revision=%d err=%v", revision, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementClaimEnumerationIncludesPendingSessions(t *testing.T) {
	query := strings.ToLower(listActiveIngestSessionClaims)
	if strings.Contains(query, "projection_state") {
		t.Fatal("unended pending sessions must renew placement until promotion or the session reaper resolves them")
	}
	if !strings.Contains(query, "ended_at is null") {
		t.Fatal("placement renewal must remain scoped to unended sessions")
	}
}

func TestFederatedPointerShortRecoverySelectsOnlyExpiredClaims(t *testing.T) {
	query := strings.ToLower(listRecoverableFederatedArtifactPointerPurges)
	for _, required := range []string{
		"federated_purge_token is not null",
		"federated_purge_lease_until <= now()",
		"interrupted_active",
		"tombstone",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("short recovery selector is missing %q", required)
		}
	}
	if strings.Contains(query, "federated_purge_token is null\n      or") {
		t.Fatal("short recovery must not turn ordinary retention discovery into a 30-second full scan")
	}
}
