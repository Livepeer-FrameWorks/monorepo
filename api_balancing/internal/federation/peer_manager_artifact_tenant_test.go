package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestNewDBArtifactTenantResolver_BatchResolves(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`SELECT artifact_hash, tenant_id::text.*FROM foghorn.artifacts.*WHERE artifact_hash = ANY`).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "tenant_id"}).
			AddRow("hash-1", "tenant-a").
			AddRow("hash-2", "tenant-b"))

	resolver := NewDBArtifactTenantResolver(db)
	tenants, err := resolver(context.Background(), []string{"hash-1", "hash-2", "hash-3"})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if tenants["hash-1"] != "tenant-a" || tenants["hash-2"] != "tenant-b" {
		t.Fatalf("unexpected tenants: %v", tenants)
	}
	if _, ok := tenants["hash-3"]; ok {
		t.Fatal("hash-3 has no registry tenant and must stay unresolved")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestNewDBArtifactTenantResolver_EmptyInputSkipsQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	resolver := NewDBArtifactTenantResolver(db)
	tenants, err := resolver(context.Background(), nil)
	if err != nil || tenants != nil {
		t.Fatalf("expected nil,nil for empty input, got %v,%v", tenants, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveArtifactTenants_NilResolverAndError(t *testing.T) {
	pm := &PeerManager{logger: logging.NewLogger()}
	if got := pm.resolveArtifactTenants([]string{"h"}); got != nil {
		t.Fatalf("nil resolver should yield nil map, got %v", got)
	}

	pm.artifactTenantResolver = func(ctx context.Context, hashes []string) (map[string]string, error) {
		return nil, errors.New("db down")
	}
	if got := pm.resolveArtifactTenants([]string{"h"}); got != nil {
		t.Fatalf("failed lookup should yield nil map, got %v", got)
	}
}

func TestShouldLogUnresolvedAd_ThrottlesPerHash(t *testing.T) {
	pm := &PeerManager{unresolvedAdLogged: map[string]time.Time{}}

	if !pm.shouldLogUnresolvedAd("hash-1") {
		t.Fatal("first unresolved skip must log")
	}
	if pm.shouldLogUnresolvedAd("hash-1") {
		t.Fatal("repeat within the interval must not log")
	}
	if !pm.shouldLogUnresolvedAd("hash-2") {
		t.Fatal("a different hash must log")
	}

	// A stale entry past the interval logs again and is pruned, not leaked.
	pm.unresolvedAdLogged["hash-1"] = time.Now().Add(-2 * time.Hour)
	if !pm.shouldLogUnresolvedAd("hash-1") {
		t.Fatal("a hash past the interval must log again")
	}
}
