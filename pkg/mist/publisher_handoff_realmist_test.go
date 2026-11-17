//go:build media_verify

package mist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// Exercises buffer identity, not platform admission: the platform's lease and
// policy checks must still authorize a publisher before choosing its runtime.
func TestPublisherRuntimeIsolation_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the pinned Mist build with ffmpeg, ffprobe and curl")
	}
	for _, protocol := range []string{"rtmp", "srt"} {
		t.Run(protocol, func(t *testing.T) {
			successorRuntime, err := streamident.BoundRuntimeName("fixture", streamident.RuntimePublisher, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
			if err != nil {
				t.Fatal(err)
			}
			if os.Getenv("MIST_HANDOFF_LEGACY_NAMES") == "1" {
				successorRuntime = "live+fixture"
			}
			name := fmt.Sprintf("fw-publisher-isolation-%s-%d", protocol, time.Now().UnixNano())
			dir := t.TempDir()
			hook := "#!/bin/sh\npayload=$(cat)\nrequested=$(printf '%s\\n' \"$payload\" | sed -n '3p')\nprintf '%s\\n' \"$requested\" >>/tmp/rewrite-audit\ncase \"$requested\" in\n  seed) printf seed ;;\n  collision) printf 'live+fixture' ;;\n  successor) cat /tmp/successor-runtime ;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "rewrite.sh"), []byte(hook), 0o755); err != nil {
				t.Fatal(err)
			}
			sourceHook := "#!/bin/sh\nstream=$(cat)\nif [ \"$stream\" = \"$(cat /tmp/successor-runtime)\" ]; then printf 'push://'; fi\n"
			if os.Getenv("MIST_HANDOFF_LEGACY_NAMES") == "1" {
				sourceHook = "#!/bin/sh\ncat >/dev/null\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "source.sh"), []byte(sourceHook), 0o755); err != nil {
				t.Fatal(err)
			}
			config := map[string]any{
				"config": map[string]any{
					"device_discovery": false,
					"protocols":        []map[string]any{{"connector": "HTTP", "port": 18080}, {"connector": "HLS"}, {"connector": "RTMP", "port": 1935}, {"connector": "TSSRT", "port": 8889}, {"connector": "DTSC", "port": 14200}},
					"triggers":         map[string]any{"PUSH_REWRITE": []map[string]any{{"handler": "/fixture/rewrite.sh", "sync": true, "onfail": "deny"}}, "STREAM_SOURCE": []map[string]any{{"handler": "/fixture/source.sh", "sync": true, "onfail": "keep"}}},
				},
				"streams": map[string]any{
					"seed": map[string]any{"source": "push://", "inputtimeout": 12},
					"live": map[string]any{"source": "dtsc://127.0.0.1:14200/seed", "inputtimeout": 12},
				},
			}
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "config.json"), encoded, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if output, err := dockerpg.CLI("rm", "-fv", name); err != nil {
					t.Errorf("remove exact owned fixture: %v %s", err, output)
				}
			})
			boot := "printf '%s' " + successorRuntime + " >/tmp/successor-runtime; exec MistController -c /fixture/config.json"
			if output, err := dockerpg.Run("run", "-d", "--name", name, "--cpus=2", "--memory=768m", "--shm-size=256m", "-v", dir+":/fixture:ro", "--entrypoint", "/bin/sh", image, "-c", boot); err != nil {
				t.Fatalf("start owned Mist fixture: %v %s", err, output)
			}
			exec := func(command string) (string, error) {
				return dockerpg.CLI("exec", name, "sh", "-c", command)
			}
			wait := func(what string, probe func() bool) {
				t.Helper()
				until := time.Now().Add(45 * time.Second)
				for time.Now().Before(until) {
					if probe() {
						return
					}
					time.Sleep(250 * time.Millisecond)
				}
				logs, _ := dockerpg.CLI("logs", "--tail", "70", name)
				t.Fatalf("%s: %s", what, logs)
			}
			wait("RTMP listener starts", func() bool {
				_, err := exec("curl --silent --max-time 1 http://127.0.0.1:18080/ >/dev/null && bash -c 'exec 3<>/dev/tcp/127.0.0.1/1935'")
				return err == nil
			})
			startPublisher := func(label, target, size string) {
				t.Helper()
				command := fmt.Sprintf("echo $$ >/tmp/%s.pid; exec ffmpeg -hide_banner -loglevel error -filter_threads 1 -re -f lavfi -i testsrc2=size=%s:rate=10 -t 90 -c:v libx264 -threads 2 -preset ultrafast -g 10 -pix_fmt yuv420p %s >/tmp/%s.log 2>&1", label, size, target, label)
				if output, err := dockerpg.CLI("exec", "-d", name, "sh", "-c", command); err != nil {
					t.Fatalf("start exactly one %s publisher: %v %s", label, err, output)
				}
			}
			media := func(runtime, height string) bool {
				url := "'http://127.0.0.1:18080/hls/" + runtime + "/index.m3u8'"
				output, err := exec("timeout 8 ffprobe -v error -select_streams v:0 -show_entries stream=height -of csv=p=0 " + url + " 2>/dev/null")
				if err != nil || !strings.Contains(output, height) {
					return false
				}
				_, err = exec("timeout 8 ffmpeg -hide_banner -loglevel error -threads 1 -i " + url + " -frames:v 3 -an -f null -")
				return err == nil
			}
			startPublisher("seed", "-f flv rtmp://127.0.0.1:1935/live/seed", "160x90")
			wait("old relay delivers advancing seed media", func() bool { return media("live+fixture", "90") })
			publishTarget := func(key string) string {
				if protocol == "srt" {
					return "-f mpegts 'srt://127.0.0.1:8889?streamid=" + key + "'"
				}
				return "-f flv rtmp://127.0.0.1:1935/live/" + key
			}
			_, refused := exec("timeout 10 ffmpeg -hide_banner -loglevel error -re -f lavfi -i testsrc2=size=320x180:rate=10 -t 3 -c:v libx264 -preset ultrafast -g 10 " + publishTarget("collision"))
			logs, logErr := dockerpg.CLI("logs", "--tail", "100", name)
			if refused == nil || logErr != nil || !strings.Contains(logs, "not a push-able stream") {
				t.Fatalf("same-name control did not reproduce the relay rejection: publish=%v logs=%v %s", refused, logErr, logs)
			}
			t.Log("same-name admitted rewrite reproduced the internal relay rejection")
			if _, err = exec("kill $(cat /tmp/seed.pid)"); err != nil {
				t.Fatal(err)
			}
			startPublisher("successor", publishTarget("successor"), "320x180")
			wait("single successor connection delivers its own 180p media", func() bool { return media(successorRuntime, "180") })
			if _, err = exec("MistUtilNuke 'live+fixture'"); err != nil {
				t.Fatalf("delayed cleanup of the exact old relay: %v", err)
			}
			if !media(successorRuntime, "180") {
				t.Fatal("old relay cleanup disrupted the successor runtime")
			}
			audit, err := exec("cat /tmp/rewrite-audit")
			if err != nil || strings.Count(audit, "successor\n") != 1 {
				t.Fatalf("successor was not one encoder invocation: %v %q", err, audit)
			}
			if _, err := exec("kill -0 $(cat /tmp/successor.pid)"); err != nil {
				t.Fatalf("successor disconnected after relay cleanup: %v", err)
			}
			t.Log("separate publisher runtime delivered 180p and survived delayed old-relay cleanup")
		})
	}
}
