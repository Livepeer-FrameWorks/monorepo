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

// TestSourcePullCredential_RealMist pins the transport half of accepted-source
// admission. An origin admits a peer's DTSC pull only when that connection
// presents the credential issued for its exact attempt, and the credential
// travels as a query argument on the source URL. That argument therefore has to
// survive the destination's DTSC play command and reach the origin's request
// URL, which is what the origin's admission hook reads. If Mist drops it, every
// cross-cell pull is classified as a viewer and refused, so this asserts the
// credential arrives rather than asserting that media merely flowed.
func TestSourcePullCredential_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact image under test (Mist and ffmpeg required)")
	}
	fixture, fixtureErr := filepath.Abs("testdata/source-credential.json")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	hook, hookErr := filepath.Abs("testdata/source-credential-hook.sh")
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	name := fmt.Sprintf("fw-source-credential-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned Mist fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -t 12 -c:v libx264 -g 10 -pix_fmt yuv420p /tmp/source.mp4 && exec MistController -c /tmp/source-credential.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080",
		"-v", fixture+":/tmp/source-credential.json:ro", "-v", hook+":/tmp/source-credential-hook.sh:ro",
		"--entrypoint", "/bin/sh", image, "-c", command); err != nil {
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

	// Requesting the replica makes it dial the origin over DTSC, which is the
	// connection whose request URL must carry the credential.
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	playlistURL := base + "/hls/credential-replica/index.m3u8"
	credentialSeen := false
	var lastPayloads string
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
				_, _, _ = fetch(child.String())
			}
		}
		payloads, _ := dockerpg.CLI("exec", name, "cat", "/tmp/conn-play-payloads.txt")
		if strings.TrimSpace(payloads) != "" {
			lastPayloads = payloads
			if strings.Contains(payloads, "token=fwsrc.two-cell-proof.CREDENTIAL") {
				credentialSeen = true
				break
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	if !credentialSeen {
		logs, _ := dockerpg.CLI("logs", "--tail", "45", name)
		t.Fatalf("origin never saw the pull credential on its request URL.\npayloads: %s\nlogs: %s", lastPayloads, logs)
	}
	// The origin must see the credential on the DTSC connection, not on some
	// other connector's request: the same credential on an HLS viewer request
	// would mean the destination is exposing it to clients.
	for _, record := range strings.Split(lastPayloads, "===RECORD===") {
		if !strings.Contains(record, "token=fwsrc.two-cell-proof.CREDENTIAL") {
			continue
		}
		lines := strings.Split(strings.TrimSpace(record), "\n")
		if len(lines) < 4 {
			t.Fatalf("admission payload was not the documented four lines: %q", record)
		}
		if connector := strings.TrimSpace(lines[2]); connector != "DTSC" {
			t.Fatalf("credential arrived on connector %q, want DTSC", connector)
		}
		if requestURL := strings.TrimSpace(lines[3]); !strings.HasPrefix(requestURL, "dtsc://") {
			t.Fatalf("credential arrived on request URL %q, want a DTSC source URL", requestURL)
		}
	}
}
