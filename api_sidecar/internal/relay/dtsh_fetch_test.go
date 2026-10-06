package relay

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// sidecarResolver resolves every asset to one sidecar URL and is safe for
// concurrent requests.
type sidecarResolver struct{ url string }

func (r sidecarResolver) Resolve(ResolveContext) (*ResolveResult, error) {
	return &ResolveResult{State: ipcpb.AssetState_ASSET_STATE_PLAYABLE, DtshPresignedGet: r.url, URLTTLSeconds: 60}, nil
}

func sidecarRelay(t *testing.T, upstream http.HandlerFunc) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	ts := mount(t, newTestServer(t, t.TempDir(), admission.CacheToDisk, sidecarResolver{url: up.URL + "/sidecar.dtsh"}, nil))
	t.Cleanup(ts.Close)
	return ts
}

func mistSidecarRequest(t *testing.T, method, url string) (int, http.Header, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "MistServer/test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body, err
}

// Mist gives up on a sidecar read after 5 s without data and retries it. A
// stalled store must get Mist an answer before that, and the answer is
// retry-later: the sidecar is not known to be missing, so it is not the 404
// that asks Mist to generate one.
func TestDtshStalledUpstreamAnswersRetryLaterBeforeMistTimesOut(t *testing.T) {
	prev := dtshFetchTimeout
	dtshFetchTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dtshFetchTimeout = prev })
	release := make(chan struct{})
	ts := sidecarRelay(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		start := time.Now()
		status, header, _, err := mistSidecarRequest(t, method, ts.URL+"/internal/artifact/vod/stalled.mkv.dtsh")
		if err != nil {
			t.Fatalf("%s: no answer before Mist's timeout: %v", method, err)
		}
		if status != http.StatusServiceUnavailable || header.Get("Retry-After") == "" {
			t.Fatalf("%s: status %d Retry-After %q, want 503 with Retry-After", method, status, header.Get("Retry-After"))
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("%s: answered after %s", method, elapsed)
		}
	}
}

// Only a store that reports the sidecar missing yields the generation 404; a
// failing store answers retry-later.
func TestDtshUpstreamStatusMapsToGenerateOrRetryLater(t *testing.T) {
	for _, tc := range []struct {
		upstream int
		want     int
	}{
		{http.StatusNotFound, http.StatusNotFound},
		{http.StatusInternalServerError, http.StatusServiceUnavailable},
		{http.StatusServiceUnavailable, http.StatusServiceUnavailable},
		{http.StatusForbidden, http.StatusServiceUnavailable},
	} {
		ts := sidecarRelay(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.upstream)
		})
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			status, _, _, err := mistSidecarRequest(t, method, ts.URL+"/internal/artifact/vod/mapped.mkv.dtsh")
			if err != nil || status != tc.want {
				t.Fatalf("%s upstream %d: status %d (%v), want %d", method, tc.upstream, status, err, tc.want)
			}
		}
	}
}

// Mist HEADs a sidecar before reading it. The HEAD learns the size from a
// one-byte ranged read instead of downloading the whole sidecar.
func TestDtshHeadDoesNotFetchTheWholeSidecar(t *testing.T) {
	body := validDtshBytes()
	var fullReads, rangedReads atomic.Int32
	ts := sidecarRelay(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "bytes=0-0" {
			rangedReads.Add(1)
			w.Header().Set("Content-Range", "bytes 0-0/"+strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[:1])
			return
		}
		fullReads.Add(1)
		_, _ = w.Write(body)
	})

	status, header, _, err := mistSidecarRequest(t, http.MethodHead, ts.URL+"/internal/artifact/vod/headonly.mkv.dtsh")
	if err != nil || status != http.StatusOK {
		t.Fatalf("HEAD status %d (%v), want 200", status, err)
	}
	if header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD Content-Length %q, want %d", header.Get("Content-Length"), len(body))
	}
	if fullReads.Load() != 0 || rangedReads.Load() != 1 {
		t.Fatalf("HEAD made %d full and %d ranged upstream reads, want 0 and 1", fullReads.Load(), rangedReads.Load())
	}

	status, _, got, err := mistSidecarRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/headonly.mkv.dtsh")
	if err != nil || status != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("GET after HEAD: status %d (%v), %d bytes", status, err, len(got))
	}
}

// Readers that ask for the same sidecar together share one upstream fetch.
func TestDtshConcurrentReadsShareOneFetch(t *testing.T) {
	body := validDtshBytes()
	var fetches atomic.Int32
	release := make(chan struct{})
	ts := sidecarRelay(t, func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		<-release
		_, _ = w.Write(body)
	})

	const readers = 5
	var wg sync.WaitGroup
	results := make([]int, readers)
	for i := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, got, err := mistSidecarRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/shared.mkv.dtsh")
			if err == nil && bytes.Equal(got, body) {
				results[i] = status
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, status := range results {
		if status != http.StatusOK {
			t.Fatalf("reader %d: status %d, want 200 with the sidecar", i, status)
		}
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("upstream fetches = %d, want 1 shared by %d readers", got, readers)
	}
}
