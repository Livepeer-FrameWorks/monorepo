package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"

	commodorecli "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedmiddleware "github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"

	"frameworks/api_balancing/internal/control"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ingestCommodoreFake records which admission RPC the front door reaches.
// ValidateStreamKey is the claiming call (it takes the 30s ingest lease), so
// the counter is a regression guard: resolution must only ever use
// ResolveStreamContext.
type ingestCommodoreFake struct {
	commodorepb.UnimplementedInternalServiceServer

	streamContext     func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error)
	streamContextHits atomic.Int32
	validateKeyHits   atomic.Int32
}

func (f *ingestCommodoreFake) ResolveStreamContext(ctx context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
	f.streamContextHits.Add(1)
	if f.streamContext != nil {
		return f.streamContext(ctx, req)
	}
	return &commodorepb.ResolveStreamContextResponse{Admitted: true, ClusterPeers: ingestTestPeers()}, nil
}

func (f *ingestCommodoreFake) ValidateStreamKey(ctx context.Context, req *commodorepb.ValidateStreamKeyRequest) (*commodorepb.ValidateStreamKeyResponse, error) {
	f.validateKeyHits.Add(1)
	return &commodorepb.ValidateStreamKeyResponse{Valid: true}, nil
}

func startIngestCommodoreFake(t *testing.T, fake *ingestCommodoreFake) {
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

	prevControl := control.CommodoreClient
	prevHandlers := commodoreClient
	control.CommodoreClient = client
	commodoreClient = client
	t.Cleanup(func() {
		control.CommodoreClient = prevControl
		commodoreClient = prevHandlers
		_ = client.Close()
		srv.Stop()
		_ = lis.Close()
	})
}

// seedIngestTestNode registers a healthy ingest-capable node whose advertised
// outputs resolve to host.
func seedIngestTestNode(t *testing.T, sm *state.StreamStateManager, nodeID, host string) {
	t.Helper()
	lat, lon := 52.0, 4.0
	sm.SetNodeInfo(nodeID, "http://"+host, true, &lat, &lon, "loc-"+nodeID, "",
		map[string]any{"HLS": "http://" + host + "/hls/$/index.m3u8"})
	sm.UpdateNodeMetrics(nodeID, struct {
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
	}{
		CPU:       10,
		RAMMax:    16_000_000_000,
		BWLimit:   1_000_000_000,
		UpSpeed:   1_000,
		CapIngest: true,
	})
	sm.TouchNode(nodeID, true)
	sm.SetProbeVerified(nodeID, true)
	// Selection authorizes a node's authenticated virtual media cluster against
	// the cluster-peer envelope on the resolve response, so responses in these
	// tests carry ingestTestCluster (see ingestTestPeers). The entitlement rules
	// themselves are covered in control.
	sm.SetNodeConnectionInfo(context.Background(), nodeID, nodeID+":18090", "", ingestTestCluster, nil)
}

// newIngestTestRouter wires the front door with a fresh rate limiter so a
// previous test's bucket usage cannot leak into this one.
func newIngestTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prevLimiter := ingestLimiter
	prevLB := lb
	prevLogger := logger
	prevClusterID := clusterID
	ingestLimiter = newIngestRateLimiter()
	lb = balancer.NewLoadBalancer(logging.NewLogger())
	logger = logging.NewLogger()
	clusterID = "cluster-1"
	t.Cleanup(func() {
		ingestLimiter = prevLimiter
		lb = prevLB
		logger = prevLogger
		clusterID = prevClusterID
	})

	router := gin.New()
	router.GET("/ingest/:streamKey", HandleIngestFrontDoor)
	router.POST("/ingest/:streamKey", HandleIngestFrontDoor)
	router.GET("/ingest/", HandleIngestFrontDoor)
	router.POST("/ingest/", HandleIngestFrontDoor)
	return router
}

func admittedStreamContext() *commodorepb.ResolveStreamContextResponse {
	return &commodorepb.ResolveStreamContextResponse{
		Admitted:           true,
		StreamId:           "stream-1",
		InternalName:       "internal-1",
		TenantId:           "tenant-1",
		IngestMode:         "push",
		IsRecordingEnabled: true,
		ClusterPeers:       ingestTestPeers(),
	}
}

// A browser/OBS WHIP publish gets a 307 to the chosen node's WHIP URL: 307
// (not 302) is what preserves the POST method and the SDP offer body.
func TestIngestFrontDoor_POSTRedirectsToWHIP(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	fake := &ingestCommodoreFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return admittedStreamContext(), nil
	}}
	startIngestCommodoreFake(t, fake)
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/sk-abc", nil))

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status: got %d want 307 (body %s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Header().Get("Location"), "http://ingest-a.example.com:18090/webrtc/sk-abc"; got != want {
		t.Errorf("location: got %q want %q", got, want)
	}

	// The claiming RPC must never be reached: resolution takes no lease.
	if hits := fake.validateKeyHits.Load(); hits != 0 {
		t.Errorf("ValidateStreamKey called %d times; resolution must not claim the ingest lease", hits)
	}
	if hits := fake.streamContextHits.Load(); hits != 1 {
		t.Errorf("ResolveStreamContext hits: got %d want 1", hits)
	}
}

func TestIngestFrontDoor_POSTRedirectPreservesWHIPOffer(t *testing.T) {
	type receivedRequest struct {
		method      string
		path        string
		contentType string
		body        string
	}
	received := make(chan receivedRequest, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- receivedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			body:        string(body),
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()

	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", strings.TrimPrefix(target.URL, "http://"))
	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	frontDoor := httptest.NewServer(newIngestTestRouter(t))
	defer frontDoor.Close()

	const offer = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\n"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontDoor.URL+"/ingest/sk-abc", strings.NewReader(offer))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/sdp")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("final status: got %d want %d", resp.StatusCode, http.StatusCreated)
	}

	got := <-received
	if got.method != http.MethodPost || got.path != "/webrtc/sk-abc" || got.contentType != "application/sdp" || got.body != offer {
		t.Fatalf("redirected request = %+v, want POST /webrtc/sk-abc with the original SDP offer", got)
	}
}

func TestIngestFrontDoor_PublicCORSPolicyAndNoStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sharedmiddleware.CORSMiddleware([]string{"https://app.frameworks.network"}, false))
	router.GET("/ingest/:streamKey", HandleIngestFrontDoor)
	router.POST("/ingest/:streamKey", HandleIngestFrontDoor)

	preflight := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/ingest/sk-abc", nil)
	req.Header.Set("Origin", "https://encoder.example")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	router.ServeHTTP(preflight, req)

	if preflight.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", preflight.Code)
	}
	if got := preflight.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("preflight Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := preflight.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("preflight Cache-Control = %q, want no-store", got)
	}
}

// Routing events are persisted verbatim into periscope.routing_decisions, so
// no field may carry the stream key — the WHIP URL that goes in the Location
// header embeds it and must not be reused as telemetry.
func TestIngestFrontDoor_RoutingEventCarriesNoStreamKey(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	router := newIngestTestRouter(t)

	var captured *RoutingEvent
	prevEmit := emitIngestRoutingEventFn
	emitIngestRoutingEventFn = func(e *RoutingEvent) { captured = e }
	t.Cleanup(func() { emitIngestRoutingEventFn = prevEmit })

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/sk-secret-key", nil))

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status: got %d want 307", rec.Code)
	}
	if captured == nil {
		t.Fatal("expected a routing event")
	}
	for field, value := range map[string]string{
		"Details":      captured.Details,
		"SelectedNode": captured.SelectedNode,
		"StreamName":   captured.StreamName,
		"InternalName": captured.InternalName,
	} {
		if strings.Contains(value, "sk-secret-key") {
			t.Errorf("routing event %s leaks the stream key: %q", field, value)
		}
	}
	// The redirect itself still has to carry the key.
	if !strings.Contains(rec.Header().Get("Location"), "sk-secret-key") {
		t.Errorf("redirect must still target the keyed WHIP URL, got %q", rec.Header().Get("Location"))
	}
}

// GET is the curl-able form: full candidate set, but owner-only metadata
// stripped because the endpoint is anonymous.
func TestIngestFrontDoor_GETReturnsJSONWithoutOwnerMetadata(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")
	seedIngestTestNode(t, sm, "ingest-b", "ingest-b.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-abc", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Primary   map[string]any   `json:"primary"`
		Fallbacks []map[string]any `json:"fallbacks"`
		Metadata  map[string]any   `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if payload.Primary["whipUrl"] == nil {
		t.Errorf("primary missing whipUrl: %+v", payload.Primary)
	}
	if len(payload.Fallbacks) != 1 {
		t.Errorf("fallbacks: got %d want 1", len(payload.Fallbacks))
	}
	if _, ok := payload.Metadata["streamKey"]; ok {
		t.Error("streamKey must not be exposed on the anonymous surface")
	}
	if _, ok := payload.Metadata["tenantId"]; ok {
		t.Error("tenantId must not be exposed on the anonymous surface")
	}
	if payload.Metadata["streamId"] != "stream-1" {
		t.Errorf("streamId should survive stripping: %+v", payload.Metadata)
	}
}

// Every rejection reason keeps its own status, so an operational failure is
// never reported to a publisher as a bad stream key.
func TestIngestFrontDoor_RejectionStatuses(t *testing.T) {
	cases := []struct {
		name   string
		resp   *commodorepb.ResolveStreamContextResponse
		status int
		code   string
	}{
		{"invalid key", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY), 404, "INVALID_STREAM_KEY"},
		{"suspended", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED), 403, "ACCOUNT_SUSPENDED"},
		{"negative balance", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE), 402, "PAYMENT_REQUIRED"},
		{"not entitled", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_NOT_ENTITLED), 403, "CLUSTER_NOT_ENTITLED"},
		{"cluster unhealthy", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_CLUSTER_UNHEALTHY), 503, "CLUSTER_UNHEALTHY"},
		{"duplicate ingest", deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST), 409, "DUPLICATE_INGEST"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")
			resp := tc.resp
			startIngestCommodoreFake(t, &ingestCommodoreFake{
				streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
					return resp, nil
				},
			})
			router := newIngestTestRouter(t)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/sk-abc", nil))

			if rec.Code != tc.status {
				t.Fatalf("status: got %d want %d (body %s)", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.code) {
				t.Errorf("body %s does not carry code %s", rec.Body.String(), tc.code)
			}
		})
	}
}

// Every response on this route is reached via a credential-bearing URL, so the
// no-store policy has to hold on the failure paths too — a cached 404 or 429
// keyed on the stream key is still a cached secret.
func TestIngestFrontDoor_NoStoreOnEveryPath(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return deniedContext(commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY), nil
		},
	})
	router := newIngestTestRouter(t)

	// Invalid key (404).
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-bogus", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("404 Cache-Control: got %q want no-store", got)
	}

	// Rate limited (429).
	var lastRec *httptest.ResponseRecorder
	for i := 0; i < int(ingestLimiter.burst)+5; i++ {
		lastRec = httptest.NewRecorder()
		router.ServeHTTP(lastRec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-bogus", nil))
	}
	if lastRec.Code != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429", lastRec.Code)
	}
	if got := lastRec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("429 Cache-Control: got %q want no-store", got)
	}
}

// A pull stream is legitimately admitted by Commodore, but must never accept a
// push. Guards the evaluator's ordering through the HTTP surface.
func TestIngestFrontDoor_AdmittedPullStreamRejected(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			resp := admittedStreamContext()
			resp.IngestMode = "pull"
			return resp, nil
		},
	})
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/sk-abc", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

// Commodore unreachable must fail closed with a transient status, never a
// stale success or a "bad key".
func TestIngestFrontDoor_CommodoreErrorFailsClosed(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return nil, status.Error(codes.Unavailable, "billing status lookup failed")
		},
	})
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-abc", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "VALIDATION_UNAVAILABLE") {
		t.Errorf("body %s missing VALIDATION_UNAVAILABLE", rec.Body.String())
	}
}

// With no ingest-capable node there is nowhere to send the publisher; that is
// a transient capacity fact, not an authentication failure.
func TestIngestFrontDoor_NoIngestNodes(t *testing.T) {
	state.ResetDefaultManagerForTests()

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/sk-abc", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "NO_INGEST_NODES") {
		t.Errorf("body %s missing NO_INGEST_NODES", rec.Body.String())
	}
}

// The limiter has to fire before Commodore is consulted, otherwise stream-key
// enumeration still costs a database round trip per guess.
func TestIngestFrontDoor_RateLimitPrecedesCommodore(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	fake := &ingestCommodoreFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return admittedStreamContext(), nil
	}}
	startIngestCommodoreFake(t, fake)
	router := newIngestTestRouter(t)

	burst := int(ingestLimiter.burst)
	var lastCode int
	for i := 0; i < burst+5; i++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-abc", nil))
		lastCode = rec.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exhausting the burst, got %d", lastCode)
	}
	if hits := int(fake.streamContextHits.Load()); hits > burst {
		t.Errorf("Commodore consulted %d times for %d allowed requests; limiter must run first", hits, burst)
	}
}

// The shared router never configures trusted proxies, so an attacker-supplied
// X-Forwarded-For must not mint a fresh bucket per request.
func TestIngestFrontDoor_SpoofedForwardedForSharesBucket(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	router := newIngestTestRouter(t)

	burst := int(ingestLimiter.burst)
	var lastCode int
	for i := 0; i < burst+5; i++ {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-abc", nil)
		// A different claimed origin on every request.
		req.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i%9)))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		lastCode = rec.Code
	}
	if lastCode != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For bypassed the limiter; got %d", lastCode)
	}
}

// A missing credential is a malformed request, not an unknown stream: the
// empty-segment route is registered explicitly so it reaches the handler's 400
// rather than falling through to the router's 404.
func TestIngestFrontDoor_MissingStreamKey(t *testing.T) {
	router := newIngestTestRouter(t)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty stream key: got %d want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "MISSING_STREAM_KEY") {
		t.Errorf("body %s missing MISSING_STREAM_KEY", rec.Body.String())
	}
}

func deniedContext(reason commodorepb.StreamKeyRejectionReason) *commodorepb.ResolveStreamContextResponse {
	return &commodorepb.ResolveStreamContextResponse{
		Admitted:        false,
		IngestMode:      "push",
		RejectionReason: reason,
	}
}

// The limiter, geo lookup, and routing telemetry all identify the publisher via
// the trusted resolver. The shared access and panic logs read the resolved value
// from the context — so if the handler does not publish it, they fall back to the
// peer address and name the proxy instead, splitting one request across two
// identities in the record.
func TestIngestFrontDoor_PublishesTrustedClientIPToContext(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	startIngestCommodoreFake(t, &ingestCommodoreFake{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return admittedStreamContext(), nil
		},
	})
	// newIngestTestRouter is called for its global setup; the router is rebuilt
	// here so the capture middleware is registered before the routes — gin does
	// not apply middleware added afterwards.
	newIngestTestRouter(t)

	var ginValue, ctxValue string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Next()
		if v, ok := c.Get(string(ctxkeys.KeyClientIP)); ok {
			ginValue, _ = v.(string)
		}
		if v, ok := c.Request.Context().Value(ctxkeys.KeyClientIP).(string); ok {
			ctxValue = v
		}
	})
	router.GET("/ingest/:streamKey", HandleIngestFrontDoor)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ingest/sk-abc", nil)
	req.RemoteAddr = "203.0.113.9:44321"
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ginValue != "203.0.113.9" {
		t.Errorf("gin context client IP = %q, want the resolved caller", ginValue)
	}
	if ctxValue != "203.0.113.9" {
		t.Errorf("request context client IP = %q, want the resolved caller", ctxValue)
	}
}

// The HTTP front door must declare no cluster either. It runs in a Foghorn
// serving several media clusters, so its CLUSTER_ID names the process, not the
// destination; sending it submits an infrastructure cluster to a gate asking
// whether the tenant may publish there, which denies every publish for tenants
// not entitled to it. Commodore resolves the target and returns it as origin.
func TestIngestFrontDoor_DeclaresNoClusterToCommodore(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	seedIngestTestNode(t, sm, "ingest-a", "ingest-a.example.com:18090")

	var declared atomic.Value
	declared.Store("unset")
	fake := &ingestCommodoreFake{streamContext: func(_ context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		declared.Store(req.GetClusterId())
		return admittedStreamContext(), nil
	}}
	startIngestCommodoreFake(t, fake)
	router := newIngestTestRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ingest/sk-abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := declared.Load().(string); got != "" {
		t.Fatalf("front door declared cluster %q; a resolve must name none", got)
	}
}

// ingestTestCluster is the virtual media cluster seeded ingest nodes belong to.
const ingestTestCluster = "cluster-1"

// ingestTestPeers is the envelope Commodore returns: plan-filtered and healthy,
// which is what makes membership alone sufficient to authorize a candidate.
func ingestTestPeers() []*clusterpeerpb.TenantClusterPeer {
	return []*clusterpeerpb.TenantClusterPeer{{ClusterId: ingestTestCluster, HealthStatus: "healthy"}}
}
