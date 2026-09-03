package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// setMonitor installs a PrometheusMonitor global for the duration of the test
// and restores the previous value afterwards.
func setMonitor(t *testing.T, pm *PrometheusMonitor) {
	t.Helper()
	prev := prometheusMonitor
	prometheusMonitor = pm
	t.Cleanup(func() { prometheusMonitor = prev })
}

// fakeMistServer answers the loopback auth handshake with status OK and returns
// the supplied body for every /api2 command, so a real *mist.Client can be
// pointed at it without emulating the challenge/response flow.
func fakeMistServer(t *testing.T, body map[string]any) *mist.Client {
	t.Helper()
	body["authorize"] = map[string]any{"status": "OK"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	c := mist.NewClient(logging.NewLogger())
	c.BaseURL = srv.URL
	return c
}

func doRequest(t *testing.T, handler gin.HandlerFunc, method, target string, params gin.Params) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequestWithContext(context.Background(), method, target, nil)
	ctx.Params = params
	handler(ctx)
	return rec
}

func TestHandleEdgeStatus(t *testing.T) {
	rec := doRequest(t, HandleEdgeStatus, http.MethodGet, "/edge/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"node_id", "operational_mode", "tenant_id", "uptime_seconds", "version"} {
		if _, ok := body[key]; !ok {
			t.Errorf("status response missing %q: %v", key, body)
		}
	}
}

// Every monitor-backed handler must fail safe with 503 before the monitor boots,
// rather than panicking on a nil global.
func TestEdgeHandlersGuardNilMonitor(t *testing.T) {
	setMonitor(t, nil)
	cases := []struct {
		name    string
		handler gin.HandlerFunc
		params  gin.Params
	}{
		{"health", HandleEdgeHealth, nil},
		{"streams", HandleEdgeStreams, nil},
		{"stream detail", HandleEdgeStreamDetail, gin.Params{{Key: "stream_name", Value: "live+s"}}},
		{"clients", HandleEdgeClients, nil},
		{"metrics", HandleEdgeMetrics, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, tc.handler, http.MethodGet, "/edge", tc.params)
			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("nil monitor code = %d, want 503", rec.Code)
			}
		})
	}
}

func TestHandleEdgeHealth(t *testing.T) {
	setMonitor(t, &PrometheusMonitor{isHealthy: true})
	rec := doRequest(t, HandleEdgeHealth, http.MethodGet, "/edge/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["healthy"] != true || body["mist_reachable"] != true {
		t.Errorf("health body = %v", body)
	}
}

func TestHandleEdgeMetricsExtractsTotals(t *testing.T) {
	client := mist.NewClient(logging.NewLogger())
	pm := monitorWithMistRuntime(client)
	pm.lastJSONData = map[string]any{
		"totals": map[string]any{"cpu": 12.5, "mem": 2048.0, "viewers": 7.0},
	}
	setMonitor(t, pm)
	rec := doRequest(t, HandleEdgeMetrics, http.MethodGet, "/edge/metrics", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["cpu_percent"] != 12.5 {
		t.Errorf("cpu_percent = %v, want 12.5", body["cpu_percent"])
	}
	if body["total_viewers"] != float64(7) {
		t.Errorf("total_viewers = %v, want 7", body["total_viewers"])
	}
}

func TestHandleEdgeMetricsReturnsUnavailableWithoutNode(t *testing.T) {
	setMonitor(t, &PrometheusMonitor{})
	rec := doRequest(t, HandleEdgeMetrics, http.MethodGet, "/edge/metrics", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

// HandleEdgeStreams maps MistServer's active_streams response into the edge API
// shape; the per-stream field extraction is the substance under test.
func TestHandleEdgeStreamsHappyPath(t *testing.T) {
	client := fakeMistServer(t, map[string]any{
		"active_streams": map[string]any{
			"live+show": map[string]any{
				"viewers":   3.0,
				"clients":   4.0,
				"upbytes":   1000.0,
				"downbytes": 2000.0,
				"inputs":    1.0,
				"outputs":   2.0,
			},
		},
	})
	setMonitor(t, monitorWithMistRuntime(client))

	rec := doRequest(t, HandleEdgeStreams, http.MethodGet, "/edge/streams", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Count   int `json:"count"`
		Streams []struct {
			Name    string `json:"name"`
			Viewers int    `json:"viewers"`
			UpBytes uint64 `json:"up_bytes"`
			Outputs int    `json:"outputs"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || len(body.Streams) != 1 {
		t.Fatalf("expected 1 stream, got %+v", body)
	}
	s := body.Streams[0]
	if s.Name != "live+show" || s.Viewers != 3 || s.UpBytes != 1000 || s.Outputs != 2 {
		t.Errorf("stream mapping wrong: %+v", s)
	}
}

func TestHandleEdgeStreamsBadGatewayOnMistError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	client := mist.NewClient(logging.NewLogger())
	client.BaseURL = srv.URL
	setMonitor(t, monitorWithMistRuntime(client))

	rec := doRequest(t, HandleEdgeStreams, http.MethodGet, "/edge/streams", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("mist error code = %d, want 502", rec.Code)
	}
}

func monitorWithMistRuntime(client *mist.Client) *PrometheusMonitor {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &mistNodeRuntime{
		generation:        1,
		nodeID:            "node-test",
		baseURL:           client.BaseURL,
		client:            client,
		acceleratorClient: client,
		ctx:               ctx,
		cancel:            cancel,
	}
	return &PrometheusMonitor{nodeRuntime: runtime}
}

func TestHandleEdgeStreamsReturnsUnavailableWithoutNode(t *testing.T) {
	setMonitor(t, &PrometheusMonitor{})
	rec := doRequest(t, HandleEdgeStreams, http.MethodGet, "/edge/streams", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestHandleEdgeStreamsReturnsUnavailableWhenNodeIsRemovedMidRequest(t *testing.T) {
	monitorLogger = logging.NewLogger()
	requestStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("command"), "authorize") {
			_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}})
			return
		}
		requestStarted <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := mist.NewClient(logging.NewLogger())
	client.BaseURL = server.URL
	pm := monitorWithMistRuntime(client)
	pm.nodeID = "node-test"
	pm.baseURL = server.URL
	setMonitor(t, pm)

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- doRequest(t, HandleEdgeStreams, http.MethodGet, "/edge/streams", nil)
	}()
	<-requestStarted
	pm.RemoveNode("node-test")
	select {
	case rec := <-response:
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("code = %d, want 503; body=%s", rec.Code, rec.Body.String())
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("edge request did not stop when its node runtime was removed")
	}
}

func TestHandleEdgeStreamDetailRequiresName(t *testing.T) {
	setMonitor(t, &PrometheusMonitor{})
	rec := doRequest(t, HandleEdgeStreamDetail, http.MethodGet, "/edge/streams/", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing stream_name code = %d, want 400", rec.Code)
	}
}

// EdgeAPIAuthMiddleware gates the edge API: a missing/malformed Authorization
// header is rejected before any validation round-trip, and a transport failure
// to the validator surfaces as 502 (not a silent allow).
func TestEdgeAPIAuthMiddleware(t *testing.T) {
	mw := EdgeAPIAuthMiddleware()

	t.Run("missing header", func(t *testing.T) {
		rec := doRequest(t, mw, http.MethodGet, "/edge/streams", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("code = %d, want 401", rec.Code)
		}
	})

	t.Run("non-bearer header", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/edge/streams", nil)
		ctx.Request.Header.Set("Authorization", "Basic abc")
		mw(ctx)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("code = %d, want 401", rec.Code)
		}
	})

	t.Run("validator unreachable yields 502", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/edge/streams", nil)
		ctx.Request.Header.Set("Authorization", "Bearer some-token")
		mw(ctx)
		// With no control stream connected, ValidateEdgeToken returns an error
		// and the middleware must reject with 502 rather than allow the request.
		if rec.Code != http.StatusBadGateway {
			t.Errorf("code = %d, want 502", rec.Code)
		}
	})
}
