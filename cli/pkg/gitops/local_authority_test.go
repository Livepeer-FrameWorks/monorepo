package gitops

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestLocalAuthorityErrorsCannotSelectCachedOrUpstreamRelease(t *testing.T) {
	t.Parallel()
	const valid = "platform_version: v1.0.0\nservices: []\n"
	for _, tc := range []struct {
		name, version, file, body string
	}{
		{"unknown field", "v1.0.0", "releases/v1.0.0.yaml", valid + "sha256: invalid\n"},
		{"invalid timestamp", "v1.0.0", "releases/v1.0.0.yaml", valid + "release_date: not-a-date\n"},
		{"wrong identity", "v1.0.0", "releases/v1.0.0.yaml", "platform_version: v2.0.0\n"},
		{"missing release", "v1.0.0", "releases/v1.0.0.yaml", ""},
		{"malformed pointer", "latest", "channels/stable.yaml", "manifest: [\n"},
		{"missing pointer", "latest", "channels/stable.yaml", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(path, body string) {
				t.Helper()
				path = filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("releases/v1.0.0.yaml", valid)
			write("channels/stable.yaml", "platform_version: v1.0.0\nmanifest: releases/v1.0.0.yaml\n")
			opts := FetchOptions{Repository: dir, CacheDir: t.TempDir(), RetryCount: 1}
			f, err := NewFetcher(opts)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Fetch("stable", tc.version); err != nil {
				t.Fatal(err)
			}
			if tc.body == "" {
				if err := os.Remove(filepath.Join(dir, tc.file)); err != nil {
					t.Fatal(err)
				}
			} else {
				write(tc.file, tc.body)
			}
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path == "/channels/stable.yaml" {
					_, _ = w.Write([]byte("platform_version: v1.0.0\nmanifest: releases/v1.0.0.yaml\n"))
					return
				}
				_, _ = w.Write([]byte(valid))
			}))
			defer upstream.Close()
			for _, cache := range []string{opts.CacheDir, t.TempDir()} {
				opts.CacheDir = cache
				manifest, err := FetchFromRepositories(opts, []string{dir, upstream.URL}, "stable", tc.version)
				if err == nil || manifest != nil {
					t.Errorf("local error hidden by cache or upstream: manifest=%v err=%v", manifest, err)
				}
			}
			if requests.Load() != 0 {
				t.Fatalf("local failure consulted upstream %d times", requests.Load())
			}
		})
	}
}
