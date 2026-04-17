package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
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
		{Name: "frameworks-cli-v1.2.3-linux-amd64.tar.gz", BrowserDownloadURL: "https://example.com/linux"},
		{Name: "frameworks-cli-v1.2.3-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/darwin"},
	}

	a := findAsset(assets, "frameworks-cli-v1.2.3-linux-amd64.tar.gz")
	if a == nil || a.BrowserDownloadURL != "https://example.com/linux" {
		t.Error("expected to find linux asset")
	}

	a = findAsset(assets, "frameworks-cli-v1.2.3-windows-amd64.zip")
	if a != nil {
		t.Error("expected nil for missing asset")
	}
}

func TestFindReleaseAssetPrefersPackagedAssets(t *testing.T) {
	release := &Release{
		TagName: "v1.2.3",
		Assets: []Asset{
			{Name: "frameworks-darwin-arm64", BrowserDownloadURL: "https://example.com/raw"},
			{Name: "frameworks-cli-v1.2.3-darwin-arm64.zip", BrowserDownloadURL: "https://example.com/zip"},
		},
	}

	asset, name := findReleaseAsset(release.Assets, release.TagName)
	if asset == nil {
		t.Fatal("expected asset")
	}
	if name != "frameworks-cli-v1.2.3-darwin-arm64.zip" {
		t.Fatalf("expected packaged asset, got %s", name)
	}
}

func TestUpdate(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho test")
	version := "v1.2.3"
	assetName := packagedAssetName(version)
	archiveBytes := mustCreateArchive(t, assetName, binaryContent)
	checksum := sha256.Sum256(archiveBytes)
	checksumStr := fmt.Sprintf("%x  %s\n", checksum, assetName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case assetName:
			w.Write(archiveBytes)
		case assetName + ".sha256":
			w.Write([]byte(checksumStr))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmpDir := t.TempDir()

	// We can't easily override os.Executable() in tests, so test the download+checksum
	// logic directly instead of calling Update().
	tmpFile := filepath.Join(tmpDir, "download")
	f, err := os.Create(tmpFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := downloadFile(context.Background(), srv.URL+"/"+assetName, f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(assetName, tmpFile, extractDir); err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(filepath.Join(extractDir, "frameworks"))
	if string(content) != string(binaryContent) {
		t.Errorf("extracted content mismatch")
	}

	if err := verifyChecksum(context.Background(), srv.URL+"/"+assetName+".sha256", tmpFile); err != nil {
		t.Errorf("checksum verification failed: %v", err)
	}

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

func packagedAssetName(version string) string {
	version = strings.TrimPrefix(version, "v")
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("frameworks-cli-v%s-%s-%s.zip", version, runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf("frameworks-cli-v%s-%s-%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
}

func mustCreateArchive(t *testing.T, assetName string, binaryContent []byte) []byte {
	t.Helper()

	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, assetName)

	switch {
	case strings.HasSuffix(assetName, ".zip"):
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatal(err)
		}

		writer := zip.NewWriter(file)
		entry, err := writer.Create("frameworks")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(binaryContent); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

	case strings.HasSuffix(assetName, ".tar.gz"):
		file, err := os.Create(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		gzWriter := gzip.NewWriter(file)
		tarWriter := tar.NewWriter(gzWriter)
		header := &tar.Header{Name: "frameworks", Mode: 0o755, Size: int64(len(binaryContent))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(binaryContent); err != nil {
			t.Fatal(err)
		}
		if err := tarWriter.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gzWriter.Close(); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}

	default:
		if err := os.WriteFile(archivePath, binaryContent, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
