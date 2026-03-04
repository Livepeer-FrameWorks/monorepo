package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultRepo = "Livepeer-FrameWorks/monorepo"
	binaryFmt   = "frameworks-%s-%s"
)

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type UpdateResult struct {
	PreviousVersion string
	NewVersion      string
}

func releasesURL() string {
	repo := os.Getenv("FRAMEWORKS_REPO")
	if repo == "" {
		repo = defaultRepo
	}
	return fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
}

// CheckLatest fetches the latest release from GitHub.
func CheckLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "frameworks-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &release, nil
}

// findAsset returns the asset matching the given name, or nil.
func findAsset(assets []Asset, name string) *Asset {
	for i := range assets {
		if assets[i].Name == name {
			return &assets[i]
		}
	}
	return nil
}

// Update downloads the latest binary and atomically replaces the current executable.
func Update(ctx context.Context, release *Release) (*UpdateResult, error) {
	binaryName := fmt.Sprintf(binaryFmt, runtime.GOOS, runtime.GOARCH)

	asset := findAsset(release.Assets, binaryName)
	if asset == nil {
		return nil, fmt.Errorf("no release asset for %s/%s (expected %s)", runtime.GOOS, runtime.GOARCH, binaryName)
	}

	execPath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("cannot determine executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve symlinks: %w", err)
	}

	// Download to temp file in the same directory (same filesystem for atomic rename)
	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".frameworks-update-*")
	if err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied writing to %s (try: sudo frameworks update)", dir)
		}
		return nil, fmt.Errorf("cannot create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpPath)
	}()

	if err := downloadFile(ctx, asset.BrowserDownloadURL, tmp); err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	tmp.Close()

	// Verify checksum if available
	checksumAsset := findAsset(release.Assets, binaryName+".sha256")
	if checksumAsset != nil {
		if err := verifyChecksum(ctx, checksumAsset.BrowserDownloadURL, tmpPath); err != nil {
			return nil, err
		}
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return nil, fmt.Errorf("chmod failed: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmpPath, execPath); err != nil {
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied replacing %s (try: sudo frameworks update)", execPath)
		}
		return nil, fmt.Errorf("failed to replace binary: %w", err)
	}

	return &UpdateResult{NewVersion: release.TagName}, nil
}

func downloadFile(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "frameworks-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	_, err = io.Copy(w, resp.Body)
	return err
}

func verifyChecksum(ctx context.Context, checksumURL, filePath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "frameworks-cli")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to fetch checksum: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read checksum: %w", err)
	}

	fields := strings.Fields(strings.TrimSpace(string(body)))
	if len(fields) == 0 {
		return fmt.Errorf("empty or malformed checksum response")
	}
	expected := fields[0]

	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))

	if expected != actual {
		return fmt.Errorf("checksum mismatch (expected %s, got %s)", expected, actual)
	}
	return nil
}
