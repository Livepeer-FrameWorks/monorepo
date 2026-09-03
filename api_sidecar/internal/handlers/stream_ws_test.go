package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/gorilla/websocket"
)

type fakeStreamWSServer struct {
	t           *testing.T
	server      *httptest.Server
	upgrader    websocket.Upgrader
	connections chan *websocket.Conn
	queries     chan []string

	mu         sync.Mutex
	streams    map[string]map[string]any
	queryBlock <-chan struct{}
}

func newFakeStreamWSServer(t *testing.T) *fakeStreamWSServer {
	t.Helper()
	fake := &fakeStreamWSServer{
		t:           t,
		upgrader:    websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		connections: make(chan *websocket.Conn, 8),
		queries:     make(chan []string, 32),
		streams:     make(map[string]map[string]any),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeStreamWSServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/ws" {
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if r.URL.Query().Get("streams") != "1" {
			f.t.Errorf("WebSocket streams query = %q", r.URL.Query().Get("streams"))
		}
		if err := conn.WriteJSON([]any{"auth", true}); err != nil {
			_ = conn.Close()
			return
		}
		f.connections <- conn
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}

	var command map[string]any
	if err := json.Unmarshal([]byte(r.URL.Query().Get("command")), &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, auth := command["authorize"]; auth {
		_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}})
		return
	}
	query, ok := command["active_streams"].(map[string]any)
	if !ok {
		http.Error(w, "missing active_streams", http.StatusBadRequest)
		return
	}
	var names []string
	if rawNames, exists := query["streams"].([]any); exists {
		for _, rawName := range rawNames {
			if name, ok := rawName.(string); ok {
				names = append(names, name)
			}
		}
		sort.Strings(names)
	}
	f.queries <- names
	if f.queryBlock != nil {
		select {
		case <-f.queryBlock:
		case <-r.Context().Done():
			return
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	response := make(map[string]any)
	if len(names) == 0 {
		for name, stream := range f.streams {
			response[name] = stream
		}
	} else {
		for _, name := range names {
			if stream, exists := f.streams[name]; exists {
				response[name] = stream
			}
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"active_streams": response})
}

func (f *fakeStreamWSServer) setStream(name string, status, inputs, outputs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams[name] = map[string]any{
		"status":  mistAPIStatusLabel(status),
		"inputs":  inputs,
		"outputs": outputs,
		"viewers": 1,
		"clients": 1,
		"tracks":  1,
		"health":  map[string]any{},
	}
}

func mistAPIStatusLabel(status int) string {
	return map[int]string{
		0: "Offline", 1: "Initializing", 2: "Input booting", 3: "Waiting for data",
		4: "Online", 5: "Shutting down", 6: "Offline",
	}[status]
}

func (f *fakeStreamWSServer) deleteStream(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.streams, name)
}

func streamFrame(name string, status, viewers, inputs, outputs int) []any {
	return []any{"stream", []any{name, status, viewers, inputs, outputs, ""}}
}

func configureFastStreamWSTest(t *testing.T) {
	t.Helper()
	oldInitial := streamWSInitialBackoff
	oldMax := streamWSMaxBackoff
	oldHealthy := streamWSHealthyAfter
	oldWarmup := streamWSWarmupWindow
	oldBatch := streamWSBatchDelay
	oldMinimum := streamWSMinRefresh
	oldFullSweepDelay := streamWSFullSweepDelay
	oldSweepBusyDelay := streamWSSweepBusyDelay
	oldPongWait := streamWSPongWait
	oldPingInterval := streamWSPingInterval
	oldWriteWait := streamWSWriteWait
	streamWSInitialBackoff = 5 * time.Millisecond
	streamWSMaxBackoff = 20 * time.Millisecond
	streamWSHealthyAfter = 30 * time.Millisecond
	streamWSWarmupWindow = 20 * time.Millisecond
	streamWSBatchDelay = 10 * time.Millisecond
	streamWSMinRefresh = 50 * time.Millisecond
	streamWSFullSweepDelay = 50 * time.Millisecond
	streamWSSweepBusyDelay = 5 * time.Millisecond
	streamWSPongWait = 100 * time.Millisecond
	streamWSPingInterval = 20 * time.Millisecond
	streamWSWriteWait = 20 * time.Millisecond
	t.Cleanup(func() {
		streamWSInitialBackoff = oldInitial
		streamWSMaxBackoff = oldMax
		streamWSHealthyAfter = oldHealthy
		streamWSWarmupWindow = oldWarmup
		streamWSBatchDelay = oldBatch
		streamWSMinRefresh = oldMinimum
		streamWSFullSweepDelay = oldFullSweepDelay
		streamWSSweepBusyDelay = oldSweepBusyDelay
		streamWSPongWait = oldPongWait
		streamWSPingInterval = oldPingInterval
		streamWSWriteWait = oldWriteWait
	})
}

func newStreamWSTestMonitor(t *testing.T) *PrometheusMonitor {
	t.Helper()
	monitorLogger = logging.NewLogger()
	pm := &PrometheusMonitor{
		mistUsername:    "mist-user",
		mistAPIPassword: "mist-password",
		sendControlTrigger: func(*ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error) {
			return &control.MistTriggerResult{}, nil
		},
	}
	// Wait for asynchronously retired generations before timing overrides are
	// restored by the outer cleanup. Some fixtures own stopChannel themselves,
	// so the production-wide Stop method is intentionally not used here.
	t.Cleanup(func() {
		pm.stopStreamWS()
		pm.monitorWorkers.Wait()
	})
	return pm
}

func receiveConnection(t *testing.T, connections <-chan *websocket.Conn) *websocket.Conn {
	t.Helper()
	select {
	case conn := <-connections:
		return conn
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WebSocket connection")
		return nil
	}
}

func receiveQuery(t *testing.T, queries <-chan []string) []string {
	t.Helper()
	select {
	case query := <-queries:
		return query
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Mist API query")
		return nil
	}
}

func assertNoQuery(t *testing.T, queries <-chan []string, wait time.Duration) {
	t.Helper()
	select {
	case query := <-queries:
		t.Fatalf("unexpected Mist API query: %v", query)
	case <-time.After(wait):
	}
}

func TestMistControllerStreamWSURL(t *testing.T) {
	got, err := mistControllerStreamWSURL("https://mist.example.test:4242/api?old=1#fragment")
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://mist.example.test:4242/ws?old=1&streams=1"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestAuthenticateStreamWSChallenge(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	received := make(chan []any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON([]any{"auth", false})
		for index := 0; index < 2; index++ {
			var frame []any
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			received <- frame
			if index == 0 {
				_ = conn.WriteJSON([]any{"auth", map[string]any{"status": "CHALL", "challenge": "abc123"}})
			} else {
				_ = conn.WriteJSON([]any{"auth", map[string]any{"status": "OK"}})
			}
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	u.Scheme = "ws"
	conn, response, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := authenticateStreamWS(conn, "mist-user", "mist-password"); err != nil {
		t.Fatal(err)
	}

	first := <-received
	firstAuth := first[1].(map[string]any)
	if firstAuth["username"] != "mist-user" || firstAuth["password"] != "" {
		t.Fatalf("initial auth = %#v", firstAuth)
	}
	second := <-received
	secondAuth := second[1].(map[string]any)
	wantHash := "8855e0e626b288ff542e29f4640636c5"
	if secondAuth["password"] != wantHash {
		t.Fatalf("challenge hash = %q, want %q", secondAuth["password"], wantHash)
	}
}

func TestStreamWSReconnectsWhenPeerStopsResponding(t *testing.T) {
	configureFastStreamWSTest(t)
	pongWait := streamWSPongWait
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	connections := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON([]any{"auth", true})
		connections <- struct{}{}
		// Deliberately never read the client's ping frames, simulating a peer
		// whose TCP connection remains open but whose controller loop is wedged.
		time.Sleep(5 * pongWait)
	}))
	defer server.Close()

	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", server.URL, server.URL)
	for attempt := 0; attempt < 2; attempt++ {
		select {
		case <-connections:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for connection %d", attempt+1)
		}
	}
}

func TestStreamWSReconnectDumpDoesNotRefreshUnchangedStreams(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	first := receiveConnection(t, fake.connections)
	_ = first.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha"}) {
		t.Fatalf("first refresh = %v", got)
	}
	_ = first.Close()
	second := receiveConnection(t, fake.connections)
	_ = second.WriteJSON(streamFrame("live+alpha", 4, 99, 1, 1))
	assertNoQuery(t, fake.queries, 3*streamWSBatchDelay)
}

func TestStreamWSReconnectRunsBootstrapBeforeChangedDumpFrames(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 2)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	first := receiveConnection(t, fake.connections)
	_ = first.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	if got := receiveQuery(t, fake.queries); len(got) != 0 {
		t.Fatalf("initial bootstrap query = %v, want full sweep", got)
	}

	_ = first.Close()
	second := receiveConnection(t, fake.connections)
	_ = second.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 2))
	if got := receiveQuery(t, fake.queries); len(got) != 0 {
		t.Fatalf("reconnect dump query = %v, want bootstrap full sweep", got)
	}
	pm.streamWSMu.Lock()
	queue := pm.streamWSQueue
	pm.streamWSMu.Unlock()
	deadline := time.Now().Add(time.Second)
	for !queue.bootstrapSynced.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !queue.bootstrapSynced.Load() {
		t.Fatal("reconnect bootstrap sweep did not complete")
	}
	_ = second.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 3))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha"}) {
		t.Fatalf("post-reconnect bootstrap query = %v", got)
	}
}

func TestStreamWSBatchesRelevantChangesAndIgnoresViewerOnly(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	fake.setStream("live+beta", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)

	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	_ = conn.WriteJSON(streamFrame("live+beta", 4, 1, 1, 1))
	time.Sleep(2 * streamWSWarmupWindow)
	if got := receiveQuery(t, fake.queries); len(got) != 0 {
		t.Fatalf("bootstrap query = %v, want full sweep", got)
	}
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 99, 1, 1))
	assertNoQuery(t, fake.queries, 3*streamWSBatchDelay)

	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 100, 1, 2))
	_ = conn.WriteJSON(streamFrame("live+beta", 4, 1, 2, 1))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha", "live+beta"}) {
		t.Fatalf("batched query = %v", got)
	}
}

func TestStreamWSBootstrapExpiresUnderContinuousTraffic(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+bootstrap", 4, 1, 2)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)

	_ = conn.WriteJSON(streamFrame("live+bootstrap", 4, 1, 1, 1))
	deadline := time.Now().Add(2 * streamWSWarmupWindow)
	for time.Now().Before(deadline) {
		// Viewer-only frames keep traffic continuous but cannot extend bootstrap.
		_ = conn.WriteJSON(streamFrame("live+bootstrap", 4, 99, 1, 1))
		time.Sleep(streamWSWarmupWindow / 4)
	}
	if got := receiveQuery(t, fake.queries); len(got) != 0 {
		t.Fatalf("bootstrap query = %v, want full sweep", got)
	}
	_ = conn.WriteJSON(streamFrame("live+bootstrap", 4, 99, 1, 2))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+bootstrap"}) {
		t.Fatalf("post-bootstrap query = %v", got)
	}
}

func TestStreamWSTrailingRefreshKeepsFinalState(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	time.Sleep(2 * streamWSWarmupWindow)
	if got := receiveQuery(t, fake.queries); len(got) != 0 {
		t.Fatalf("bootstrap query = %v, want full sweep", got)
	}

	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 2))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha"}) {
		t.Fatalf("first query = %v", got)
	}
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 3))
	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha"}) {
		t.Fatalf("trailing query = %v", got)
	}
}

func TestStreamWSEmptyTargetedResultWaitsForAuthoritativePoll(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	fullSweep := make(chan struct{}, 1)
	pm.streamWSFullSweep = func(string, string) bool { fullSweep <- struct{}{}; return true }
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	time.Sleep(2 * streamWSWarmupWindow)
	<-fullSweep
	fake.deleteStream("live+alpha")
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 2))

	if got := receiveQuery(t, fake.queries); !reflect.DeepEqual(got, []string{"live+alpha"}) {
		t.Fatalf("targeted query = %v", got)
	}
	select {
	case <-fullSweep:
		t.Fatal("empty targeted result amplified ordinary churn into a full sweep")
	case <-time.After(3 * streamWSBatchDelay):
	}
}

func TestStreamWSEndFrameReliesOnAuthoritativePoll(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	fullSweep := make(chan struct{}, 1)
	pm.streamWSFullSweep = func(string, string) bool { fullSweep <- struct{}{}; return true }
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 1))
	time.Sleep(2 * streamWSWarmupWindow)
	<-fullSweep
	_ = conn.WriteJSON(streamFrame("live+alpha", 5, 1, 0, 0))
	select {
	case <-fullSweep:
		t.Fatal("stream-end frame amplified into an all-stream sweep")
	case <-time.After(3 * streamWSBatchDelay):
	}
}

func TestStreamWSFailedFullSweepIsRearmed(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	streamWSInitialBackoff = 30 * time.Millisecond
	streamWSMaxBackoff = 120 * time.Millisecond
	fake := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	attempts := make(chan time.Time, 3)
	var attemptsMu sync.Mutex
	attempt := 0
	pm.streamWSFullSweep = func(string, string) bool {
		attemptsMu.Lock()
		attempt++
		current := attempt
		attemptsMu.Unlock()
		attempts <- time.Now()
		return current >= 3
	}
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamWSMu.Lock()
	queue := pm.streamWSQueue
	pm.streamWSMu.Unlock()
	queue.requestFullSweep()

	var attemptTimes []time.Time
	for want := 1; want <= 3; want++ {
		select {
		case attemptedAt := <-attempts:
			attemptTimes = append(attemptTimes, attemptedAt)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for full sweep attempt %d", want)
		}
	}
	firstDelay := attemptTimes[1].Sub(attemptTimes[0])
	secondDelay := attemptTimes[2].Sub(attemptTimes[1])
	if firstDelay < 20*time.Millisecond || secondDelay < 45*time.Millisecond {
		t.Fatalf("full sweep retries did not back off: first=%s second=%s", firstDelay, secondDelay)
	}
}

func TestStreamWSSuccessfulFullSweepsAreDebounced(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	fake := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	attempts := make(chan time.Time, 2)
	pm.streamWSFullSweep = func(string, string) bool {
		attempts <- time.Now()
		return true
	}
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamWSMu.Lock()
	queue := pm.streamWSQueue
	pm.streamWSMu.Unlock()
	queue.requestFullSweep()
	first := <-attempts
	queue.requestFullSweep()
	select {
	case second := <-attempts:
		if delay := second.Sub(first); delay < streamWSFullSweepDelay {
			t.Fatalf("successful full sweeps were not debounced: %s", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("debounced full sweep did not run")
	}
}

func TestStreamWSStopCancelsBlockedTargetedRefresh(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	fake.queryBlock = make(chan struct{})
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	conn := receiveConnection(t, fake.connections)
	_ = conn.WriteJSON(streamFrame("live+alpha", 4, 1, 1, 2))
	_ = receiveQuery(t, fake.queries)

	removed := make(chan struct{})
	go func() {
		pm.RemoveNode("node-a")
		close(removed)
	}()
	select {
	case <-removed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("node removal waited for the Mist HTTP client timeout")
	}
}

func TestStreamWSNodeReplacementDoesNotWaitForQueuedFullSweep(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	first := newFakeStreamWSServer(t)
	second := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	started := make(chan struct{})
	release := make(chan struct{})
	pm.streamWSFullSweep = func(nodeID, _ string) bool {
		if nodeID != "node-a" {
			t.Errorf("full sweep node = %q, want node-a", nodeID)
		}
		close(started)
		<-release
		return true
	}
	pm.AddNode("node-a", first.server.URL, first.server.URL)
	_ = receiveConnection(t, first.connections)
	pm.streamWSMu.Lock()
	queue := pm.streamWSQueue
	pm.streamWSMu.Unlock()
	queue.requestFullSweep()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queued full sweep did not start")
	}

	replaced := make(chan struct{})
	go func() {
		pm.AddNode("node-b", second.server.URL, second.server.URL)
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("node replacement waited for the old generation full sweep")
	}
	close(release)
	_ = receiveConnection(t, second.connections)
}

func TestStreamObservationRejectsOlderFullPollResponse(t *testing.T) {
	configureFastStreamWSTest(t)
	full := newFakeStreamWSServer(t)
	targeted := newFakeStreamWSServer(t)
	full.setStream("live+alpha", 4, 1, 1)
	targeted.setStream("live+alpha", 4, 2, 1)
	releaseFull := make(chan struct{})
	full.queryBlock = releaseFull

	pm := newStreamWSTestMonitor(t)
	pm.streamWSMu.Lock()
	pm.streamWSGeneration = 1
	pm.streamWSCancel = func() {}
	pm.streamWSMu.Unlock()
	runtime := newMistNodeRuntime(1, "node-a", targeted.server.URL, "mist-user", "mist-password")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	updates := make(chan uint32, 2)
	pm.sendControlTrigger = func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		updates <- trigger.GetStreamLifecycleUpdate().GetTotalInputs()
		return &control.MistTriggerResult{}, nil
	}

	fullClient := mist.NewClient(monitorLogger)
	fullClient.BaseURL = full.server.URL
	fullClient.Username = "mist-user"
	fullClient.Password = "mist-password"
	var fullMu sync.Mutex
	fullDone := make(chan struct{})
	go func() {
		pm.emitStreamLifecycleWithClient(context.Background(), "node-a", full.server.URL, fullClient, &fullMu, func() bool { return true })
		close(fullDone)
	}()
	if got := receiveQuery(t, full.queries); len(got) != 0 {
		t.Fatalf("full query filter = %v, want none", got)
	}
	if result := pm.refreshStreams(context.Background(), 1, runtime, []string{"live+alpha"}); result.needsFullSweep {
		t.Fatal("targeted refresh unexpectedly requested a full sweep")
	}
	select {
	case inputs := <-updates:
		if inputs != 2 {
			t.Fatalf("targeted inputs = %d, want 2", inputs)
		}
	case <-time.After(time.Second):
		t.Fatal("targeted refresh did not emit")
	}
	close(releaseFull)
	select {
	case <-fullDone:
	case <-time.After(time.Second):
		t.Fatal("full poll did not finish")
	}
	select {
	case inputs := <-updates:
		t.Fatalf("stale full poll emitted inputs=%d", inputs)
	case <-time.After(3 * streamWSBatchDelay):
	}
	pm.streamDiffMu.Lock()
	seeded := pm.activeStreamsSeeded
	pm.streamDiffMu.Unlock()
	if !seeded {
		t.Fatal("targeted row freshness prevented authoritative full-poll reconciliation")
	}
}

func TestStreamObservationOrdersClaimThroughSend(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	oldObservation := pm.beginStreamObservation()
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	oldDone := make(chan struct{})
	var orderMu sync.Mutex
	var order []string
	go func() {
		pm.applyStreamObservation("live+alpha", oldObservation, func() {
			close(oldStarted)
			<-releaseOld
			orderMu.Lock()
			order = append(order, "old")
			orderMu.Unlock()
		})
		close(oldDone)
	}()
	<-oldStarted

	targetIssued := make(chan uint64, 1)
	go func() {
		targetIssued <- pm.beginTargetedStreamObservation([]string{"live+alpha"})
	}()
	select {
	case <-targetIssued:
		t.Fatal("newer targeted observation was issued while the older send was in flight")
	case <-time.After(3 * streamWSBatchDelay):
	}
	close(releaseOld)
	<-oldDone
	newObservation := <-targetIssued
	if !pm.applyStreamObservation("live+alpha", newObservation, func() {
		orderMu.Lock()
		order = append(order, "new")
		orderMu.Unlock()
	}) {
		t.Fatal("newer observation was unexpectedly rejected")
	}
	if !reflect.DeepEqual(order, []string{"old", "new"}) {
		t.Fatalf("stream send order = %v", order)
	}
}

func TestFullPollPrunePreservesInFlightTargetedObservations(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	fullObservation := pm.beginStreamObservation()
	firstTargeted := pm.beginTargetedStreamObservation([]string{"live+alpha"})
	_ = pm.beginTargetedStreamObservation([]string{"live+beta"})
	pm.pruneStreamObservations(
		map[string]struct{}{"live+alpha": {}, "live+beta": {}},
		fullObservation,
	)
	if !pm.applyStreamObservation("live+alpha", firstTargeted, func() {}) {
		t.Fatal("authoritative prune discarded a targeted refresh issued while the poll was in flight")
	}
}

func TestFullPollPrunePreservesNewerAbsentTargetedObservation(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	fullObservation := pm.beginStreamObservation()
	targetedObservation := pm.beginTargetedStreamObservation([]string{"live+alpha"})
	pm.pruneStreamObservations(map[string]struct{}{}, fullObservation)
	if !pm.applyStreamObservation("live+alpha", targetedObservation, func() {}) {
		t.Fatal("authoritative prune discarded a newer absent targeted observation")
	}
}

func TestAuthoritativeInventoryPrunesReconnectDedupState(t *testing.T) {
	queue := &streamWSRefreshQueue{
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
		observed:    make(map[string]streamWSObserved, maxStreamWSStateEntries),
		fullSweep:   true,
	}
	observedAt := time.Now()
	for index := 0; index < maxStreamWSStateEntries; index++ {
		queue.observed[fmt.Sprintf("live+old-%d", index)] = streamWSObserved{
			key: streamWSChangeKey{status: 4}, observedAt: observedAt,
		}
	}
	queue.recordAuthoritativeInventory(
		map[string]struct{}{"live+old-0": {}},
		observedAt,
		observedAt.Add(time.Second),
	)
	if len(queue.observed) != 1 {
		t.Fatalf("dedup entries after authoritative prune = %d, want 1", len(queue.observed))
	}
	if !queue.bootstrapSynced.Load() || queue.fullSweep {
		t.Fatal("authoritative inventory did not complete WebSocket bootstrap")
	}
	if _, _, tracked := queue.observe("live+new", streamWSChangeKey{status: 4}); !tracked {
		t.Fatal("new stream remained poll-only after authoritative inventory freed dedup capacity")
	}
}

func TestStreamWSDedupOverflowDegradesToPollingAndRecovers(t *testing.T) {
	queue := &streamWSRefreshQueue{
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
		observed:    make(map[string]streamWSObserved, maxStreamWSStateEntries),
	}
	for index := 0; index < maxStreamWSStateEntries; index++ {
		queue.observed[fmt.Sprintf("live+existing-%d", index)] = streamWSObserved{
			key: streamWSChangeKey{status: 4}, observedAt: time.Now(),
		}
	}
	if _, _, tracked := queue.observe("live+overflow", streamWSChangeKey{status: 4}); tracked {
		t.Fatal("first overflow frame should request one authoritative sweep")
	}
	if !queue.observedOverflow {
		t.Fatal("dedup queue did not enter poll-only overflow mode")
	}
	if _, changed, tracked := queue.observe("live+another", streamWSChangeKey{status: 4}); !tracked || changed {
		t.Fatal("overflow mode should ignore later frames without requesting repeated sweeps")
	}

	largeInventory := make(map[string]struct{}, maxStreamWSStateEntries+1)
	for index := 0; index <= maxStreamWSStateEntries; index++ {
		largeInventory[fmt.Sprintf("live+existing-%d", index)] = struct{}{}
	}
	queue.recordAuthoritativeInventory(largeInventory, time.Now(), time.Now())
	if !queue.observedOverflow {
		t.Fatal("an over-cap authoritative inventory should remain poll-only")
	}

	queue.recordAuthoritativeInventory(
		map[string]struct{}{"live+existing-0": {}},
		time.Now(),
		time.Now(),
	)
	if queue.observedOverflow {
		t.Fatal("bounded authoritative inventory did not restore targeted refreshes")
	}
	if _, _, tracked := queue.observe("live+recovered", streamWSChangeKey{status: 4}); !tracked {
		t.Fatal("new stream was not tracked after overflow recovery")
	}
}

func TestAuthoritativeInventoryPreservesNewerFullSweepRequest(t *testing.T) {
	queue := &streamWSRefreshQueue{
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
		observed:    make(map[string]streamWSObserved),
		fullSweep:   true,
	}
	pollStartedAt := time.Now().Add(-time.Second)
	queue.fullSweepRequestedAt = time.Now()
	queue.recordAuthoritativeInventory(nil, pollStartedAt, time.Now())
	if !queue.fullSweep {
		t.Fatal("inventory cleared a full sweep requested after the poll snapshot")
	}
}

func TestStreamWSQueueOverflowPreservesNewerFullSweepRequest(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "node-a", "http://mist.invalid", "", "")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	_, cancel := context.WithCancel(context.Background())
	pm.streamWSMu.Lock()
	pm.streamWSGeneration = 1
	pm.streamWSCancel = cancel
	pm.streamWSMu.Unlock()
	queue := &streamWSRefreshQueue{
		pm:          pm,
		generation:  1,
		runtime:     runtime,
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time, maxStreamWSStateEntries),
		observed:    make(map[string]streamWSObserved),
		wake:        make(chan struct{}, 1),
	}
	for index := 0; index < maxStreamWSStateEntries; index++ {
		queue.lastRefresh[fmt.Sprintf("live+old-%d", index)] = time.Now()
	}

	pollStartedAt := time.Now()
	queue.nudge("live+overflow")
	queue.recordAuthoritativeInventory(nil, pollStartedAt, time.Now())
	if !queue.fullSweep {
		t.Fatal("inventory cleared the overflow sweep requested after the poll snapshot")
	}
}

func TestStreamWSReadyBatchIsURLBounded(t *testing.T) {
	queue := &streamWSRefreshQueue{
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
	}
	for index := 0; index < 500; index++ {
		queue.pending[fmt.Sprintf("live+%03d-%s", index, strings.Repeat("x", 48))] = time.Now()
	}
	names, _ := queue.takeReady(time.Now())
	if len(names) == 0 || len(names) > maxStreamWSRefreshBatchNames {
		t.Fatalf("batch size = %d, want 1..%d", len(names), maxStreamWSRefreshBatchNames)
	}
	encodedBytes := 0
	for _, name := range names {
		encodedBytes += len(url.QueryEscape(name)) + 6
	}
	if encodedBytes > maxStreamWSRefreshBatchBytes {
		t.Fatalf("encoded batch size = %d, limit %d", encodedBytes, maxStreamWSRefreshBatchBytes)
	}
	if len(queue.pending) == 0 {
		t.Fatal("bounded batch drained every pending stream in one request")
	}
}

func TestStreamWSAuthoritativeAttemptThrottlesMalformedRow(t *testing.T) {
	now := time.Now()
	queue := &streamWSRefreshQueue{
		pending:     map[string]time.Time{"live+bad": now},
		lastRefresh: make(map[string]time.Time),
	}
	queue.markAttempted([]string{"live+bad"}, now)
	if names, wait := queue.takeReady(now.Add(streamWSMinRefresh / 2)); len(names) != 0 || wait <= 0 {
		t.Fatalf("malformed attempted row was not throttled: names=%v wait=%v", names, wait)
	}
}

func TestStreamLifecycleContentionIsNotMistFailure(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "node-a", "http://mist.invalid", "", "")
	pm.streamLifecycleInFlight.Store(true)
	t.Cleanup(func() { pm.streamLifecycleInFlight.Store(false) })
	result := pm.emitStreamLifecycleWithClient(
		context.Background(),
		runtime.nodeID,
		runtime.baseURL,
		runtime.acceleratorClient,
		&runtime.acceleratorMu,
		func() bool { return true },
	)
	if result != streamLifecyclePollContended {
		t.Fatalf("contended lifecycle result = %v, want contention", result)
	}
}

func TestSameNodeReregistrationPreservesPendingOffline(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	fake := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamDiffMu.Lock()
	pm.pendingOfflineNames = map[string]struct{}{"live+vanished": {}}
	pm.lastActiveInternalNames = map[string]struct{}{"live+previous": {}}
	pm.activeStreamsSeeded = true
	pm.streamDiffMu.Unlock()

	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamDiffMu.Lock()
	_, pending := pm.pendingOfflineNames["live+vanished"]
	_, previous := pm.lastActiveInternalNames["live+previous"]
	seeded := pm.activeStreamsSeeded
	pm.streamDiffMu.Unlock()
	if !pending || !previous || !seeded {
		t.Fatalf("same-node re-registration lost reconciliation state: pending=%v previous=%v seeded=%v", pending, previous, seeded)
	}
}

func TestSameNodeRemoveThenAddPreservesPendingOffline(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	fake := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamDiffMu.Lock()
	pm.pendingOfflineNames = map[string]struct{}{"live+vanished": {}}
	pm.lastActiveInternalNames = map[string]struct{}{"live+previous": {}}
	pm.activeStreamsSeeded = true
	pm.streamDiffMu.Unlock()

	pm.RemoveNode("node-a")
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
	pm.streamDiffMu.Lock()
	_, pending := pm.pendingOfflineNames["live+vanished"]
	_, previous := pm.lastActiveInternalNames["live+previous"]
	seeded := pm.activeStreamsSeeded
	pm.streamDiffMu.Unlock()
	if !pending || !previous || !seeded {
		t.Fatalf("same-node remove/add lost reconciliation state: pending=%v previous=%v seeded=%v", pending, previous, seeded)
	}
}

func TestTargetedPresenceParticipatesInVanishDiff(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.pendingOfflineNames = make(map[string]struct{})
	observation := pm.beginStreamObservation()
	pm.recordTargetedStreamPresence("live+ephemeral", observation)

	pm.streamDiffMu.Lock()
	toReport := updatePendingOffline(
		pm.pendingOfflineNames,
		pm.lastActiveInternalNames,
		map[string]struct{}{},
		pm.activeStreamsSeeded,
	)
	pm.streamDiffMu.Unlock()
	if !reflect.DeepEqual(toReport, []string{"ephemeral"}) {
		t.Fatalf("targeted stream vanish = %v, want ephemeral", toReport)
	}
}

func TestNewerTargetedPresenceIsSharedByEveryReconciliationPath(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.pendingOfflineNames = make(map[string]struct{})
	fullObservation := pm.beginStreamObservation()
	targetedObservation := pm.beginStreamObservation()
	pm.recordTargetedStreamPresence("live+alpha", targetedObservation)

	reconciled, toReport := pm.reconcileStreamPresenceSnapshot(
		map[string]struct{}{},
		fullObservation,
	)
	if _, present := reconciled["live+alpha"]; !present {
		t.Fatal("newer targeted stream was absent from the shared reconciliation snapshot")
	}
	if len(toReport) != 0 {
		t.Fatalf("newer targeted stream produced offline reports: %v", toReport)
	}
	if _, present := pm.lastActiveInternalNames["alpha"]; !present {
		t.Fatal("newer targeted stream was absent from the vanish baseline")
	}
}

func TestStreamObservationStateIsBounded(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	names := make([]string, maxStreamObservationEntries+257)
	for index := range names {
		names[index] = fmt.Sprintf("live+stream-%d", index)
	}
	observation := pm.beginTargetedStreamObservation(names)
	pm.streamObservationMu.Lock()
	defer pm.streamObservationMu.Unlock()
	if observation != 0 || !pm.streamObservationOverflow {
		t.Fatalf("oversized targeted observation = %d overflow=%v, want explicit full-poll fallback", observation, pm.streamObservationOverflow)
	}
	if len(pm.streamObservationIssued) > maxStreamObservationEntries {
		t.Fatalf("issued observation map grew to %d entries", len(pm.streamObservationIssued))
	}
}

func TestStreamObservationOverflowStaysPollOnlyUntilBoundedAuthoritativePoll(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	oldObservation := pm.beginStreamObservation()
	names := make([]string, maxStreamObservationEntries+1)
	for index := range names {
		names[index] = fmt.Sprintf("live+stream-%d", index)
	}
	if got := pm.beginTargetedStreamObservation(names); got != 0 {
		t.Fatalf("oversized targeted observation = %d, want fallback sentinel", got)
	}
	if pm.claimStreamObservation("live+stale", oldObservation) {
		t.Fatal("observation older than the overflow floor was accepted")
	}

	fullObservation := pm.beginStreamObservation()
	if !pm.claimStreamObservation("live+fresh", fullObservation) {
		t.Fatal("authoritative full-poll observation was rejected in overflow mode")
	}
	largePresent := make(map[string]struct{}, len(names))
	for _, name := range names {
		largePresent[name] = struct{}{}
	}
	pm.pruneStreamObservations(largePresent, fullObservation)
	if got := pm.beginTargetedStreamObservation([]string{"live+fresh"}); got != 0 {
		t.Fatalf("targeted acceleration resumed above the state cap: %d", got)
	}

	boundedObservation := pm.beginStreamObservation()
	pm.pruneStreamObservations(map[string]struct{}{"live+fresh": {}}, boundedObservation)
	if got := pm.beginTargetedStreamObservation([]string{"live+fresh"}); got == 0 {
		t.Fatal("targeted acceleration did not resume after a bounded authoritative poll")
	}
}

func TestTargetedRefreshFailureRequiresFullSweepWithoutCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("command"), "authorize") {
			_ = json.NewEncoder(w).Encode(map[string]any{"authorize": map[string]any{"status": "OK"}})
			return
		}
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "node-a", server.URL, "mist-user", "mist-password")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	pm.streamWSMu.Lock()
	pm.streamWSGeneration = 1
	pm.streamWSCancel = func() {}
	pm.streamWSMu.Unlock()

	pollObservation := pm.beginStreamObservation()
	result := pm.refreshStreams(context.Background(), 1, runtime, []string{"live+alpha"})
	if result.completed || !result.needsFullSweep {
		t.Fatalf("failed targeted refresh result = %+v", result)
	}
	if !pm.applyStreamObservation("live+alpha", pollObservation, func() {}) {
		t.Fatal("failed targeted request suppressed the in-flight full-poll row")
	}
}

func TestTargetedRefreshKeepsRequestOrderAcrossMistFetch(t *testing.T) {
	fake := newFakeStreamWSServer(t)
	fake.setStream("live+alpha", 4, 1, 1)
	release := make(chan struct{})
	fake.queryBlock = release
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "node-a", fake.server.URL, "mist-user", "mist-password")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	pm.streamWSMu.Lock()
	pm.streamWSGeneration = 1
	pm.streamWSCancel = func() {}
	pm.streamWSMu.Unlock()

	resultCh := make(chan streamRefreshResult, 1)
	go func() {
		resultCh <- pm.refreshStreams(context.Background(), 1, runtime, []string{"live+alpha"})
	}()
	_ = receiveQuery(t, fake.queries)
	pollObservation := pm.beginStreamObservation()
	pm.pruneStreamObservations(map[string]struct{}{}, pollObservation)
	close(release)
	result := <-resultCh
	if result.completed || !result.needsFullSweep || len(result.refreshed) != 0 {
		t.Fatalf("targeted result = %+v, want stale pre-poll response rejected", result)
	}
}

func TestAuthoritativeInventoryPreservesNudgesNewerThanPollSnapshot(t *testing.T) {
	queue := &streamWSRefreshQueue{
		pending:     make(map[string]time.Time),
		lastRefresh: make(map[string]time.Time),
		observed:    make(map[string]streamWSObserved),
		wake:        make(chan struct{}, 1),
	}
	pollStartedAt := time.Now().Add(-time.Second)
	queue.observe("live+new", streamWSChangeKey{status: 4, inputs: 1})
	queue.mu.Lock()
	queue.pending["live+new"] = time.Now()
	queue.mu.Unlock()
	queue.recordAuthoritativeInventory(map[string]struct{}{}, pollStartedAt, time.Now())

	queue.mu.Lock()
	_, observed := queue.observed["live+new"]
	_, pending := queue.pending["live+new"]
	queue.mu.Unlock()
	if !observed || !pending {
		t.Fatalf("newer frame pruned by older inventory: observed=%v pending=%v", observed, pending)
	}
}

func TestNodeMetricsForwardingIsCoalescedAndDoesNotBlockUpdates(t *testing.T) {
	oldTimeout := nodeMetricsForwardTimeout
	nodeMetricsForwardTimeout = time.Second
	t.Cleanup(func() { nodeMetricsForwardTimeout = oldTimeout })
	pm := newStreamWSTestMonitor(t)
	pm.stopChannel = make(chan bool)
	pm.updateChannel = make(chan models.NodeUpdate, 4)
	pm.nodeMetricsWake = make(chan struct{}, 1)
	runtime := newMistNodeRuntime(1, "node-a", "http://mist.invalid", "", "")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	pm.nodeID = runtime.nodeID
	pm.baseURL = runtime.baseURL

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	forwarded := make(chan uint32, 3)
	var callsMu sync.Mutex
	calls := 0
	pm.sendControlTriggerCtx = func(ctx context.Context, trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		callsMu.Lock()
		calls++
		call := calls
		callsMu.Unlock()
		if call == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		forwarded <- trigger.GetNodeLifecycleUpdate().GetCpuTenths()
		return &control.MistTriggerResult{}, nil
	}
	updatesDone := make(chan struct{})
	forwardDone := make(chan struct{})
	go func() { pm.processUpdates(); close(updatesDone) }()
	go func() { pm.forwardNodeMetricsLoop(); close(forwardDone) }()

	pm.updateChannel <- models.NodeUpdate{NodeID: "node-a", BaseURL: runtime.baseURL, JSONData: map[string]any{"cpu": float64(1)}}
	<-firstStarted
	pm.updateChannel <- models.NodeUpdate{NodeID: "node-a", BaseURL: runtime.baseURL, JSONData: map[string]any{"cpu": float64(2)}}
	pm.updateChannel <- models.NodeUpdate{NodeID: "node-a", BaseURL: runtime.baseURL, JSONData: map[string]any{"cpu": float64(3)}}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		pm.mutex.RLock()
		cpu := pm.lastJSONData["cpu"]
		pm.mutex.RUnlock()
		if cpu == float64(3) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("update consumer stopped while node metric send was blocked")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirst)
	if first := <-forwarded; first != 10 {
		t.Fatalf("first forwarded CPU = %d, want 10", first)
	}
	select {
	case latest := <-forwarded:
		if latest != 30 {
			t.Fatalf("coalesced forwarded CPU = %d, want 30", latest)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced latest node metrics were not forwarded")
	}
	close(pm.stopChannel)
	<-updatesDone
	<-forwardDone
}

func TestNodeMetricsForwarderShutdownBoundsUncooperativeSend(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.stopChannel = make(chan bool)
	pm.nodeMetricsWake = make(chan struct{}, 1)
	runtime := newMistNodeRuntime(1, "node-a", "http://mist.invalid", "", "")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	pm.nodeID = runtime.nodeID
	pm.lastJSONData = map[string]any{"cpu": float64(1)}
	pm.nodeMetricsVersion.Store(1)

	started := make(chan struct{})
	release := make(chan struct{})
	pm.sendControlTriggerCtx = func(context.Context, *ipcpb.MistTrigger, logging.Logger) (*control.MistTriggerResult, error) {
		close(started)
		<-release // Deliberately ignores cancellation, like a wedged transport SendMsg.
		return &control.MistTriggerResult{}, nil
	}
	done := make(chan struct{})
	go func() { pm.forwardNodeMetricsLoop(); close(done) }()
	pm.requestNodeMetricsForward()
	<-started
	runtime.cancel()
	close(pm.stopChannel)
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("joined node-metrics worker waited for an uncooperative transport send")
	}
	if !pm.nodeMetricsSendInFlight.Load() {
		t.Fatal("uncooperative transport attempt was not retained in its bounded slot")
	}
	close(release)
	deadline := time.Now().Add(250 * time.Millisecond)
	for pm.nodeMetricsSendInFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if pm.nodeMetricsSendInFlight.Load() {
		t.Fatal("transport slot was not released when the send returned")
	}
}

func TestNodeRuntimeStopCancelsActiveLifecycleSend(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	runtime := newMistNodeRuntime(1, "node-a", "http://mist.invalid", "", "")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	started := make(chan struct{})
	pm.sendControlTriggerCtx = func(ctx context.Context, _ *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !runtime.launch(func(ctx context.Context) {
		_, _ = pm.sendMistTriggerContext(ctx, &ipcpb.MistTrigger{})
	}) {
		t.Fatal("runtime rejected lifecycle task")
	}
	<-started
	stopped := make(chan struct{})
	go func() {
		runtime.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("runtime stop waited on a lifecycle send after cancellation")
	}
}

func TestAddNodePublishesReplacementWithoutWaitingForRetiredWrite(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	retired := newMistNodeRuntime(1, "node-a", "http://mist-a.invalid", "", "")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = retired
	pm.nodeRuntimeMu.Unlock()
	pm.nodeID = retired.nodeID
	pm.baseURL = retired.baseURL
	started := make(chan struct{})
	release := make(chan struct{})
	if !retired.launch(func(context.Context) {
		close(started)
		<-release
	}) {
		t.Fatal("retired runtime rejected test write")
	}
	<-started

	added := make(chan struct{})
	go func() {
		pm.AddNode("node-b", "http://mist-b.invalid", "http://mist-b.invalid")
		close(added)
	}()
	select {
	case <-added:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("AddNode waited for a retired runtime write")
	}
	if runtime := pm.currentNodeRuntime(); runtime == nil || runtime.nodeID != "node-b" {
		close(release)
		t.Fatalf("replacement runtime = %#v, want node-b", runtime)
	}
	close(release)
}

func TestProcessUpdatesRejectsSameNodeFromRetiredAddress(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.stopChannel = make(chan bool)
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	pm.nodeMetricsWake = make(chan struct{}, 1)
	pm.nodeID = "node-a"
	pm.baseURL = "http://mist-new.invalid"
	pm.lastJSONData = map[string]any{"cpu": float64(7)}
	done := make(chan struct{})
	go func() {
		pm.processUpdates()
		close(done)
	}()
	pm.updateChannel <- models.NodeUpdate{
		NodeID: "node-a", BaseURL: "http://mist-old.invalid", JSONData: map[string]any{"cpu": float64(99)},
	}
	time.Sleep(20 * time.Millisecond)
	pm.mutex.RLock()
	cpu := pm.lastJSONData["cpu"]
	pm.mutex.RUnlock()
	if cpu != float64(7) {
		t.Fatalf("retired-address update overwrote current snapshot: cpu=%v", cpu)
	}
	close(pm.stopChannel)
	<-done
}

func TestPrometheusMonitorStopCancelsNodeRuntimeBeforeReturning(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	pm := newStreamWSTestMonitor(t)
	pm.stopChannel = make(chan bool)
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	runtime := newMistNodeRuntime(1, "node-a", server.URL, "mist-user", "mist-password")
	pm.nodeRuntimeMu.Lock()
	pm.nodeRuntime = runtime
	pm.nodeRuntimeMu.Unlock()
	runtime.launch(func(context.Context) { pm.emitNodeLifecycleRuntime(runtime) })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("node poll did not start")
	}

	stopped := make(chan struct{})
	go func() {
		pm.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop did not cancel and join the node runtime")
	}
}

func TestNodeMutationsAreIgnoredAfterMonitorStop(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.stopChannel = make(chan bool)
	pm.Stop()

	pm.streamWSMu.Lock()
	streamGeneration := pm.streamWSGeneration
	pm.streamWSMu.Unlock()
	pm.nodeRuntimeMu.RLock()
	nodeGeneration := pm.nextNodeGeneration
	pm.nodeRuntimeMu.RUnlock()

	pm.AddNode("node-after-stop", "http://mist.invalid", "http://mist.invalid")
	pm.RemoveNode("node-after-stop")

	if runtime := pm.currentNodeRuntime(); runtime != nil {
		t.Fatalf("post-stop node mutation published runtime %#v", runtime)
	}
	pm.streamWSMu.Lock()
	gotStreamGeneration := pm.streamWSGeneration
	pm.streamWSMu.Unlock()
	pm.nodeRuntimeMu.RLock()
	gotNodeGeneration := pm.nextNodeGeneration
	pm.nodeRuntimeMu.RUnlock()
	if gotStreamGeneration != streamGeneration || gotNodeGeneration != nodeGeneration {
		t.Fatalf(
			"post-stop node mutation changed generations: stream %d->%d node %d->%d",
			streamGeneration,
			gotStreamGeneration,
			nodeGeneration,
			gotNodeGeneration,
		)
	}
}

func TestStreamWSReconnectsAndRejectsStaleGeneration(t *testing.T) {
	configureFastStreamWSTest(t)
	streamWSWarmupWindow = 0
	first := newFakeStreamWSServer(t)
	second := newFakeStreamWSServer(t)
	second.setStream("live+fresh", 4, 1, 1)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", first.server.URL, first.server.URL)
	oldConn := receiveConnection(t, first.connections)
	pm.streamWSMu.Lock()
	oldGeneration := pm.streamWSGeneration
	pm.streamWSMu.Unlock()
	oldRuntime := pm.currentNodeRuntime()

	pm.AddNode("node-b", second.server.URL, second.server.URL)
	newConn := receiveConnection(t, second.connections)
	pm.refreshStreams(context.Background(), oldGeneration, oldRuntime, []string{"live+stale"})
	assertNoQuery(t, first.queries, 3*streamWSBatchDelay)
	_ = oldConn.WriteJSON(streamFrame("live+stale", 4, 1, 1, 2))
	assertNoQuery(t, second.queries, 3*streamWSBatchDelay)

	_ = newConn.Close()
	_ = receiveConnection(t, second.connections)
}

func TestStreamWSAlwaysDialsForConfiguredNode(t *testing.T) {
	configureFastStreamWSTest(t)
	fake := newFakeStreamWSServer(t)
	pm := newStreamWSTestMonitor(t)
	pm.AddNode("node-a", fake.server.URL, fake.server.URL)
	_ = receiveConnection(t, fake.connections)
}

func TestNodeReplacementResetsNodeScopedStreamState(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.lastActiveInternalNames = map[string]struct{}{"old-stream": {}}
	pm.pendingOfflineNames = map[string]struct{}{"old-stream": {}}
	pm.admittedRuntimeMissing = map[admittedRuntimeIdentity]time.Time{{runtimeName: "old-stream"}: time.Now()}
	pm.activeStreamsSeeded = true
	pm.streamObservationLast = map[string]uint64{"live+old-stream": 9}
	pm.streamObservationIssued = map[string]uint64{"live+old-stream": 10}
	pm.streamObservationNext.Store(10)

	pm.AddNode("node-b", "http://mist-b.invalid", "http://mist-b.invalid")
	if pm.activeStreamsSeeded || len(pm.lastActiveInternalNames) != 0 || len(pm.pendingOfflineNames) != 0 || len(pm.admittedRuntimeMissing) != 0 {
		t.Fatalf("node-scoped reconciliation state was retained across replacement")
	}
	if pm.streamObservationNext.Load() != 10 || len(pm.streamObservationLast) != 0 || len(pm.streamObservationIssued) != 0 {
		t.Fatalf("stream observation maps were retained or monotonic counter reset across replacement")
	}
}

func TestNodeStateResetDoesNotReleaseActiveLifecyclePoll(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.streamLifecycleInFlight.Store(true)
	pm.resetNodeScopedStreamState(false)
	if !pm.streamLifecycleInFlight.Load() {
		t.Fatal("node reset released a lifecycle-poll claim still owned by a retired runtime")
	}
	pm.streamLifecycleInFlight.Store(false)
}

func TestSameNodeStateResetKeepsObservationCounterAheadOfRetainedPresence(t *testing.T) {
	pm := newStreamWSTestMonitor(t)
	pm.targetedActiveNames = map[string]uint64{"live+alpha": 42}
	pm.streamObservationNext.Store(42)
	pm.resetNodeScopedStreamState(true)
	if observation := pm.beginStreamObservation(); observation != 43 {
		t.Fatalf("observation after same-node reset = %d, want 43", observation)
	}
}
