//go:build media_verify

package mist

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// This fixture exercises Mist's existing per-connection gate with actual DTSC
// media. The hook answers are fixed; tenant policy and peer identity are not
// simulated by declaring a successful HTTP request to be media evidence.
func TestSourceAdmission_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact image under test (Mist and ffmpeg required)")
	}
	fixture, fixtureErr := filepath.Abs("testdata/source-admission.json")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	hook, hookErr := filepath.Abs("testdata/source-admission-hook.sh")
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	name := fmt.Sprintf("fw-source-admission-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned Mist fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -t 12 -c:v libx264 -g 10 -pix_fmt yuv420p /tmp/source.mp4 && exec MistController -c /tmp/source-admission.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080", "-v", fixture+":/tmp/source-admission.json:ro", "-v", hook+":/tmp/source-admission-hook.sh:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
		t.Fatalf("start isolated media fixture: %v %s", err, output)
	}
	port, portErr := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if portErr != nil {
		t.Fatal(portErr)
	}
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 12 * time.Second}
	fetch := func(rawURL string) (string, int, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
		if err != nil {
			return "", 0, err
		}
		response, err := client.Do(req)
		if err != nil {
			return "", 0, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		return string(body), response.StatusCode, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	playlistURL := base + "/hls/allowed-replica/index.m3u8"
	mediaReceived := false
	for ctx.Err() == nil {
		body, status, err := fetch(playlistURL)
		if err == nil && status == http.StatusOK {
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parent, _ := url.Parse(playlistURL)
				reference, parseErr := url.Parse(line)
				if parseErr != nil {
					continue
				}
				child := parent.ResolveReference(reference)
				if child.Host != parent.Host {
					t.Fatal("fixture advertised an external media endpoint")
				}
				if strings.Contains(child.Path, ".m3u8") {
					playlistURL = child.String()
					break
				}
				segment, code, segmentErr := fetch(child.String())
				if segmentErr == nil && code == http.StatusOK && len(segment) >= 3*188 && segment[0] == 0x47 && segment[188] == 0x47 && segment[376] == 0x47 {
					mediaReceived = true
					break
				}
			}
		}
		if mediaReceived {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !mediaReceived {
		logs, _ := dockerpg.CLI("logs", "--tail", "45", name)
		t.Fatalf("allowed DTSC pull did not deliver MPEG-TS media: %s", logs)
	}
	body, _, _ := fetch(base + "/hls/denied-replica/index.m3u8")
	if strings.Contains(body, "#EXTINF:") {
		t.Fatal("denied DTSC pull exposed media segments")
	}
	logs, err := dockerpg.CLI("logs", "--tail", "150", name)
	if err != nil || !strings.Contains(string(logs), "Not allowed to play (CONN_PLAY)") {
		t.Fatalf("denied request did not reach the existing connection gate: %v %s", err, logs)
	}
}
