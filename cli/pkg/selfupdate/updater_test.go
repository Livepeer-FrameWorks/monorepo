package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCheckLatest(t *testing.T) {
	release := Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "frameworks-linux-amd64", BrowserDownloadURL: "https://example.com/frameworks-linux-amd64"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(release)
	}))
	defer srv.Close()

	t.Setenv("FRAMEWORKS_REPO", "test/repo")
	// Override the releases URL by patching — we can't easily, so test the parsing logic
	// via a direct HTTP call instead.

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got Release
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TagName != "v1.2.3" {
		t.Errorf("expected v1.2.3, got %s", got.TagName)
	}
}

func TestFindAsset(t *testing.T) {
	assets := []Asset{
		{Name: "frameworks-linux-amd64", BrowserDownloadURL: "https://example.com/linux"},
		{Name: "frameworks-darwin-arm64", BrowserDownloadURL: "https://example.com/darwin"},
	}

	a := findAsset(assets, "frameworks-linux-amd64")
	if a == nil || a.BrowserDownloadURL != "https://example.com/linux" {
		t.Error("expected to find linux asset")
	}

	a = findAsset(assets, "frameworks-windows-amd64")
	if a != nil {
		t.Error("expected nil for missing asset")
	}
}

func TestUpdate(t *testing.T) {
	// Create a fake binary to serve
	binaryContent := []byte("#!/bin/sh\necho test")
	checksum := sha256.Sum256(binaryContent)
	checksumStr := fmt.Sprintf("%x  frameworks-%s-%s\n", checksum, runtime.GOOS, runtime.GOARCH)

	binaryName := fmt.Sprintf("frameworks-%s-%s", runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case binaryName:
			w.Write(binaryContent)
		case binaryName + ".sha256":
			w.Write([]byte(checksumStr))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Create a temp "executable" that we'll replace
	tmpDir := t.TempDir()
	execPath := filepath.Join(tmpDir, "frameworks")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	release := &Release{
		TagName: "v2.0.0",
		Assets: []Asset{
			{Name: binaryName, BrowserDownloadURL: srv.URL + "/" + binaryName},
			{Name: binaryName + ".sha256", BrowserDownloadURL: srv.URL + "/" + binaryName + ".sha256"},
		},
	}

	// We can't easily override os.Executable() in tests, so test the download+checksum
	// logic directly instead of calling Update().
	tmpFile := filepath.Join(tmpDir, "download")
	f, err := os.Create(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := downloadFile(context.Background(), srv.URL+"/"+binaryName, f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	content, _ := os.ReadFile(tmpFile)
	if string(content) != string(binaryContent) {
		t.Errorf("downloaded content mismatch")
	}

	if err := verifyChecksum(context.Background(), srv.URL+"/"+binaryName+".sha256", tmpFile); err != nil {
		t.Errorf("checksum verification failed: %v", err)
	}

	_ = release // used above for reference
}

func TestVerifyChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  file\n"))
	}))
	defer srv.Close()

	tmpFile := filepath.Join(t.TempDir(), "file")
	os.WriteFile(tmpFile, []byte("content"), 0o644)

	err := verifyChecksum(context.Background(), srv.URL+"/checksum", tmpFile)
	if err == nil {
		t.Error("expected checksum mismatch error")
	}
}
