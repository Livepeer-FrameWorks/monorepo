package relay

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("platform client must not fetch a tenant source")
}

func loopbackPolicy() restream.DestinationPolicy {
	return restream.DestinationPolicy{
		AllowedCIDRs: []*net.IPNet{{IP: net.ParseIP("127.0.0.1").To4(), Mask: net.CIDRMask(32, 32)}},
	}
}

// A VOD import's source is served to Mist through the upload route, fetched
// with the tenant-source client and never the platform client.
func TestImportSourceServedThroughTenantClient(t *testing.T) {
	body := bytes.Repeat([]byte("imported-video-bytes"), 64)
	up := upstreamServer(t, body)
	defer up.Close()

	const hash = "importhash01"
	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:           ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		TenantSourceURL: up.URL + "/talk.mp4",
	}}}
	s := New(Options{
		BasePath:               t.TempDir(),
		Admitter:               &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:               resolver,
		HTTPClient:             &http.Client{Transport: failingTransport{}},
		TenantSourceHTTPClient: newTenantSourceClient(loopbackPolicy()),
	})
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/upload/"+hash+".mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("status=%d, %d bytes; want 200 and the %d-byte source", resp.StatusCode, len(got), len(body))
	}
}

// The default tenant-source client dials only public addresses: an import
// whose source is on loopback is refused before any request reaches it.
func TestImportSourceDefaultClientRefusesPrivateAddresses(t *testing.T) {
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	const hash = "importhash02"
	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:           ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		TenantSourceURL: up.URL + "/talk.mp4",
	}}}
	s := newTestServer(t, t.TempDir(), admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/upload/"+hash+".mp4")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK || hits.Load() != 0 {
		t.Fatalf("status=%d, source hits=%d; a loopback import source must be refused unfetched", resp.StatusCode, hits.Load())
	}
}

// Only the import source uses the tenant client; S3 and peer upstreams keep
// the platform client.
func TestUpstreamClientSelection(t *testing.T) {
	platform, tenant := &http.Client{}, &http.Client{}
	s := &Server{httpc: platform, tenantHTTPC: tenant}
	for _, tc := range []struct {
		name string
		res  *ResolveResult
		want *http.Client
	}{
		{"import source", &ResolveResult{TenantSourceURL: "https://cdn.example/v.mp4"}, tenant},
		{"s3", &ResolveResult{MediaPresignedURL: "https://s3.example/v.mp4"}, platform},
		{"peer relay", &ResolveResult{PeerRelayURL: "https://edge.example/internal/artifact/vod/h.mp4"}, platform},
	} {
		if got := s.upstreamClient(tc.res); got != tc.want {
			t.Errorf("%s: wrong client", tc.name)
		}
	}
}

// slowMissingSource answers 404 after delay, like an import source host whose
// 404 takes seconds, and counts the requests that reached it.
func slowMissingSource(t *testing.T, delay time.Duration) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-time.After(delay):
			w.WriteHeader(http.StatusNotFound)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(up.Close)
	return up, &hits
}

// slowMissingImport mounts a relay whose upload hash resolves to an import
// source on up.
func slowMissingImport(t *testing.T, up *httptest.Server, hash string) *httptest.Server {
	t.Helper()
	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:           ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		TenantSourceURL: up.URL + "/missing.mp4",
	}}}
	s := New(Options{
		BasePath:               t.TempDir(),
		Admitter:               &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:               resolver,
		HTTPClient:             &http.Client{Transport: failingTransport{}},
		TenantSourceHTTPClient: newTenantSourceClient(loopbackPolicy()),
	})
	ts := mount(t, s)
	t.Cleanup(ts.Close)
	return ts
}

// mistRead is one Mist read of url with Mist's 5 s source read timeout. It
// returns 0 when the read timed out.
func mistRead(t *testing.T, url string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "MistServer/Unknown")
	resp, err := (&http.Client{Timeout: mistSourceReadTimeout}).Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// An import whose source answers 404 only after 12 s, read by Mist with its
// 5 s source read timeout: the size probe outlives each reader, so the 404 is
// learned once, from one upstream request, and the retry that arrives after
// it is answered 404 instead of starting another probe that Mist abandons.
func TestImportSourceSlowMissingAnswerOutlivesReaders(t *testing.T) {
	up, hits := slowMissingSource(t, 12*time.Second)
	const hash = "importslow404"
	ts := slowMissingImport(t, up, hash)

	started := time.Now()
	status := 0
	for status == 0 && time.Since(started) < 25*time.Second {
		status = mistRead(t, ts.URL+"/internal/artifact/upload/"+hash+".mp4")
	}
	elapsed := time.Since(started)
	if status != http.StatusNotFound || elapsed > 14*time.Second {
		t.Fatalf("status %d after %s; want 404 within ~12 s", status, elapsed.Round(time.Second))
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("source got %d requests; want one probe shared by every reader", n)
	}
}

// A redirect may not leave http(s).
func TestTenantSourceClientRefusesNonHTTPRedirect(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
	}))
	defer up.Close()
	client := newTenantSourceClient(loopbackPolicy())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, up.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a redirect to a file: URL must be refused")
	}
}
