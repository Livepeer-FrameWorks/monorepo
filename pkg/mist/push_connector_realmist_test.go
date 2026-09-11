//go:build media_verify

package mist

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// Proves the connector attestation on a real Mist build: an RTMP publisher and an
// SRT publisher each trigger PUSH_REWRITE, and Mist itself appends its connector
// name as the fourth payload line regardless of what URL the publisher used.
func TestPushRewriteConnector_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact image under test (Mist and ffmpeg required)")
	}
	fixture, err := filepath.Abs("testdata/push-connector.json")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := filepath.Abs("testdata/push-connector-hook.sh")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-push-connector-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned Mist fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 6 -c:v libx264 -g 10 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && exec MistController -c /tmp/push-connector.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-v", fixture+":/tmp/push-connector.json:ro", "-v", hook+":/tmp/push-connector-hook.sh:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
		t.Fatalf("start isolated media fixture: %v %s", err, output)
	}
	waitForSource := func() {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := dockerpg.CLI("exec", name, "test", "-s", "/tmp/source.mp4"); err == nil {
				if _, err := dockerpg.CLI("exec", name, "sh", "-c", "pgrep MistController >/dev/null"); err == nil {
					time.Sleep(time.Second)
					return
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		logs, _ := dockerpg.CLI("logs", "--tail", "30", name)
		t.Fatalf("fixture did not become ready: %s", logs)
	}
	waitForSource()
	protocols, _ := dockerpg.CLI("exec", name, "sh", "-c", "ffmpeg -hide_banner -protocols 2>/dev/null | tr -d ' '")
	srtSupported := strings.Contains(string(protocols), "\nsrt\n") || strings.Contains(string(protocols), "srt")
	publish := func(target string) {
		t.Helper()
		// Connectors come up shortly after the controller; a refused connection
		// is retried, any other publish failure is a real fixture failure.
		var output string
		var err error
		for attempt := 0; attempt < 30; attempt++ {
			output, err = dockerpg.CLI("exec", name, "sh", "-c", "ffmpeg -hide_banner -loglevel error -re -i /tmp/source.mp4 -c copy "+target)
			if err == nil || !strings.Contains(output, "Connection refused") {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("publish %s: %v %s", target, err, output)
		}
	}
	publish("-f flv rtmp://127.0.0.1:1935/live/pushed")
	if srtSupported {
		publish("-f mpegts 'srt://127.0.0.1:8889?streamid=pushed'")
	}
	audit, err := dockerpg.CLI("exec", name, "cat", "/tmp/push-connector-audit")
	if err != nil {
		logs, _ := dockerpg.CLI("logs", "--tail", "60", name)
		t.Fatalf("PUSH_REWRITE never fired: %v %s", err, logs)
	}
	connectors := map[string]int{}
	for _, record := range strings.Split(strings.TrimSpace(string(audit)), "\n") {
		fields := strings.Split(strings.TrimRight(record, "\t"), "\t")
		t.Logf("PUSH_REWRITE payload: %q", fields)
		if len(fields) != 4 {
			t.Fatalf("PUSH_REWRITE payload has %d lines, want URL, host, stream and connector: %q", len(fields), fields)
		}
		if fields[2] != "pushed" || strings.TrimSpace(fields[3]) == "" {
			t.Fatalf("unexpected PUSH_REWRITE payload: %q", fields)
		}
		connectors[fields[3]]++
	}
	if connectors["RTMP"] == 0 {
		t.Fatalf("RTMP publisher did not attest its connector: %v", connectors)
	}
	if srtSupported && connectors["TSSRT"] == 0 {
		t.Fatalf("SRT publisher did not attest its connector: %v", connectors)
	}
	if !srtSupported {
		t.Log("fixture ffmpeg lacks SRT support; connector attestation proven for RTMP only")
	}
}
