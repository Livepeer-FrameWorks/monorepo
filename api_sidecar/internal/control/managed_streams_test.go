package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestMistEntryToSnapshot_ParsesStreamIDTag locks the parse half of the
// Apply→Mist-config→Register round trip: Mist persists the
// `fw:stream:<id>` tag, and hydration must read stream_id back out so
// Foghorn can key its lastSent map by stream_id (matching the reconciler's
// runtime key). Without it, post-restart hydration would land Foghorn's
// state under the bare Mist name and cause the next tick to Apply
// (under stream_id) then Retract (the bare-name hydrated entry) the same
// physical stream.
func TestMistEntryToSnapshot_ParsesStreamIDTag(t *testing.T) {
	entry := map[string]interface{}{
		"source":    "ts-exec:cat /dev/null",
		"always_on": true,
		"realtime":  false,
		"tags": []interface{}{
			managedStreamOwnerTag,
			"ingest:mist_native",
			managedStreamIDTagPrefix + "stream-uuid-from-mist",
		},
	}
	snap := mistEntryToSnapshot(entry)
	if snap.streamID != "stream-uuid-from-mist" {
		t.Fatalf("stream_id not parsed from tags; got %q", snap.streamID)
	}
	if snap.ingestMode != "mist_native" {
		t.Fatalf("ingest mode not parsed from tags; got %q", snap.ingestMode)
	}
	if snap.source != "ts-exec:cat /dev/null" {
		t.Fatalf("source not preserved: %q", snap.source)
	}
}

// TestMistEntryToSnapshot_NoStreamIDTagLeavesEmpty covers the legacy case
// where a Mist config predates the stream_id tag convention. Snapshot is
// still returned (owner-tag and ingest-mode preserved) but stream_id stays
// empty. The Foghorn hydrator skips entries without stream_id rather than
// keying lastSent by the bare name.
func TestMistEntryToSnapshot_NoStreamIDTagLeavesEmpty(t *testing.T) {
	entry := map[string]interface{}{
		"source":    "ts-exec:cat /dev/null",
		"always_on": true,
		"tags": []interface{}{
			managedStreamOwnerTag,
			"ingest:mist_native",
		},
	}
	snap := mistEntryToSnapshot(entry)
	if snap.streamID != "" {
		t.Fatalf("expected empty stream_id when tag absent; got %q", snap.streamID)
	}
	if snap.ingestMode != "mist_native" {
		t.Fatalf("ingest mode lost: %q", snap.ingestMode)
	}
}

// TestHandleApplyManagedStream_WritesStreamIDTag covers the produce half of
// the round trip: a Foghorn-sent Apply with a stream_id must end up tagged
// on the Mist stream so future hydration can recover it.
func TestHandleApplyManagedStream_WritesStreamIDTag(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	handleApplyManagedStream(logger, &ipcpb.ApplyManagedStream{
		Name:       "frameworks-demo",
		Source:     "ts-exec:cat /dev/null",
		AlwaysOn:   true,
		Tags:       []string{"ingest:mist_native"},
		IngestMode: "mist_native",
		StreamId:   "stream-uuid-applied",
	})

	calls := mock.callsContainingKey("addstream")
	if len(calls) != 1 {
		t.Fatalf("want 1 addstream call, got %d", len(calls))
	}
	addstream := calls[0]["addstream"].(map[string]any)
	demo := addstream["frameworks-demo"].(map[string]any)
	tags := demo["tags"].([]any)
	var hasStreamID bool
	for _, t := range tags {
		if s, ok := t.(string); ok && s == managedStreamIDTagPrefix+"stream-uuid-applied" {
			hasStreamID = true
			break
		}
	}
	if !hasStreamID {
		t.Fatalf("Apply did not embed stream_id tag in Mist config; tags=%v", tags)
	}
}

func TestNormalizeTagsAlwaysIncludesOwnerAndDedupes(t *testing.T) {
	got := normalizeTags([]string{"a", "ingest:mist_native", "a"}, "")
	if !slices.Contains(got, managedStreamOwnerTag) {
		t.Fatalf("missing owner tag: %v", got)
	}
	if !slices.Contains(got, "a") || !slices.Contains(got, "ingest:mist_native") {
		t.Fatalf("missing input tag: %v", got)
	}
	count := 0
	for _, tag := range got {
		if tag == "a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected dedup, got %v", got)
	}
}

func TestNormalizeTagsStableOrderEnablesIdempotentCompare(t *testing.T) {
	a := normalizeTags([]string{"zeta", "alpha", "ingest:mist_native"}, "stream-uuid")
	b := normalizeTags([]string{"ingest:mist_native", "alpha", "zeta"}, "stream-uuid")
	if !slices.Equal(a, b) {
		t.Fatalf("tag normalization not stable: %v vs %v", a, b)
	}
}

type mockMistServer struct {
	mu       sync.Mutex
	requests []map[string]any
	srv      *httptest.Server
}

func newMockMistServer(t *testing.T) *mockMistServer {
	return newMockMistServerWithStreams(t, nil)
}

// newMockMistServerWithStreams returns a mock that also responds to
// `config_backup` with a synthetic config containing the supplied streams.
// Used to exercise post-restart hydration of appliedManagedStreams.
func newMockMistServerWithStreams(t *testing.T, streams map[string]map[string]any) *mockMistServer {
	t.Helper()
	m := &mockMistServer{}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("command")
		var parsed map[string]any
		if cmd != "" {
			_ = json.Unmarshal([]byte(cmd), &parsed)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &parsed)
		}
		m.mu.Lock()
		m.requests = append(m.requests, parsed)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if _, ok := parsed["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if _, ok := parsed["config_backup"]; ok {
			resp := map[string]any{"config_backup": map[string]any{"streams": streams}}
			buf, _ := json.Marshal(resp)
			_, _ = w.Write(buf)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mockMistServer) callsContainingKey(key string) []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []map[string]any{}
	for _, req := range m.requests {
		if _, ok := req[key]; ok {
			out = append(out, req)
		}
	}
	return out
}

func withMockMistAndCleanState(t *testing.T) *mockMistServer {
	t.Helper()
	mock := newMockMistServer(t)
	prev := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL}
	t.Cleanup(func() { currentConfig = prev })

	appliedManagedStreams.Lock()
	saved := appliedManagedStreams.m
	appliedManagedStreams.m = make(map[string]managedStreamLocalSnapshot)
	appliedManagedStreams.Unlock()
	t.Cleanup(func() {
		appliedManagedStreams.Lock()
		appliedManagedStreams.m = saved
		appliedManagedStreams.Unlock()
	})
	return mock
}

func TestHandleApplyManagedStream_FirstApplyAddsStream(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	handleApplyManagedStream(logger, &ipcpb.ApplyManagedStream{
		Name:       "frameworks-demo",
		Source:     "ts-exec:ffmpeg -re -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -",
		AlwaysOn:   true,
		Tags:       []string{"ingest:mist_native"},
		IngestMode: "mist_native",
		StreamId:   "stream-uuid",
		TenantId:   "tenant-uuid",
	})

	calls := mock.callsContainingKey("addstream")
	if len(calls) != 1 {
		t.Fatalf("want 1 addstream call, got %d (requests=%+v)", len(calls), mock.requests)
	}
	addstream, _ := calls[0]["addstream"].(map[string]any)
	if _, ok := addstream["frameworks-demo"]; !ok {
		t.Fatalf("addstream did not include frameworks-demo: %+v", addstream)
	}
	if saves := mock.callsContainingKey("save"); len(saves) != 1 {
		t.Fatalf("Apply must persist Mist config with save; got %d save calls", len(saves))
	}
}

func TestHandleApplyManagedStream_IdempotentRepeat(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	req := &ipcpb.ApplyManagedStream{
		Name:       "frameworks-demo",
		Source:     "ts-exec:ffmpeg -re -i clip.mp4 -c copy -f mpegts -",
		AlwaysOn:   true,
		Tags:       []string{"ingest:mist_native"},
		IngestMode: "mist_native",
	}
	handleApplyManagedStream(logger, req)
	handleApplyManagedStream(logger, req)

	calls := mock.callsContainingKey("addstream")
	if len(calls) != 1 {
		t.Fatalf("repeat-Apply with identical fields should be a noop; got %d addstream calls", len(calls))
	}
	if saves := mock.callsContainingKey("save"); len(saves) != 1 {
		t.Fatalf("repeat-Apply should only save the first materialization; got %d save calls", len(saves))
	}
}

func TestHandleApplyManagedStream_FieldChangeReAdds(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	mkReq := func(source string) *ipcpb.ApplyManagedStream {
		return &ipcpb.ApplyManagedStream{
			Name:       "frameworks-demo",
			Source:     source,
			AlwaysOn:   true,
			Tags:       []string{"ingest:mist_native"},
			IngestMode: "mist_native",
		}
	}
	handleApplyManagedStream(logger, mkReq("ts-exec:cat /dev/null"))
	handleApplyManagedStream(logger, mkReq("ts-exec:cat /dev/zero"))

	calls := mock.callsContainingKey("addstream")
	if len(calls) != 2 {
		t.Fatalf("field change should trigger a second addstream; got %d calls", len(calls))
	}
	if saves := mock.callsContainingKey("save"); len(saves) != 2 {
		t.Fatalf("field change should persist both materializations; got %d save calls", len(saves))
	}
}

func TestHandleRetractManagedStream_UnknownNameIsNoop(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	handleRetractManagedStream(logger, &ipcpb.RetractManagedStream{
		Name:     "tenant-rtmp-pushed-stream",
		StreamId: "spoofed-id",
	})

	if len(mock.callsContainingKey("deletestream")) != 0 {
		t.Fatalf("retract for unknown name must not call deletestream: %+v", mock.requests)
	}
}

// TestHydrateAppliedManagedStreams_RejectsOwnerTagWithoutStreamID asserts
// the hydration footgun guard: a Mist stream config with the owner tag but
// no fw:stream:<id> is NOT adopted as managed. Owner tag alone would let an
// accidentally-tagged stream be name-matched by a future Retract.
func TestHydrateAppliedManagedStreams_RejectsOwnerTagWithoutStreamID(t *testing.T) {
	mock := newMockMistServerWithStreams(t, map[string]map[string]any{
		"orphan-owner-tag": {
			"source": "push://",
			"tags":   []any{managedStreamOwnerTag, "ingest:mist_native"},
		},
	})
	prevCfg := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL}
	t.Cleanup(func() { currentConfig = prevCfg })

	appliedManagedStreams.Lock()
	saved := appliedManagedStreams.m
	appliedManagedStreams.m = make(map[string]managedStreamLocalSnapshot)
	appliedManagedStreams.Unlock()
	t.Cleanup(func() {
		appliedManagedStreams.Lock()
		appliedManagedStreams.m = saved
		appliedManagedStreams.Unlock()
	})

	HydrateAppliedManagedStreamsFromMist(logging.NewLogger())

	appliedManagedStreams.Lock()
	_, claimed := appliedManagedStreams.m["orphan-owner-tag"]
	appliedManagedStreams.Unlock()
	if claimed {
		t.Fatalf("hydrate must NOT claim ownership of owner-tagged stream missing fw:stream:<id>")
	}
}

// TestHydrateAppliedManagedStreams_RecoversAfterRestart asserts the
// post-restart retract path: a sidecar restart starts with an empty
// in-memory map, but Mist still has the previously-Applied stream
// configured (owner-tagged). Hydrate walks Mist's config and repopulates
// appliedManagedStreams so a subsequent Foghorn Retract correctly fires
// deletestream against Mist instead of being silently dropped.
func TestHydrateAppliedManagedStreams_RecoversAfterRestart(t *testing.T) {
	mock := newMockMistServerWithStreams(t, map[string]map[string]any{
		"frameworks-demo": {
			"source":    "ts-exec:cat /dev/null",
			"always_on": true,
			"tags": []any{
				managedStreamOwnerTag,
				"ingest:mist_native",
				managedStreamIDTagPrefix + "stream-uuid-from-mist",
			},
		},
		// A tenant push stream that is NOT ours — must be ignored.
		"some-tenant-stream": {
			"source": "push://",
			"tags":   []any{"live"},
		},
	})
	prevCfg := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL}
	t.Cleanup(func() { currentConfig = prevCfg })

	appliedManagedStreams.Lock()
	saved := appliedManagedStreams.m
	appliedManagedStreams.m = make(map[string]managedStreamLocalSnapshot)
	appliedManagedStreams.Unlock()
	t.Cleanup(func() {
		appliedManagedStreams.Lock()
		appliedManagedStreams.m = saved
		appliedManagedStreams.Unlock()
	})

	logger := logging.NewLogger()

	HydrateAppliedManagedStreamsFromMist(logger)

	appliedManagedStreams.Lock()
	demoSnap, ownsDemo := appliedManagedStreams.m["frameworks-demo"]
	_, ownsTenant := appliedManagedStreams.m["some-tenant-stream"]
	appliedManagedStreams.Unlock()
	if !ownsDemo {
		t.Fatalf("hydrate should recover owner-tagged stream into map")
	}
	if ownsTenant {
		t.Fatalf("hydrate must NOT claim ownership of non-tagged tenant streams")
	}
	// Stream ID must round-trip from Mist config tags so the
	// Register-time snapshot can ship it to Foghorn for lastSent keying.
	if demoSnap.streamID != "stream-uuid-from-mist" {
		t.Fatalf("hydrated snapshot lost stream_id from tags; got %q", demoSnap.streamID)
	}
	// And the snapshot Foghorn would receive on Register must carry it.
	registerSet := snapshotAppliedManagedStreamsForRegister()
	if len(registerSet) != 1 || registerSet[0].GetStreamId() != "stream-uuid-from-mist" {
		t.Fatalf("Register snapshot must carry stream_id; got %+v", registerSet)
	}

	// Now Retract: must call deletestream because the map contains the entry.
	handleRetractManagedStream(logger, &ipcpb.RetractManagedStream{
		Name:     "frameworks-demo",
		StreamId: "stream-uuid",
	})
	if len(mock.callsContainingKey("deletestream")) != 1 {
		t.Fatalf("post-hydrate retract must call deletestream once; got: %+v", mock.requests)
	}
}

func TestHandleRetractManagedStream_KnownNameDeletes(t *testing.T) {
	mock := withMockMistAndCleanState(t)
	logger := logging.NewLogger()

	apply := &ipcpb.ApplyManagedStream{
		Name:       "frameworks-demo",
		Source:     "ts-exec:cat /dev/null",
		AlwaysOn:   true,
		Tags:       []string{"ingest:mist_native"},
		IngestMode: "mist_native",
	}
	handleApplyManagedStream(logger, apply)

	handleRetractManagedStream(logger, &ipcpb.RetractManagedStream{
		Name:     "frameworks-demo",
		StreamId: "stream-uuid",
	})

	if len(mock.callsContainingKey("deletestream")) != 1 {
		t.Fatalf("known-name retract should call deletestream once: %+v", mock.requests)
	}
	if saves := mock.callsContainingKey("save"); len(saves) != 2 {
		t.Fatalf("apply and retract should both save Mist config; got %d save calls", len(saves))
	}
	appliedManagedStreams.Lock()
	_, present := appliedManagedStreams.m["frameworks-demo"]
	appliedManagedStreams.Unlock()
	if present {
		t.Fatalf("local map should drop the entry after successful retract")
	}
}
