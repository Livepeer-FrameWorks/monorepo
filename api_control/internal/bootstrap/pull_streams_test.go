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
	if _, _, err := validatePullStreamShape(ps); err != nil {
		t.Fatalf("validatePullStreamShape: %v", err)
	}

	ps.SourceURI = "https://example.com/live"
	if _, _, err := validatePullStreamShape(ps); err == nil {
		t.Fatal("expected source_uri validation error")
	}
}

func TestValidatePullStreamShapeReadsSourceLocation(t *testing.T) {
	ps := PullStream{
		PlaybackID:  "lan-camera",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "LAN camera",
		SourceURI:   "rtsp://10.0.0.5/live",
		SourceLocation: &SourceLocation{
			Clusters:     []SourceLocationCluster{{ClusterID: "warehouse-edge", NodeIDs: []string{"gw-2", "gw-1"}}, {ClusterID: "backup-edge"}},
			AvoidNodeIDs: []string{"gw-9"},
		},
	}
	_, location, err := validatePullStreamShape(ps)
	if err != nil {
		t.Fatalf("shape: %v", err)
	}
	if strings.Join(location.ClusterIDs(), ",") != "backup-edge,warehouse-edge" || strings.Join(location.Clusters[1].NodeIDs, ",") != "gw-1,gw-2" || strings.Join(location.AvoidNodeIDs, ",") != "gw-9" {
		t.Fatalf("canonical location = %+v", location)
	}

	ps.SourceLocation = &SourceLocation{}
	if _, _, err := validatePullStreamShape(ps); err == nil || !strings.Contains(err.Error(), "source location") {
		t.Fatalf("empty source_location accepted: %v", err)
	}
	ps.SourceLocation = &SourceLocation{Clusters: []SourceLocationCluster{{ClusterID: "warehouse-edge", NodeIDs: []string{"gw-1"}}}, AvoidNodeIDs: []string{"gw-1"}}
	if _, _, err := validatePullStreamShape(ps); err == nil || !strings.Contains(err.Error(), "both allowed and avoided") {
		t.Fatalf("node both allowed and avoided accepted: %v", err)
	}
}

func TestValidatePullStreamShapeRejectsLegacyAllowedClusterIDs(t *testing.T) {
	ps := validPullStream()
	empty := []string{}
	ps.LegacyAllowedClusterIDs = &empty
	_, _, err := validatePullStreamShape(ps)
	if err == nil || !strings.Contains(err.Error(), "allowed_cluster_ids") || !strings.Contains(err.Error(), "source_location") {
		t.Fatalf("legacy allowed_cluster_ids error = %v", err)
	}
}

// TestValidatePullStreamPlacement_PrivateRequiresConsentedClusters locks the
// per-source placement invariant. A private URI:
//   - without source_location clusters must fail (no implicit fallback)
//   - restricted to a non-opted-in cluster must fail (missing capability)
//   - restricted to an opted-in cluster must pass
func TestValidatePullStreamPlacement_PrivateRequiresConsentedClusters(t *testing.T) {
	ps := PullStream{
		PlaybackID:  "private-demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Private demo",
		SourceURI:   "tsudp://10.0.0.5:9000",
	}
	class, _, err := validatePullStreamShape(ps)
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

	if err := validatePullStreamPlacement(ps, class, nil, candidates); err == nil || !strings.Contains(err.Error(), "source_location") {
		t.Fatalf("private URI without source_location clusters must fail placement: %v", err)
	}
	if err := validatePullStreamPlacement(ps, class, []string{"demo-media"}, candidates); err == nil {
		t.Fatal("private URI restricted to cluster without capability must fail placement")
	}
	if err := validatePullStreamPlacement(ps, class, []string{"selfhost-edge"}, candidates); err != nil {
		t.Fatalf("private URI restricted to opted-in cluster should pass: %v", err)
	}
	if err := validatePullStreamPlacement(ps, class, []string{"ghost-cluster"}, candidates); err == nil {
		t.Fatal("unknown source_location cluster must fail placement")
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

// TestReconcilePullStreamRefusesPushToPullConversion locks the safety check that
// converting an existing push stream to pull is destructive (would orphan the
// stream key, change ingest semantics) so it errors.
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
	_, _, err = reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, nil, fakeCipher{})
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
	_, _, err = reconcilePullStream(context.Background(), db, tenantID, "acme", ps, nil, fakeCipher{})
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
	action, gotStreamID, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, nil, fakeCipher{})
	if err != nil {
		t.Fatalf("reconcilePullStream: %v", err)
	}
	if action != "noop" || gotStreamID != streamID {
		t.Fatalf("action = %q stream %q, want noop %q (encrypt/decrypt round-trip should match)", action, gotStreamID, streamID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// TestReconcilePullStreamNoopWithSameSourceLocationClusters verifies the
// idempotent compare covers the mirrored pin column: the same cluster set in
// the stored row and the declared source location ⇒ noop, no UPDATE issued.
func TestReconcilePullStreamNoopWithSameSourceLocationClusters(t *testing.T) {
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
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		SourceURI:   plaintextURI,
		Enabled:     true,
	}
	action, _, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, []string{"warehouse-edge"}, fakeCipher{})
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

// TestReconcilePullStreamUpdatesWhenSourceLocationClustersChange verifies a
// diff in the mirrored cluster set alone is enough to trigger the upsert.
func TestReconcilePullStreamUpdatesWhenSourceLocationClustersChange(t *testing.T) {
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
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		SourceURI:   plaintextURI,
		Enabled:     true,
	}
	action, _, err := reconcilePullStream(context.Background(), db, tenantID, "frameworks", ps, []string{"warehouse-edge"}, fakeCipher{})
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
