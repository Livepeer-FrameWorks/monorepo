package relay

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"frameworks/api_sidecar/internal/admission"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A processing input admitted to the disk block cache is sized from its
// upstream, and that size is kept for the processing job to report; a
// refused input records nothing.
func TestProcessingInputRecordsStagedSize(t *testing.T) {
	body := []byte("imported source body of some length")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := int64(len(body))
		spec := strings.TrimPrefix(r.Header.Get("Range"), "bytes=")
		parts := strings.SplitN(spec, "-", 2)
		start, _ := strconv.ParseInt(parts[0], 10, 64)
		end := size - 1
		if len(parts) == 2 && parts[1] != "" {
			if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil && e < end {
				end = e
			}
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start : end+1])
	}))
	defer up.Close()

	fetch := func(t *testing.T, hash string, decision admission.CacheDecision) int {
		t.Helper()
		resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
			State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
			MediaPresignedURL: up.URL,
		}}}
		s := newTestServer(t, t.TempDir(), decision, resolver, nil)
		ts := mount(t, s)
		defer ts.Close()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/upload/"+hash+".mp4", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", "bytes=0-7")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if status := fetch(t, "importstaged", admission.CacheToDisk); status != http.StatusPartialContent {
		t.Fatalf("staged fetch status = %d", status)
	}
	if size, ok := TakeProcessingInputSize("importstaged"); !ok || size != int64(len(body)) {
		t.Fatalf("staged size = %d, %v; want %d", size, ok, len(body))
	}
	if _, ok := TakeProcessingInputSize("importstaged"); ok {
		t.Fatal("staged size was not forgotten once taken")
	}

	if status := fetch(t, "importrefused", admission.CacheMemoryOnly); status != http.StatusServiceUnavailable {
		t.Fatalf("refused fetch status = %d", status)
	}
	if size, ok := TakeProcessingInputSize("importrefused"); ok {
		t.Fatalf("refused input recorded a staged size %d", size)
	}
}
