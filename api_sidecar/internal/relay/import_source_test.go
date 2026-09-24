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
