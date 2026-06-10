package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// serveBytes returns a test server that serves the given body at every path.
func serveBytes(t *testing.T, body []byte, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// downloadArtifact fetches the artifact and refuses to hand back a path unless
// the bytes match the declared checksum — the supply-chain integrity gate for
// self-updates.
func TestDownloadArtifactSuccess(t *testing.T) {
	body := []byte("frameworks-binary-payload")
	url := serveBytes(t, body, http.StatusOK)

	path, cleanup, err := downloadArtifact(context.Background(), &ipcpb.DesiredComponent{
		ArtifactUrl: url,
		Checksum:    "sha256:" + sha256Hex(body),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded artifact: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("downloaded content mismatch: %q", got)
	}
}

func TestDownloadArtifactChecksumMismatchRejected(t *testing.T) {
	url := serveBytes(t, []byte("real-bytes"), http.StatusOK)

	// A valid-format digest that does not match the served bytes must fail.
	_, cleanup, err := downloadArtifact(context.Background(), &ipcpb.DesiredComponent{
		ArtifactUrl: url,
		Checksum:    "sha256:" + sha256Hex([]byte("different-bytes")),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("checksum mismatch must be rejected")
	}
}

func TestDownloadArtifactHTTPErrorRejected(t *testing.T) {
	url := serveBytes(t, nil, http.StatusNotFound)
	_, cleanup, err := downloadArtifact(context.Background(), &ipcpb.DesiredComponent{
		ArtifactUrl: url,
		Checksum:    "sha256:" + sha256Hex([]byte("x")),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("non-2xx response must be rejected")
	}
}

func TestDownloadArtifactRequiresChecksum(t *testing.T) {
	url := serveBytes(t, []byte("x"), http.StatusOK)
	_, cleanup, err := downloadArtifact(context.Background(), &ipcpb.DesiredComponent{ArtifactUrl: url})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("a self-update with no declared checksum must be refused")
	}
}

func TestDownloadArtifactBadURLRejected(t *testing.T) {
	_, cleanup, err := downloadArtifact(context.Background(), &ipcpb.DesiredComponent{
		ArtifactUrl: "://not a url",
		Checksum:    "sha256:" + sha256Hex([]byte("x")),
	})
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatal("an unparseable artifact URL must error")
	}
}

// extractArtifactSibling stages an extracted archive in a temp dir alongside
// the install root (so the swap into place is a local rename), and falls back
// to copying a non-archive artifact verbatim.
func TestExtractArtifactSiblingTarGz(t *testing.T) {
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "mist.tar.gz")
	writeTarGz(t, archive, map[string]string{
		"bin/MistController": "controller",
		"lib/libmist.so":     "lib",
	})

	root := filepath.Join(tmp, "mistserver")
	staging, err := extractArtifactSibling(root, archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Dir(staging) != tmp {
		t.Fatalf("staging %q must be a sibling of root %q", staging, root)
	}
	assertExtracted(t, staging, map[string]string{
		"bin/MistController": "controller",
		"lib/libmist.so":     "lib",
	})
}

func TestExtractArtifactSiblingBareFileFallback(t *testing.T) {
	tmp := t.TempDir()
	artifact := filepath.Join(tmp, "MistServer") // not an archive
	if err := os.WriteFile(artifact, []byte("raw-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(tmp, "mistserver")
	staging, err := extractArtifactSibling(root, artifact)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(staging, "MistServer"))
	if err != nil {
		t.Fatalf("bare artifact should be copied into staging: %v", err)
	}
	if string(got) != "raw-binary" {
		t.Fatalf("bare artifact content mismatch: %q", got)
	}
}
