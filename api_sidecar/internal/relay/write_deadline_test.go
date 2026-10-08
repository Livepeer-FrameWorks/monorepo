package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
)

// rangedUpstream serves size bytes of fill with Range support. Each response
// body goes out in chunk-sized writes with gap between them.
func rangedUpstream(t *testing.T, size int64, chunk int, gap time.Duration) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := int64(0), size-1
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
			start, _ = strconv.ParseInt(parts[0], 10, 64)
			if len(parts) == 2 && parts[1] != "" {
				end, _ = strconv.ParseInt(parts[1], 10, 64)
			}
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		flusher, _ := w.(http.Flusher)
		buf := bytes.Repeat([]byte{'x'}, chunk)
		for remaining := end - start + 1; remaining > 0; {
			n := min(remaining, int64(chunk))
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			remaining -= n
			if remaining > 0 && gap > 0 {
				select {
				case <-time.After(gap):
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	t.Cleanup(up.Close)
	return up
}

func relayForUpstream(t *testing.T, up *httptest.Server, hash string, size int64, decision admission.CacheDecision, blockSize int64) *Server {
	t.Helper()
	res := &ResolveResult{
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(size),
		URLTTLSeconds:     60,
	}
	return New(Options{
		BasePath:  t.TempDir(),
		Admitter:  &fakeAdmitter{decision: decision},
		Resolver:  &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}},
		BlockSize: blockSize,
	})
}

// A relay response outlives the listener's WriteTimeout whenever the upstream
// is slower than that budget. Helmsman's listener runs with pkg/server's 30 s
// WriteTimeout; here it is 200 ms and the upstream takes about 700 ms.
func TestRelayStreamOutlivesServerWriteTimeout(t *testing.T) {
	const size = 8 * 32 * 1024
	up := rangedUpstream(t, size, 32*1024, 100*time.Millisecond)
	s := relayForUpstream(t, up, "slow", size, admission.CacheMemoryOnly, size)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	s.MountRoutes(r)
	ts := httptest.NewUnstartedServer(r)
	ts.Config.WriteTimeout = 200 * time.Millisecond
	ts.Start()
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/slow.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("relay body cut after %d of %d bytes: %v", len(got), size, err)
	}
	if len(got) != size {
		t.Fatalf("relay body = %d bytes, want %d", len(got), size)
	}
}

// A client that stops reading must still release the handler: each write gets
// writeStallTimeout to make progress. The listener has no WriteTimeout here,
// so only the relay's own deadline can end the response.
func TestRelayStreamTimesOutClientThatStopsReading(t *testing.T) {
	const size = int64(256 << 20)
	up := rangedUpstream(t, size, 256*1024, 0)
	s := relayForUpstream(t, up, "stall", size, admission.CacheMemoryOnly, 0)
	s.writeStallTimeout = 300 * time.Millisecond

	handlerDone := make(chan time.Time, 1)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Next()
		handlerDone <- time.Now()
	})
	s.MountRoutes(r)
	ts := httptest.NewServer(r)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/internal/artifact/vod/stall.mkv", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	stalledAt := time.Now()

	select {
	case doneAt := <-handlerDone:
		if waited := doneAt.Sub(stalledAt); waited < 300*time.Millisecond {
			t.Fatalf("handler returned after %v, before the stall timeout", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("relay handler still writing to a client that stopped reading")
	}
}
