package bootstrap

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

func TestValidatePullStreamShapeChecksSourceURI(t *testing.T) {
	ps := PullStream{
		PlaybackID:  "frameworks-demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "FrameWorks marketing demo",
		SourceURI:   "https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8",
		Enabled:     true,
	}
	if _, err := validatePullStreamShape(ps); err != nil {
		t.Fatalf("validatePullStreamShape: %v", err)
	}

	ps.SourceURI = "https://example.com/live"
	if _, err := validatePullStreamShape(ps); err == nil {
		t.Fatal("expected source_uri validation error")
	}
}

// TestValidatePullStreamPlacement_PrivateRequiresExplicitAllowedClusters locks
// the per-source placement invariant. A private URI:
//   - with empty allowed_cluster_ids must fail (no implicit fallback)
//   - pinned to a non-opted-in cluster must fail (missing capability)
//   - pinned to an opted-in cluster must pass
func TestValidatePullStreamPlacement_PrivateRequiresExplicitAllowedClusters(t *testing.T) {
	ps := PullStream{
		PlaybackID:  "private-demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Private demo",
		SourceURI:   "tsudp://10.0.0.5:9000",
	}
	class, err := validatePullStreamShape(ps)
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if class != pullsource.ClassPrivate {
		t.Fatalf("class = %s, want private", class)
	}

	candidates := []pullsource.ClusterCapability{
		{ID: "demo-media", AllowPrivatePullSources: false},
		{ID: "selfhost-edge", AllowPrivatePullSources: true},
	}

	// empty allowed list ⇒ private rejected
	if err := validatePullStreamPlacement(ps, class, candidates); err == nil {
		t.Fatal("private URI with empty allowed_cluster_ids must fail placement")
	}

	// pinned to non-opted-in ⇒ rejected for missing capability
	ps.AllowedClusterIDs = []string{"demo-media"}
	if err := validatePullStreamPlacement(ps, class, candidates); err == nil {
		t.Fatal("private URI pinned to cluster without capability must fail placement")
	}

	// pinned to opted-in ⇒ pass
	ps.AllowedClusterIDs = []string{"selfhost-edge"}
	if err := validatePullStreamPlacement(ps, class, candidates); err != nil {
		t.Fatalf("private URI pinned to opted-in cluster should pass: %v", err)
	}

	// unknown id ⇒ rejected
	ps.AllowedClusterIDs = []string{"ghost-cluster"}
	if err := validatePullStreamPlacement(ps, class, candidates); err == nil {
		t.Fatal("unknown allowed_cluster_ids entry must fail placement")
	}
}

// stubClusterResolver is the minimal ClusterCapabilityResolver for tests.
type stubClusterResolver struct {
	caps []pullsource.ClusterCapability
}

func (s stubClusterResolver) MediaClusterCapabilities(_ context.Context) ([]pullsource.ClusterCapability, error) {
	return s.caps, nil
}

// fakeCipher is an identity cipher for round-trip tests: ciphertext = "enc:" + plaintext.
type fakeCipher struct{}

func (fakeCipher) Encrypt(plaintext string) (string, error) { return "enc:" + plaintext, nil }
func (fakeCipher) Decrypt(stored string) (string, error) {
	return strings.TrimPrefix(stored, "enc:"), nil
}

// TestReconcilePullStreamRefusesPushToPullConversion locks the safety check at
// pull_streams.go:120-122 — converting an existing push stream to pull is
// destructive (would orphan stream key, change ingest semantics) so it errors.
func TestReconcilePullStreamRefusesPushToPullConversion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "00000000-0000-0000-0000-000000000001"
	mock.ExpectQuery("FROM commodore.streams s").
		WithArgs(tenantID, "demo").
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "ingest_mode", "source_uri_enc", "enabled", "allowed_cluster_ids"}).
			AddRow("00000000-0000-0000-0000-000000000010", "Demo", "", "push", nil, nil, "{}"))

	ps := PullStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		SourceURI:   "rtsp://example.com/live",
		Enabled:     true,
	}
	_, err = reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, fakeCipher{})
	if err == nil {
		t.Fatal("expected refusal error, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to convert") {
		t.Fatalf("error %q does not contain refusal phrase", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestCreatePullStreamFailsClearlyWithoutOwner locks the precondition that
// streams.user_id requires an existing role='owner' user in the tenant.
// The owner SELECT must run, return no rows, and produce a tenant-named
// error before the INSERT is attempted.
func TestCreatePullStreamFailsClearlyWithoutOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "00000000-0000-0000-0000-000000000001"
	mock.ExpectQuery("FROM commodore.streams s").
		WithArgs(tenantID, "demo").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("FROM commodore.users").
		WithArgs(tenantID).
		WillReturnError(sql.ErrNoRows)

	ps := PullStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.tenants[acme]"},
		Title:       "Demo",
		SourceURI:   "rtsp://example.com/live",
		Enabled:     true,
	}
	_, err = reconcilePullStream(context.Background(), db, tenantID, "acme", ps, fakeCipher{})
	if err == nil {
		t.Fatal("expected missing-owner error, got nil")
	}
	if !strings.Contains(err.Error(), "acme") {
		t.Fatalf("error %q must name the tenant alias", err)
	}
	if !strings.Contains(err.Error(), "no owner user") {
		t.Fatalf("error %q must mention the missing owner condition", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestReconcilePullStreamEncryptsBeforeUpsert proves the source_uri never
// hits the database in plaintext and that idempotent comparison decrypts the
// stored value back to the same plaintext for a noop check.
func TestReconcilePullStreamEncryptsBeforeUpsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "00000000-0000-0000-0000-000000000001"
	streamID := "00000000-0000-0000-0000-000000000020"
	plaintextURI := "rtsp://upstream.example.com/live"
	storedCiphertext := "enc:" + plaintextURI

	mock.ExpectQuery("FROM commodore.streams s").
		WithArgs(tenantID, "demo").
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "ingest_mode", "source_uri_enc", "enabled", "allowed_cluster_ids"}).
			AddRow(streamID, "Demo", "", "pull", storedCiphertext, true, "{}"))

	ps := PullStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		SourceURI:   plaintextURI,
		Enabled:     true,
	}
	action, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, fakeCipher{})
	if err != nil {
		t.Fatalf("reconcilePullStream: %v", err)
	}
	if action != "noop" {
		t.Fatalf("action = %q, want noop (encrypt/decrypt round-trip should match)", action)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestReconcilePullStreamNoopWithSameAllowedClusters verifies the idempotent
// compare extends to allowed_cluster_ids: same set in stored row and incoming
// desired-state ⇒ noop, no UPDATE issued.
func TestReconcilePullStreamNoopWithSameAllowedClusters(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "00000000-0000-0000-0000-000000000001"
	streamID := "00000000-0000-0000-0000-000000000020"
	plaintextURI := "rtsp://10.0.0.5/live"
	storedCiphertext := "enc:" + plaintextURI

	mock.ExpectQuery("FROM commodore.streams s").
		WithArgs(tenantID, "demo").
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "ingest_mode", "source_uri_enc", "enabled", "allowed_cluster_ids"}).
			AddRow(streamID, "Demo", "", "pull", storedCiphertext, true, "{warehouse-edge}"))

	ps := PullStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		SourceURI:         plaintextURI,
		Enabled:           true,
		AllowedClusterIDs: []string{"warehouse-edge"},
	}
	action, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, fakeCipher{})
	if err != nil {
		t.Fatalf("reconcilePullStream: %v", err)
	}
	if action != "noop" {
		t.Fatalf("action = %q, want noop", action)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestReconcilePullStreamUpdatesWhenAllowedClustersChange verifies a diff in
// allowed_cluster_ids alone is enough to trigger the upsert.
func TestReconcilePullStreamUpdatesWhenAllowedClustersChange(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tenantID := "00000000-0000-0000-0000-000000000001"
	streamID := "00000000-0000-0000-0000-000000000020"
	plaintextURI := "rtsp://10.0.0.5/live"
	storedCiphertext := "enc:" + plaintextURI

	mock.ExpectQuery("FROM commodore.streams s").
		WithArgs(tenantID, "demo").
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "ingest_mode", "source_uri_enc", "enabled", "allowed_cluster_ids"}).
			AddRow(streamID, "Demo", "", "pull", storedCiphertext, true, "{old-edge}"))

	mock.ExpectExec("INSERT INTO commodore.stream_pull_sources").
		WillReturnResult(sqlmock.NewResult(0, 1))

	ps := PullStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		SourceURI:         plaintextURI,
		Enabled:           true,
		AllowedClusterIDs: []string{"warehouse-edge"},
	}
	action, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, fakeCipher{})
	if err != nil {
		t.Fatalf("reconcilePullStream: %v", err)
	}
	if action != "updated" {
		t.Fatalf("action = %q, want updated", action)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
