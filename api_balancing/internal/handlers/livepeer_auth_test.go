package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func configureLivepeerAuthNode(t *testing.T, healthy, ingest, processing bool) *state.StreamStateManager {
	t.Helper()
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "test-job-secret")
	oldCluster := clusterID
	clusterID = "media-gateway"
	t.Cleanup(func() { clusterID = oldCluster })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("edge-1", "http://203.0.113.4:4242", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-1", healthy)
	sm.SetNodeConnectionInfo(context.Background(), "edge-1", "203.0.113.4", "tenant-1", "edge-cell", nil)
	sm.UpdateNodeMetrics("edge-1", struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{CapEdge: true, CapIngest: ingest, CapProcessing: processing, ProcessingClasses: map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 1, Ready: []string{"slot-1"}},
	}})
	return sm
}

func mintLivepeerAuthToken(t *testing.T, claims control.TranscodeJobClaims) string {
	t.Helper()
	claims.NodeID = "edge-1"
	claims.ClusterID = "edge-cell"
	claims.TenantID = "tenant-1"
	claims.AllowedGatewayClusterIDs = []string{"media-gateway"}
	claims.IssuedAt = time.Now().Unix()
	token, err := control.MintTranscodeJobToken("test-job-secret", claims)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestHandleLivepeerAuthRejectsMissingTokenWithHTTP403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldLogger := logger
	logger = logging.NewLogger()
	t.Cleanup(func() { logger = oldLogger })
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() { db = oldDB; _ = mockDB.Close() })

	body := []byte(`{"url":"http://gateway/live/processing+secret-artifact/0.ts","remoteIP":"203.0.113.4"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/livepeer/auth", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	HandleLivepeerAuth(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing token status=%d body=%s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("missing token reached database: %v", err)
	}
}

func TestAuthorizeSignedLivepeerJobRejectsManifestIPClusterAndCapabilityDrift(t *testing.T) {
	configureLivepeerAuthNode(t, true, false, false)
	claims := control.TranscodeJobClaims{
		ManifestID: "processing+artifact", JobID: "job-1", AttemptOrGeneration: "2", Session: "job-1", SpecDigest: strings.Repeat("a", 64),
	}
	token := mintLivepeerAuthToken(t, claims)

	for _, tc := range []struct {
		name, manifestID, remoteIP, wantReason string
	}{
		{name: "manifest swap", manifestID: "processing+other", remoteIP: "203.0.113.4", wantReason: authRejectInvalidToken},
		{name: "unrecognized remote IP", manifestID: "processing+artifact", remoteIP: "203.0.113.9", wantReason: authRejectNodeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := authorizeSignedLivepeerJob(context.Background(), tc.manifestID, livepeerAuthRequest{JobToken: token, RemoteIP: tc.remoteIP})
			if got != nil || reason != tc.wantReason {
				t.Fatalf("reason=%q context=%+v, want %q", reason, got, tc.wantReason)
			}
		})
	}

	oldCluster := clusterID
	clusterID = "unapproved-gateway"
	got, reason := authorizeSignedLivepeerJob(context.Background(), "processing+artifact", livepeerAuthRequest{JobToken: token, RemoteIP: "203.0.113.4"})
	clusterID = oldCluster
	if got != nil || reason != authRejectInvalidToken {
		t.Fatalf("unapproved gateway cluster: reason=%q context=%+v", reason, got)
	}

	got, reason = authorizeSignedLivepeerJob(context.Background(), "processing+artifact", livepeerAuthRequest{JobToken: token, RemoteIP: "203.0.113.4"})
	if got != nil || reason != authRejectNodeCapability {
		t.Fatalf("missing processing capability: reason=%q context=%+v", reason, got)
	}
}

func TestAuthorizeSignedLivepeerLiveJobBindsGenerationAndCanonicalSpec(t *testing.T) {
	sm := configureLivepeerAuthNode(t, true, true, false)
	normalizedCounter := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_livepeer_auth_profile_normalized_total"}, nil)
	oldMetrics := metrics
	metrics = &FoghornMetrics{LivepeerAuthProfileNormalized: normalizedCounter}
	t.Cleanup(func() { metrics = oldMetrics })
	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"profile":"H264ConstrainedHigh"}],"workload":"live","deadline_ms":1000,"min_speed":1,"frameworks_gateway_cluster_ids":["media-gateway"]}]`
	digest, err := mist.LivepeerJobSpecDigest(processesJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.UpdateStreamFromBuffer("live+stream", "stream", "edge-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.SetStreamStreamID("stream", "stream-id")
	if _, err := sm.BindStreamLivepeerAuth("stream", processesJSON, digest, "state:generation-1"); err != nil {
		t.Fatal(err)
	}
	claims := control.TranscodeJobClaims{
		ManifestID: "live+stream", AttemptOrGeneration: "state:generation-1", Session: "state:generation-1", SpecDigest: digest,
	}
	token := mintLivepeerAuthToken(t, claims)
	request := livepeerAuthRequest{
		JobToken: token, RemoteIP: "203.0.113.4", Source: livepeerSource{Width: 1280, Height: 720, FPS: 30, Codec: "h264"},
		Profiles: []livepeerJSONProfile{{"name": "360p", "bitrate": 900000, "height": 360, "profile": "H264ConstrainedHigh"}},
	}
	got, reason := authorizeSignedLivepeerJob(context.Background(), "live+stream", request)
	if got == nil || reason != "" || got.StreamID != "stream-id" || got.Workload != "live" || got.SpecDigest != digest {
		t.Fatalf("live authorization failed: reason=%q context=%+v", reason, got)
	}
	if got := testutil.ToFloat64(normalizedCounter.WithLabelValues()); got != 1 {
		t.Fatalf("profile normalization counter=%v, want 1", got)
	}

	claims.AttemptOrGeneration = "state:generation-2"
	claims.Session = "state:generation-2"
	request.JobToken = mintLivepeerAuthToken(t, claims)
	got, reason = authorizeSignedLivepeerJob(context.Background(), "live+stream", request)
	if got != nil || reason != authRejectStaleJob {
		t.Fatalf("stale live generation accepted: reason=%q context=%+v", reason, got)
	}

	claims.AttemptOrGeneration = "state:generation-1"
	claims.Session = "state:generation-1"
	claims.SpecDigest = strings.Repeat("b", 64)
	request.JobToken = mintLivepeerAuthToken(t, claims)
	got, reason = authorizeSignedLivepeerJob(context.Background(), "live+stream", request)
	if got != nil || reason != authRejectStaleJob {
		t.Fatalf("tampered live spec digest accepted: reason=%q context=%+v", reason, got)
	}
}

func TestAuthorizeSignedLivepeerProcessingJobReturnsStoredCanonicalContract(t *testing.T) {
	t.Setenv("FOGHORN_BALANCER_CAPABILITY_SECRET", "test-job-secret")
	oldCluster := clusterID
	clusterID = "media-gateway"
	t.Cleanup(func() { clusterID = oldCluster })

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("edge-1", "http://203.0.113.4:4242", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-1", true)
	sm.SetNodeConnectionInfo(context.Background(), "edge-1", "203.0.113.4", "tenant-1", "edge-cell", nil)
	sm.UpdateNodeMetrics("edge-1", struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]state.ClassCapacity
	}{CapEdge: true, CapProcessing: true, ProcessingClasses: map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 1, Ready: []string{"slot-1"}},
	}})

	processesJSON := `[{"process":"Livepeer","target_profiles":[{"name":"360p","bitrate":900000,"height":360,"profile":"H264ConstrainedHigh"}],"workload":"vod","deadline_ms":30000,"min_speed":0.5,"frameworks_gateway_cluster_ids":["media-gateway"]}]`
	digest, err := mist.LivepeerJobSpecDigest(processesJSON)
	if err != nil {
		t.Fatal(err)
	}
	claims := control.TranscodeJobClaims{
		ManifestID: "processing+artifact", JobID: "job-1", AttemptOrGeneration: "2", Session: "job-1",
		NodeID: "edge-1", ClusterID: "edge-cell", TenantID: "tenant-1", SpecDigest: digest,
		AllowedGatewayClusterIDs: []string{"media-gateway"}, IssuedAt: time.Now().Unix(),
	}
	token, err := control.MintTranscodeJobToken("test-job-secret", claims)
	if err != nil {
		t.Fatal(err)
	}

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() { db = oldDB; _ = mockDB.Close() })
	mock.ExpectQuery(`SELECT pj\.job_id::text AS job_id[\s\S]*FROM foghorn\.processing_jobs`).
		WithArgs("job-1", "artifact").
		WillReturnRows(sqlmock.NewRows([]string{"job_id", "tenant_id", "stream_id", "processes_json", "retry_count", "processing_node_id", "status", "width", "height", "fps", "input_codec"}).
			AddRow("job-1", "tenant-1", "stream-1", processesJSON, int32(2), "edge-1", "processing", 1920, 1080, 30.0, "h264"))

	got, reason := authorizeSignedLivepeerJob(context.Background(), "processing+artifact-Ab12Cd34", livepeerAuthRequest{
		URL: "http://gateway/live/processing+artifact-Ab12Cd34/0.ts", JobToken: token, RemoteIP: "203.0.113.4",
		Source: livepeerSource{Width: 1920, Height: 1080, FPS: 30, Codec: "h264", PixelFormat: "yuv420p"},
		Profiles: []livepeerJSONProfile{{"name": "360p", "bitrate": 900000, "height": 360, "width": 640,
			"fps": 30000, "fpsDen": 1000, "profile": "H264ConstrainedHigh", "gop": "0.0"}},
	})
	if got == nil || reason != "" {
		t.Fatalf("authorization failed: reason=%q context=%+v", reason, got)
	}
	if got.TenantID != "tenant-1" || got.StreamID != "stream-1" || got.NodeID != "edge-1" || got.SpecDigest != digest || got.Workload != "vod" || got.DeadlineMs != 30000 || got.MinSpeed != 0.5 || len(got.Profiles) != 1 {
		t.Fatalf("unexpected canonical context: %+v", got)
	}

	// A self-hosted edge may omit its observed profiles, but it may not replace
	// the canonical profile values embedded in the signed job spec.
	mock.ExpectQuery(`SELECT pj\.job_id::text AS job_id[\s\S]*FROM foghorn\.processing_jobs`).
		WithArgs("job-1", "artifact").
		WillReturnRows(sqlmock.NewRows([]string{"job_id", "tenant_id", "stream_id", "processes_json", "retry_count", "processing_node_id", "status", "width", "height", "fps", "input_codec"}).
			AddRow("job-1", "tenant-1", "stream-1", processesJSON, int32(2), "edge-1", "processing", 1920, 1080, 30.0, "h264"))
	got, reason = authorizeSignedLivepeerJob(context.Background(), "processing+artifact-Ab12Cd34", livepeerAuthRequest{
		URL: "http://gateway/live/processing+artifact-Ab12Cd34/0.ts", JobToken: token, RemoteIP: "203.0.113.4",
		Source:   livepeerSource{Width: 1920, Height: 1080, FPS: 30, Codec: "h264", PixelFormat: "yuv420p"},
		Profiles: []livepeerJSONProfile{{"name": "360p", "bitrate": 1, "height": 360, "profile": "H264ConstrainedHigh"}},
	})
	if got != nil || reason != authRejectSpecMismatch {
		t.Fatalf("modified client profiles were not rejected: reason=%q context=%+v", reason, got)
	}

	mock.ExpectQuery(`SELECT pj\.job_id::text AS job_id[\s\S]*FROM foghorn\.processing_jobs`).
		WithArgs("job-1", "artifact").
		WillReturnRows(sqlmock.NewRows([]string{"job_id", "tenant_id", "stream_id", "processes_json", "retry_count", "processing_node_id", "status", "width", "height", "fps", "input_codec"}).
			AddRow("job-1", "tenant-1", "stream-1", processesJSON, int32(2), "edge-1", "processing", 1920, 1080, 30.0, "h264"))
	got, reason = authorizeSignedLivepeerJob(context.Background(), "processing+artifact-Ab12Cd34", livepeerAuthRequest{
		URL: "http://gateway/live/processing+artifact-Ab12Cd34/0.ts", JobToken: token, RemoteIP: "203.0.113.4",
		Source: livepeerSource{Width: 1280, Height: 720, FPS: 30, Codec: "h264", PixelFormat: "yuv420p"},
	})
	if got != nil || reason != authRejectSourceMismatch {
		t.Fatalf("modified source media was not rejected: reason=%q context=%+v", reason, got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizeSignedLivepeerJobRejectsInvalidTokenBeforeDatabaseLookup(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	oldDB := db
	db = mockDB
	t.Cleanup(func() { db = oldDB; _ = mockDB.Close() })

	got, reason := authorizeSignedLivepeerJob(context.Background(), "processing+secret-artifact", livepeerAuthRequest{
		URL: "http://gateway/live/processing+secret-artifact/0.ts", JobToken: "attacker-controlled", RemoteIP: "203.0.113.4",
	})
	if got != nil || reason != authRejectInvalidToken {
		t.Fatalf("invalid token result: reason=%q context=%+v", reason, got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("invalid token reached the database: %v", err)
	}
}

func TestLivepeerAuthResponseHasExactConsumerKeys(t *testing.T) {
	encoded, err := json.Marshal(livepeerAuthResponse{})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"manifestID", "tenantID", "streamID", "profiles", "workload", "deadlineMs", "minSpeed", "authorizedEdgeNodeID", "specDigest"}
	if len(got) != len(want) {
		t.Fatalf("response keys=%v", got)
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Fatalf("response missing %s: %s", key, encoded)
		}
	}
}

func TestLivepeerProfilesFromProcessesJSONNormalizesMistLivepeerProfiles(t *testing.T) {
	processesJSON := `[{"process":"AV","codec":"AAC","track_select":"audio=all&video=none&subtitle=none"},{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","hardcoded_broadcasters":"[{\"address\":\"https://livepeer.example\"}]","target_profiles":[{"name":"360p","bitrate":900000,"fps":0,"height":360,"profile":"H264ConstrainedHigh","track_inhibit":"video=<640x360"},{"name":"480p","bitrate":1600000,"fps":0,"height":480,"profile":"H264ConstrainedHigh","track_inhibit":"video=<850x480"}]}]`

	got := mist.LivepeerProfilesFromProcessesJSON(processesJSON, mist.SourceMediaInfo{
		Width:  2718,
		Height: 1750,
		FPS:    24,
	})
	if len(got) != 2 {
		t.Fatalf("expected 2 profiles, got %d: %#v", len(got), got)
	}
	want := []livepeerJSONProfile{
		{
			"name":    "360p",
			"bitrate": float64(900000),
			"fps":     24000,
			"fpsDen":  1000,
			"height":  360,
			"width":   560,
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
		{
			"name":    "480p",
			"bitrate": float64(1600000),
			"fps":     24000,
			"fpsDen":  1000,
			"height":  480,
			"width":   746,
			"profile": "H264ConstrainedHigh",
			"gop":     "0.0",
		},
	}
	assertJSONEqual(t, want, got)
	if _, ok := got[0]["track_inhibit"]; ok {
		t.Fatal("expected non-matching track_inhibit to be removed before auth response")
	}
}

func TestLivepeerProfilesFromProcessesJSONDropsInhibitedProfiles(t *testing.T) {
	processesJSON := `[{"process":"Livepeer","source_track":"maxbps","track_select":"video=maxbps","target_profiles":[{"name":"1080p","bitrate":6500000,"fps":0,"height":1080,"profile":"H264ConstrainedHigh","track_inhibit":"video=<1920x1080"}]}]`

	got := mist.LivepeerProfilesFromProcessesJSON(processesJSON, mist.SourceMediaInfo{
		Width:  640,
		Height: 360,
		FPS:    30,
	})
	if len(got) != 0 {
		t.Fatalf("expected inhibited profile to be dropped, got %#v", got)
	}
}

func assertJSONEqual(t *testing.T, want, got interface{}) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("json mismatch\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}

func TestExtractManifestID(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"http://gw:8935/live/abc123/0.ts", "abc123"},
		{"http://gw:8935/live/abc123/segment-12.ts", "abc123"},
		{"/live/foo/bar.ts", "foo"},
		{"http://gw:8935/notlive/abc/0.ts", ""},
		{"", ""},
		{"://broken", ""},
	}
	for _, tc := range cases {
		got := extractManifestID(tc.raw)
		if got != tc.want {
			t.Errorf("extractManifestID(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
