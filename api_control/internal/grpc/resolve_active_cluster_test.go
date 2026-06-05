package grpc

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
)

// preCachedRoute installs a clusterRoute in the server's routeCache so
// resolveClusterRouteForTenant does not need a quartermaster client.
func preCachedRoute(s *CommodoreServer, tenantID, routeClusterID string) {
	s.routeCacheTTL = time.Hour
	s.routeCache[tenantID] = &clusterRoute{
		clusterID: routeClusterID,
		clusterPeers: []*quartermasterpb.TenantClusterPeer{
			{ClusterId: routeClusterID, RegionId: "eu-west"},
		},
		resolvedAt: time.Now(),
	}
}

func newResolverTestServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	srv := &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		routeCache:    make(map[string]*clusterRoute),
		routeCacheTTL: time.Hour,
	}
	return srv, mock, func() { db.Close() }
}

// TestResolvePlaybackID_ActiveIngestClusterOverridesOriginCluster asserts
// that when commodore.streams.active_ingest_cluster_id is non-NULL it
// overrides the tenant-routed origin_cluster_id in the response, so
// PLAY_REWRITE / federation route to the actual active source.
func TestResolvePlaybackID_ActiveIngestClusterOverridesOriginCluster(t *testing.T) {
	srv, mock, done := newResolverTestServer(t)
	defer done()
	preCachedRoute(srv, "tenant-1", "media-eu-1")

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT id, internal_name, tenant_id, requires_auth, ingest_mode, active_ingest_cluster_id
			FROM commodore.streams WHERE playback_id = $1`)).
		WithArgs("pb-managed").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "internal_name", "tenant_id", "requires_auth", "ingest_mode", "active_ingest_cluster_id",
		}).AddRow("stream-1", "internal-1", "tenant-1", false, "mist_native", "media-us-1"))

	resp, err := srv.ResolvePlaybackID(context.Background(), &commodorepb.ResolvePlaybackIDRequest{PlaybackId: "pb-managed"})
	if err != nil {
		t.Fatalf("ResolvePlaybackID: %v", err)
	}
	if got := resp.GetOriginClusterId(); got != "media-us-1" {
		t.Fatalf("active_ingest_cluster_id must override route default; got origin=%q want media-us-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestResolvePlaybackID_NullActiveClusterFallsBackToRoute pins the inverse:
// when active_ingest_cluster_id is NULL the response falls back to the
// tenant-routed cluster (the pre-managed-streams behavior).
func TestResolvePlaybackID_NullActiveClusterFallsBackToRoute(t *testing.T) {
	srv, mock, done := newResolverTestServer(t)
	defer done()
	preCachedRoute(srv, "tenant-1", "media-eu-1")

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT id, internal_name, tenant_id, requires_auth, ingest_mode, active_ingest_cluster_id
			FROM commodore.streams WHERE playback_id = $1`)).
		WithArgs("pb-push").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "internal_name", "tenant_id", "requires_auth", "ingest_mode", "active_ingest_cluster_id",
		}).AddRow("stream-2", "internal-2", "tenant-1", false, "push", nil))

	resp, err := srv.ResolvePlaybackID(context.Background(), &commodorepb.ResolvePlaybackIDRequest{PlaybackId: "pb-push"})
	if err != nil {
		t.Fatalf("ResolvePlaybackID: %v", err)
	}
	if got := resp.GetOriginClusterId(); got != "media-eu-1" {
		t.Fatalf("NULL active_ingest_cluster_id must use route default; got origin=%q want media-eu-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

// TestResolveInternalName_ActiveIngestClusterOverridesOriginCluster mirrors
// the playback-id assertion for the internal-name resolver, which Decklog
// uses for event enrichment and which feeds downstream attribution.
func TestResolveInternalName_ActiveIngestClusterOverridesOriginCluster(t *testing.T) {
	srv, mock, done := newResolverTestServer(t)
	defer done()
	preCachedRoute(srv, "tenant-1", "media-eu-1")

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, tenant_id, user_id, is_recording_enabled, requires_auth, active_ingest_cluster_id
		FROM commodore.streams WHERE internal_name = $1`)).
		WithArgs("internal-managed").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "user_id", "is_recording_enabled", "requires_auth", "active_ingest_cluster_id",
		}).AddRow("stream-1", "tenant-1", "user-1", false, false, "media-us-1"))

	resp, err := srv.ResolveInternalName(context.Background(), &commodorepb.ResolveInternalNameRequest{InternalName: "internal-managed"})
	if err != nil {
		t.Fatalf("ResolveInternalName: %v", err)
	}
	if got := resp.GetOriginClusterId(); got != "media-us-1" {
		t.Fatalf("active_ingest_cluster_id must override route default; got origin=%q want media-us-1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}
