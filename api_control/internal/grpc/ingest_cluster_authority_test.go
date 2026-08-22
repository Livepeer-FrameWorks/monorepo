package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"github.com/sirupsen/logrus"
)

var streamContextCols = []string{
	"id", "user_id", "tenant_id", "internal_name", "is_active",
	"is_recording_enabled", "playback_id", "ingest_mode", "requires_auth",
	"active_ingest_cluster_id", "lease_fresh",
}

func healthyPeer(clusterID string) *clusterpeerpb.TenantClusterPeer {
	return &clusterpeerpb.TenantClusterPeer{
		ClusterId:    clusterID,
		ClusterType:  "edge",
		HealthStatus: "healthy",
	}
}

// serverWithRoute builds a Commodore whose tenant route is already cached, so
// resolution needs no Quartermaster and no admission refresh.
func serverWithRoute(t *testing.T, db *sql.DB, route *clusterRoute) *CommodoreServer {
	t.Helper()
	route.resolvedAt = time.Now()
	route.admissionResolvedAt = time.Now()
	return &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		routeCache:    map[string]*clusterRoute{"tenant-id": route},
		routeCacheTTL: 5 * time.Minute,
		purserClient:  startPurserEntitlementFake(t, &purserEntitlementFake{}),
	}
}

func expectStreamKeyRow(mock sqlmock.Sqlmock, leasedCluster any, leaseFresh any) {
	mock.ExpectQuery(`ResolveStreamContextByIdentifier`).
		WithArgs(int64(activeIngestLease.Seconds()), "stream_key", "sk_live").
		WillReturnRows(sqlmock.NewRows(streamContextCols).
			AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk", "push", false, leasedCluster, leaseFresh))
}

func resolveByStreamKey(t *testing.T, s *CommodoreServer, clusterID string) *commodorepb.ResolveStreamContextResponse {
	t.Helper()
	resp, err := s.ResolveStreamContext(context.Background(), &commodorepb.ResolveStreamContextRequest{
		Identifier: &commodorepb.ResolveStreamContextRequest_StreamKey{StreamKey: "sk_live"},
		ClusterId:  clusterID,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return resp
}

// A resolve by stream key names no cluster, because the Foghorn asking serves
// several and cannot know which one the publish lands in. That must not mean
// the entitlement gate is skipped: with no cluster the tenant's plan permits
// at all, the publish is refused rather than resolved.
func TestResolveStreamContext_PublishIntentRejectsEmptyEntitlement(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, nil, false)

	// No cluster the tenant's plan permits.
	s := serverWithRoute(t, db, &clusterRoute{clusterID: "media-eu"})

	resp := resolveByStreamKey(t, s, "")
	if resp.GetAdmitted() {
		t.Fatal("a publish-intent resolve was admitted without an entitlement check")
	}
	if resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED {
		t.Fatalf("rejection reason = %v, want CLUSTER_NOT_ENTITLED", resp.GetRejectionReason())
	}
}

// An unclaimed publish is admitted on the ENVELOPE, not on one cluster: the
// caller ranks across every healthy authorized cluster, so admission asks the
// same question. A degraded default must not sink the publish while another
// authorized cluster is healthy.
func TestResolveStreamContext_PublishIntentAdmitsWhenAnyClusterIsHealthy(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, nil, false)

	degraded := healthyPeer("media-eu")
	degraded.HealthStatus = "degraded"
	s := serverWithRoute(t, db, &clusterRoute{
		// The tenant routes to the degraded cluster by default.
		clusterID:      "media-eu",
		admissionPeers: []*clusterpeerpb.TenantClusterPeer{degraded, healthyPeer("media-us")},
		clusterPeers:   []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-us")},
	})

	resp := resolveByStreamKey(t, s, "")
	if !resp.GetAdmitted() {
		t.Fatalf("publish rejected though a healthy cluster is authorized: %q", resp.GetAdmissionReason())
	}
	if len(resp.GetClusterPeers()) != 1 || resp.GetClusterPeers()[0].GetClusterId() != "media-us" {
		t.Fatalf("envelope should carry only the healthy cluster: %+v", resp.GetClusterPeers())
	}
}

// When nothing in the envelope is healthy, the publish is refused as a health
// problem rather than an entitlement one — the fleet is degraded, the plan is
// not at fault.
func TestResolveStreamContext_PublishIntentRejectsWhenNoClusterIsHealthy(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, nil, false)

	degraded := healthyPeer("media-eu")
	degraded.HealthStatus = "degraded"
	s := serverWithRoute(t, db, &clusterRoute{
		clusterID:      "media-eu",
		admissionPeers: []*clusterpeerpb.TenantClusterPeer{degraded},
	})

	resp := resolveByStreamKey(t, s, "")
	if resp.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY {
		t.Fatalf("rejection reason = %v, want CLUSTER_UNHEALTHY", resp.GetRejectionReason())
	}
}

// While a publisher holds a fresh ingest lease, that cluster is where the
// stream is. Routing a reconnect to the tenant's default instead would hand
// them an endpoint PUSH_REWRITE refuses as a duplicate.
func TestResolveStreamContext_FreshLeaseOutranksTenantRoute(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, "media-us", true)

	s := serverWithRoute(t, db, &clusterRoute{
		clusterID:      "media-eu",
		admissionPeers: []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
		clusterPeers:   []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
	})

	resp := resolveByStreamKey(t, s, "")
	if !resp.GetAdmitted() {
		t.Fatalf("expected admitted, got %q", resp.GetAdmissionReason())
	}
	if got := resp.GetOriginClusterId(); got != "media-us" {
		t.Fatalf("origin = %q, want the leased cluster media-us", got)
	}
}

// An expired lease is not placement. Once it lapses the tenant's route decides
// again, or a stream would be pinned to wherever it last published.
func TestResolveStreamContext_StaleLeaseFallsBackToTenantRoute(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, "media-us", false)

	s := serverWithRoute(t, db, &clusterRoute{
		clusterID:      "media-eu",
		admissionPeers: []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
		clusterPeers:   []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
	})

	if got := resolveByStreamKey(t, s, "").GetOriginClusterId(); got != "media-eu" {
		t.Fatalf("origin = %q, want the tenant route media-eu", got)
	}
}

// A caller that names a cluster is speaking for the cluster it runs and knows
// better than a recorded column. The lease must not override it, or every
// Foghorn trigger path would be redirected by a stale claim.
func TestResolveStreamContext_DeclaredClusterOutranksLease(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	expectStreamKeyRow(mock, "media-us", true)

	s := serverWithRoute(t, db, &clusterRoute{
		clusterID:      "media-eu",
		admissionPeers: []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
		clusterPeers:   []*clusterpeerpb.TenantClusterPeer{healthyPeer("media-eu"), healthyPeer("media-us")},
	})

	if got := resolveByStreamKey(t, s, "media-eu").GetOriginClusterId(); got != "media-eu" {
		t.Fatalf("origin = %q, want the declared cluster media-eu", got)
	}
}

// Only a resolve by stream key is publish intent. The managed-stream and
// trigger identifiers that name no cluster keep skipping the gate and relying
// on placement scoping, exactly as before.
func TestResolveStreamContext_NonPublishIntentStillSkipsClusterGate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(`ResolveStreamContextByIdentifier`).
		WithArgs(int64(activeIngestLease.Seconds()), "internal_name", "internal").
		WillReturnRows(sqlmock.NewRows(streamContextCols).
			AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk", "mist_native", false, nil, false))

	// No admission peer for the routed cluster: a gate would reject.
	s := serverWithRoute(t, db, &clusterRoute{clusterID: "media-eu"})

	resp, err := s.ResolveStreamContext(context.Background(), &commodorepb.ResolveStreamContextRequest{
		Identifier: &commodorepb.ResolveStreamContextRequest_InternalName{InternalName: "internal"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resp.GetAdmitted() {
		t.Fatalf("undeclared non-publish resolve was gated: %q", resp.GetAdmissionReason())
	}
}
