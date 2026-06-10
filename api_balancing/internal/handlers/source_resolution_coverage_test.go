package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"

	"github.com/DATA-DOG/go-sqlmock"
	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// withLoggerSourceRes guarantees the package-global logger is non-nil. Like the
// get_source helper it never restores to nil: the fire-and-forget
// postBalancingEvent goroutine outlives the test and would panic on a naked
// nil-restore.
func withLoggerSourceRes(t *testing.T) {
	t.Helper()
	if logger == nil {
		logger = logging.NewLogger()
	}
}

// pullSourceFakeSourceRes is an in-process Commodore InternalService double whose
// only exercised RPC is ResolvePullSourceByInternalName. The handlers' pull
// resolution path (resolvePullSourceForSource) dials this through the real
// concrete *commodore.GRPCClient.
type pullSourceFakeSourceRes struct {
	commodorepb.UnimplementedInternalServiceServer
	resolvePull func(context.Context, *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error)
}

func (f *pullSourceFakeSourceRes) ResolvePullSourceByInternalName(ctx context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
	if f.resolvePull != nil {
		return f.resolvePull(ctx, req)
	}
	return &commodorepb.ResolvePullSourceByInternalNameResponse{}, nil
}

// startPullCommodoreSourceRes serves the fake on a localhost gRPC listener,
// builds a real *commodore.GRPCClient against it, and points
// control.CommodoreClient (the global resolvePullSourceForSource reads) at it.
// Everything is restored on cleanup.
func startPullCommodoreSourceRes(t *testing.T, fake *pullSourceFakeSourceRes) {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	commodorepb.RegisterInternalServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()

	client, err := commodorecli.NewGRPCClient(commodorecli.GRPCConfig{
		GRPCAddr:      lis.Addr().String(),
		AllowInsecure: true,
		Logger:        logging.NewLogger(),
		Timeout:       5 * time.Second,
	})
	if err != nil {
		srv.Stop()
		_ = lis.Close()
		t.Fatalf("commodore client: %v", err)
	}

	prev := control.CommodoreClient
	control.CommodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prev
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// newSourceCtxSourceRes builds a gin context modelling a Mist /source lookup for
// the given query string.
func newSourceCtxSourceRes(rawQuery string) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	raw := "/"
	if rawQuery != "" {
		raw = "/?" + rawQuery
	}
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", raw, nil)
	return c, w
}

// Invariant: handleGetPullSource returns the configured upstream origin URI
// verbatim when (a) Commodore resolves an enabled pull source, (b) the upstream
// classifies non-blocked, and (c) this cluster passes the placement filter
// (public class + no allowed_cluster_ids pin ⇒ the local cluster is eligible).
// This is the origin-pull leg of source resolution: a placeable public upstream
// is handed straight back to the requesting Mist edge.
func TestHandleGetPullSourceUpstreamReturnedWhenPlaceable(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	t.Setenv("CLUSTER_ID", "cluster-local")

	const upstream = "https://origin.example.com/live/master.m3u8"
	startPullCommodoreSourceRes(t, &pullSourceFakeSourceRes{
		resolvePull: func(_ context.Context, req *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			if req.GetInternalName() != "cam1" {
				t.Errorf("ResolvePullSource got %q, want cam1 (pull+ prefix stripped)", req.GetInternalName())
			}
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:     true,
				Enabled:   true,
				SourceUri: upstream,
				// no AllowedClusterIds ⇒ public source placeable on any edge.
			}, nil
		},
	})

	c, w := newSourceCtxSourceRes("source=pull%2Bcam1")
	handleGetPullSource(c, "pull+cam1", 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != upstream {
		t.Fatalf("pull upstream body = %q, want %q (placeable origin returned verbatim)", got, upstream)
	}
}

// Invariant: handleGetSource dispatches a pull+ stream to the pull resolver and,
// on the upstream-origin leg, returns the full upstream URI inline (the /source
// answer Mist dials). Locks that the pull-kind branch of handleGetSource reaches
// the placeable-upstream decision, distinct from origin DTSC and remote.
func TestHandleGetSourcePullDispatchReturnsUpstream(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	t.Setenv("CLUSTER_ID", "cluster-local")

	const upstream = "srt://origin.example.com:9000"
	startPullCommodoreSourceRes(t, &pullSourceFakeSourceRes{
		resolvePull: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:     true,
				Enabled:   true,
				SourceUri: upstream,
			}, nil
		},
	})

	q := url.Values{}
	q.Set("source", "pull+cam2")
	c, w := newSourceCtxSourceRes(q.Encode())
	handleGetSource(c, "pull+cam2", q)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != upstream {
		t.Fatalf("pull dispatch body = %q, want %q", got, upstream)
	}
}

// Invariant: a pull source pinned to a DIFFERENT cluster (allowed_cluster_ids
// excludes this one) is NOT placeable locally. With no federation client wired
// (no allowed peer is pulling), the resolver must fail closed with the explicit
// "not placed" sentinel — never leak the upstream onto a cluster the source
// isn't pinned to, and never push:// or an origin DTSC. This is the placement
// chokepoint that protects pin integrity.
func TestHandleGetPullSourceNotPlacedWhenPinnedElsewhere(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	t.Setenv("CLUSTER_ID", "cluster-local")

	startPullCommodoreSourceRes(t, &pullSourceFakeSourceRes{
		resolvePull: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:             true,
				Enabled:           true,
				SourceUri:         "https://origin.example.com/live/master.m3u8",
				AllowedClusterIds: []string{"cluster-other"}, // pin excludes cluster-local
			}, nil
		},
	})

	c, w := newSourceCtxSourceRes("source=pull%2Bcam3")
	handleGetPullSource(c, "pull+cam3", 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != control.OfflineNotPlaced {
		t.Fatalf("pinned-elsewhere body = %q, want %q (fail closed, no upstream leak)", got, control.OfflineNotPlaced)
	}
}

// Invariant: an enabled pull source whose upstream classifies as ClassBlocked
// (loopback literal) is rejected with the blocked-URI sentinel before any
// placement check — the SSRF/internal-target guard fires regardless of cluster
// policy.
func TestHandleGetPullSourceBlockedUpstream(t *testing.T) {
	withSeededBalancer(t)
	withLoggerSourceRes(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	t.Setenv("CLUSTER_ID", "cluster-local")

	startPullCommodoreSourceRes(t, &pullSourceFakeSourceRes{
		resolvePull: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
			return &commodorepb.ResolvePullSourceByInternalNameResponse{
				Found:     true,
				Enabled:   true,
				SourceUri: "https://127.0.0.1/live/master.m3u8", // loopback ⇒ ClassBlocked
			}, nil
		},
	})

	c, w := newSourceCtxSourceRes("source=pull%2Bcam4")
	handleGetPullSource(c, "pull+cam4", 0, 0, nil, "edgeA", "203.0.113.5", c.Request.Context(), time.Now())

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != control.OfflineBlockedURI {
		t.Fatalf("blocked-upstream body = %q, want %q", got, control.OfflineBlockedURI)
	}
}

// withMockDBSourceRes installs a sqlmock-backed *sql.DB as the package-global
// `db` for the duration of the test, restoring the prior value on cleanup. The
// default regexp matcher treats ExpectQuery args as substrings.
func withMockDBSourceRes(t *testing.T) sqlmock.Sqlmock {
	t.Helper()
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prev := db
	db = mockDB
	t.Cleanup(func() {
		db = prev
		_ = mockDB.Close()
	})
	return mock
}

// Invariant: HandleNodesOverview?full=true composes the DB-backed full-state
// payload — artifacts (joined to vod_metadata, enriched with hosting node_ids),
// processing jobs, and in-memory streams — alongside the node list. The counts
// must match the rows returned, and per-tenant attribution (tenant_id) must be
// carried through to the cluster-ops aggregate payload. This locks the *sql.DB
// branch that the compact-mode wave-1 tests never reached.
func TestHandleNodesOverviewFullStateComposesDBPayload(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerSourceRes(t)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "edge-1", host: "edge-1.example", active: true,
		ramMax: 100, ramCur: 10,
	}, "live+show", 0, 0, 0)

	mock := withMockDBSourceRes(t)

	now := time.Now()
	// artifacts JOIN vod_metadata
	mock.ExpectQuery("FROM foghorn.artifacts").
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "status", "internal_name", "tenant_id",
			"storage_location", "sync_status", "s3_url", "format", "size_bytes",
			"manifest_path", "duration_seconds", "dtsh_synced", "retention_until",
			"created_at", "updated_at",
			"video_codec", "audio_codec", "resolution", "duration_ms", "bitrate_kbps",
			"filename", "title",
		}).AddRow(
			"hash-aaa", "vod", "ready", "vod+clip", "tenant-vod",
			"local", "synced", "s3://bucket/x.mp4", "mp4", int64(1234),
			"/m/x.m3u8", int32(60), true, nil,
			now, now,
			"h264", "aac", "1920x1080", int32(60000), int32(4500),
			"clip.mp4", "My Clip",
		))
	// per-artifact hosting nodes subquery (runs on context.Background())
	mock.ExpectQuery("FROM foghorn.artifact_nodes").
		WithArgs("hash-aaa").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("store-1"))
	// processing jobs
	mock.ExpectQuery("FROM foghorn.processing_jobs").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id", "tenant_id", "artifact_hash", "job_type", "status", "progress",
			"use_gateway", "processing_node_id", "routing_reason", "error_message", "retry_count",
			"created_at", "started_at", "completed_at",
		}).AddRow(
			"job-1", "tenant-vod", "hash-aaa", "transcode", "running", 42,
			true, "proc-1", "capacity", nil, 0,
			now, now, nil,
		))

	c, w := overviewContext("full=true")
	HandleNodesOverview(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal full-state %q: %v", w.Body.String(), err)
	}

	// Node list + count present (full-state wraps the compact list).
	if nc, _ := resp["node_count"].(float64); nc != 1 {
		t.Fatalf("node_count = %v, want 1", resp["node_count"])
	}

	// Artifacts: one row, count matches, tenant attribution + hosting node carried.
	arts, ok := resp["artifacts"].([]any)
	if !ok || len(arts) != 1 {
		t.Fatalf("artifacts = %v, want exactly 1", resp["artifacts"])
	}
	if ac, _ := resp["artifact_count"].(float64); ac != 1 {
		t.Fatalf("artifact_count = %v, want 1", resp["artifact_count"])
	}
	art0 := arts[0].(map[string]any)
	if art0["artifact_hash"] != "hash-aaa" || art0["tenant_id"] != "tenant-vod" {
		t.Fatalf("artifact composition wrong: %v", art0)
	}
	if art0["video_codec"] != "h264" || art0["title"] != "My Clip" {
		t.Fatalf("vod_metadata join not composed into artifact: %v", art0)
	}
	hosts, _ := art0["nodes"].([]any)
	if len(hosts) != 1 || hosts[0] != "store-1" {
		t.Fatalf("artifact hosting nodes = %v, want [store-1]", art0["nodes"])
	}

	// Processing jobs: one row, count matches, tenant + routing carried.
	jobs, ok := resp["processing_jobs"].([]any)
	if !ok || len(jobs) != 1 {
		t.Fatalf("processing_jobs = %v, want exactly 1", resp["processing_jobs"])
	}
	if jc, _ := resp["processing_job_count"].(float64); jc != 1 {
		t.Fatalf("processing_job_count = %v, want 1", resp["processing_job_count"])
	}
	job0 := jobs[0].(map[string]any)
	if job0["job_id"] != "job-1" || job0["tenant_id"] != "tenant-vod" || job0["routing_reason"] != "capacity" {
		t.Fatalf("job composition wrong: %v", job0)
	}

	// In-memory streams included in full state.
	streams, ok := resp["streams"].([]any)
	if !ok || len(streams) == 0 {
		t.Fatalf("streams = %v, want non-empty", resp["streams"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// Invariant: when an artifact query fails, full-state surfaces the error under
// `artifacts_error` rather than aborting the whole overview — operators still
// get the node list and the other sections. Locks the DB-error arm of the
// full-state branch.
func TestHandleNodesOverviewFullStateArtifactsError(t *testing.T) {
	sm := withSeededBalancer(t)
	withLoggerSourceRes(t)
	seedNodeWithStream(t, sm, seedNode{
		nodeID: "edge-1", host: "edge-1.example", active: true,
		ramMax: 100, ramCur: 10,
	}, "", 0, 0, 0)

	mock := withMockDBSourceRes(t)
	mock.ExpectQuery("FROM foghorn.artifacts").
		WillReturnError(context.DeadlineExceeded)
	// jobs query still runs even after artifacts errored.
	mock.ExpectQuery("FROM foghorn.processing_jobs").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id", "tenant_id", "artifact_hash", "job_type", "status", "progress",
			"use_gateway", "processing_node_id", "routing_reason", "error_message", "retry_count",
			"created_at", "started_at", "completed_at",
		}))

	c, w := overviewContext("full=true")
	HandleNodesOverview(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := resp["artifacts_error"]; !has {
		t.Fatalf("expected artifacts_error key on query failure, got %v", resp)
	}
	if !strings.Contains(resp["artifacts_error"].(string), context.DeadlineExceeded.Error()) {
		t.Fatalf("artifacts_error = %v, want the query error", resp["artifacts_error"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
