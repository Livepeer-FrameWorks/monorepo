package jobs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// fakeHLSPresigner satisfies control.S3ClientInterface so resolveHLSSegmentURLs
// can be exercised end-to-end. Only ParseS3URL and GeneratePresignedGET carry
// behavior; GeneratePresignedGET fails for any key containing failSubstr so the
// per-segment skip-on-failure path can be driven deterministically.
type fakeHLSPresigner struct {
	failSubstr string
}

func (f *fakeHLSPresigner) GeneratePresignedGET(key string, _ time.Duration) (string, error) {
	if f.failSubstr != "" && strings.Contains(key, f.failSubstr) {
		return "", fmt.Errorf("presign refused for %s", key)
	}
	return "https://signed.example/" + key, nil
}

func (f *fakeHLSPresigner) ParseS3URL(s3URL string) (string, error) {
	if strings.HasPrefix(s3URL, "s3://") {
		if parts := strings.SplitN(s3URL[5:], "/", 2); len(parts) == 2 {
			return parts[1], nil
		}
	}
	return s3URL, nil
}

// Unused-by-this-test interface methods.
func (f *fakeHLSPresigner) GeneratePresignedPUT(string, time.Duration) (string, error) {
	return "", nil
}
func (f *fakeHLSPresigner) PutObject(context.Context, string, []byte, string) error { return nil }
func (f *fakeHLSPresigner) ListPrefix(context.Context, string) ([]string, error)    { return nil, nil }
func (f *fakeHLSPresigner) Delete(context.Context, string) error                    { return nil }
func (f *fakeHLSPresigner) DeleteByURL(context.Context, string) error               { return nil }
func (f *fakeHLSPresigner) DeletePrefix(context.Context, string) (int, error)       { return 0, nil }
func (f *fakeHLSPresigner) BuildClipS3Key(string, string, string, string) string    { return "" }
func (f *fakeHLSPresigner) BuildDVRS3Key(string, string, string) string             { return "" }
func (f *fakeHLSPresigner) BuildVodS3Key(string, string, string) string             { return "" }
func (f *fakeHLSPresigner) BuildS3URL(key string) string                            { return "s3://bucket/" + key }

func newHLSDispatcher() *ProcessingDispatcher {
	return NewProcessingDispatcher(ProcessingDispatcherConfig{Logger: logging.NewLogger()})
}

// TestResolveHLSSegmentURLs covers the manifest-to-presigned-pairs contract:
// every segment filename and embedded tag URI is rewritten to
// "name=presignedURL" against the segment's sibling S3 directory, derived from
// the artifact's s3:// URL. The function fetches over HTTP, so each case serves
// a manifest from httptest.
func TestResolveHLSSegmentURLs(t *testing.T) {
	control.SetS3Client(&fakeHLSPresigner{})
	t.Cleanup(func() { control.SetS3Client(nil) })

	serve := func(t *testing.T, status int, body string) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}

	d := newHLSDispatcher()
	s3URL := "s3://my-bucket/tenant-a/abc123/index.m3u8"

	t.Run("segments and embedded URIs are presigned against sibling dir", func(t *testing.T) {
		manifest := strings.Join([]string{
			"#EXTM3U",
			"#EXT-X-VERSION:7",
			`#EXT-X-MAP:URI="init.mp4"`,
			"#EXTINF:6.0,",
			"seg-0.ts",
			"#EXTINF:6.0,",
			"seg-1.ts",
			"#EXT-X-ENDLIST",
		}, "\n")

		got, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, serve(t, http.StatusOK, manifest))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := strings.Join([]string{
			"init.mp4=https://signed.example/tenant-a/abc123/init.mp4",
			"seg-0.ts=https://signed.example/tenant-a/abc123/seg-0.ts",
			"seg-1.ts=https://signed.example/tenant-a/abc123/seg-1.ts",
		}, "\n")
		if got != want {
			t.Fatalf("pairs =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("absolute http URIs in tags are left for the manifest as-is", func(t *testing.T) {
		// An EXT-X-MAP whose URI is already absolute must not be presigned (it is
		// not a sibling S3 object); only the relative segment is rewritten.
		manifest := strings.Join([]string{
			"#EXTM3U",
			`#EXT-X-MAP:URI="https://cdn.example/init.mp4"`,
			"seg-0.ts",
		}, "\n")
		got, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, serve(t, http.StatusOK, manifest))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "seg-0.ts=https://signed.example/tenant-a/abc123/seg-0.ts" {
			t.Fatalf("absolute URI leaked into pairs: %q", got)
		}
	})

	t.Run("a single failing presign is skipped, not fatal", func(t *testing.T) {
		control.SetS3Client(&fakeHLSPresigner{failSubstr: "seg-1.ts"})
		t.Cleanup(func() { control.SetS3Client(&fakeHLSPresigner{}) })

		manifest := "seg-0.ts\nseg-1.ts\nseg-2.ts\n"
		got, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, serve(t, http.StatusOK, manifest))
		if err != nil {
			t.Fatalf("partial presign failure must not be fatal, got err %v", err)
		}
		if strings.Contains(got, "seg-1.ts") {
			t.Fatalf("failed segment must be omitted, got %q", got)
		}
		if !strings.Contains(got, "seg-0.ts=") || !strings.Contains(got, "seg-2.ts=") {
			t.Fatalf("surviving segments missing from %q", got)
		}
	})

	t.Run("empty manifest yields empty pairs", func(t *testing.T) {
		got, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, serve(t, http.StatusOK, "\n  \n"))
		if err != nil || got != "" {
			t.Fatalf("empty manifest = (%q, %v), want (\"\", nil)", got, err)
		}
	})

	t.Run("non-200 manifest is an error", func(t *testing.T) {
		_, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, serve(t, http.StatusNotFound, "nope"))
		if err == nil {
			t.Fatal("expected error for non-200 manifest")
		}
	})

	t.Run("unreachable manifest URL is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close() // close immediately so the fetch fails
		if _, err := d.resolveHLSSegmentURLs(context.Background(), s3URL, url); err == nil {
			t.Fatal("expected error fetching a closed server")
		}
	})
}
