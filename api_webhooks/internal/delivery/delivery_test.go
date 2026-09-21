package delivery

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/webhooksig"

	"google.golang.org/protobuf/proto"
)

func storedClipReady(t *testing.T) ledger.StoredEvent {
	t.Helper()
	msg := &publicv1.ClipReady{Artifact: &publicv1.Artifact{ArtifactId: "a1", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: "s1"}, DurationMs: 1500, SizeBytes: 42}
	payload, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := events.SpecFor(msg)
	if !ok {
		t.Fatal("ClipReady is not registered")
	}
	return ledger.StoredEvent{ID: "0190aaaa-0000-7000-8000-000000000001", Type: "clip.ready", SchemaName: string(spec.MessageName), Payload: payload, OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 123000, time.UTC)}
}

func TestRenderIsTheProtoJSONEnvelope(t *testing.T) {
	ev := storedClipReady(t)
	first, err := Render(ev, "v1")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := Render(ev, "v1")
	if string(first) != string(second) {
		t.Fatal("two renders of one event differ; the signed bytes must be stable")
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(first, &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "type", "api_version", "created_at", "data"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("body %s lacks %q", first, key)
		}
	}
	if string(body["created_at"]) != `"2026-09-19T10:00:00.000123Z"` || string(body["type"]) != `"clip.ready"` || string(body["api_version"]) != `"v1"` {
		t.Fatalf("envelope = %s", first)
	}
	var data map[string]any
	if err := json.Unmarshal(body["data"], &data); err != nil {
		t.Fatal(err)
	}
	artifact, _ := data["artifact"].(map[string]any)
	if data["durationMs"] != "1500" || artifact["kind"] != "ARTIFACT_KIND_CLIP" || artifact["playbackId"] != "" {
		t.Fatalf("data = %s; want protojson names, enum names, int64 as strings, and unset fields present", body["data"])
	}
	if _, err := Render(ev, "v2"); err == nil {
		t.Fatal("an unknown api version rendered")
	}
	ev.SchemaName = "frameworks.events.public.v1.ClipFailed"
	if _, err := Render(ev, "v1"); err == nil {
		t.Fatal("a payload stored under another schema rendered")
	}
}

func loopbackPolicy(t *testing.T, srv *httptest.Server) restream.DestinationPolicy {
	t.Helper()
	host, _, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return restream.DestinationPolicy{AllowedCIDRs: []*net.IPNet{{IP: net.ParseIP(host).To4(), Mask: net.CIDRMask(32, 32)}}}
}

func rootCAs(srv *httptest.Server) ClientOptions {
	return ClientOptions{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
}

func TestSenderSignsAndClassifies(t *testing.T) {
	var hits atomic.Int64
	status := atomic.Int64{}
	status.Store(200)
	var lastBody []byte
	var lastHeader http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastBody, _ = io.ReadAll(r.Body)
		lastHeader = r.Header.Clone()
		if status.Load() == http.StatusFound {
			http.Redirect(w, r, "https://elsewhere.example/", http.StatusFound)
			return
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write(make([]byte, 8<<10))
	}))
	defer srv.Close()
	opts := rootCAs(srv)
	opts.Policy = loopbackPolicy(t, srv)
	opts.OnlyAddress = srv.Listener.Addr().String()
	sender := &Sender{HTTP: NewHTTPClient(opts)}
	key, _ := webhooksig.GenerateKey()
	body := []byte(`{"id":"e1"}`)

	out := sender.Send(context.Background(), srv.URL+"/hook", "e1", body, []webhooksig.Key{key})
	if !out.Success || out.StatusCode != 200 || len(out.Excerpt) != maxResponseRead {
		t.Fatalf("success outcome = %+v (excerpt %d bytes)", out, len(out.Excerpt))
	}
	if err := key.Verify(lastBody, lastHeader, time.Now(), 0); err != nil {
		t.Fatalf("receiver cannot verify: %v", err)
	}
	if lastHeader.Get("User-Agent") != UserAgent || lastHeader.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", lastHeader)
	}

	status.Store(503)
	if out := sender.Send(context.Background(), srv.URL, "e1", body, []webhooksig.Key{key}); out.Success || out.ErrorClass != ClassHTTPStatus || out.StatusCode != 503 {
		t.Fatalf("503 outcome = %+v", out)
	}
	status.Store(http.StatusFound)
	before := hits.Load()
	if out := sender.Send(context.Background(), srv.URL, "e1", body, []webhooksig.Key{key}); out.Success || out.ErrorClass != ClassRedirect {
		t.Fatalf("redirect outcome = %+v", out)
	}
	if hits.Load() != before+1 {
		t.Fatal("the redirect was followed")
	}

	// The production client: no exceptions, so the loopback receiver is refused
	// at connect time and never sees a request.
	production := &Sender{HTTP: NewHTTPClient(rootCAs(srv))}
	before = hits.Load()
	if out := production.Send(context.Background(), srv.URL, "e1", body, []webhooksig.Key{key}); out.Success || out.ErrorClass != ClassBlockedDestination {
		t.Fatalf("production client outcome = %+v, want blocked_destination", out)
	}
	if hits.Load() != before {
		t.Fatal("the production client reached the loopback receiver")
	}

	// A test client that allows only this receiver still refuses another
	// loopback listener.
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer other.Close()
	if out := sender.Send(context.Background(), other.URL, "e1", body, []webhooksig.Key{key}); out.Success || out.ErrorClass != ClassBlockedDestination {
		t.Fatalf("the test client reached a second receiver: %+v", out)
	}
}

func TestClassifyTLSFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	// The receiver's certificate is not trusted by a client without its CA.
	sender := &Sender{HTTP: NewHTTPClient(ClientOptions{Policy: loopbackPolicy(t, srv)})}
	key, _ := webhooksig.GenerateKey()
	if out := sender.Send(context.Background(), srv.URL, "e1", []byte(`{}`), []webhooksig.Key{key}); out.Success || out.ErrorClass != ClassTLS {
		t.Fatalf("untrusted certificate outcome = %+v, want tls", out)
	}
}

func TestRenderTest(t *testing.T) {
	body, err := RenderTest("d1", "ep1", "v1", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"id":"d1","type":"webhook.test","api_version":"v1","created_at":"1970-01-01T00:00:00Z","data":{"endpointId":"ep1"}}` {
		t.Fatalf("test body = %s", body)
	}
}
