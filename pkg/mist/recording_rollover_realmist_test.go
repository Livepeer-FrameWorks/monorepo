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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// A replacement writer must append one recording without modifying any prior
// segment, including segments already pruned out of its rolling playlist.
func TestRecordingSourceRolloverPrunedPlaylist_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the pinned media fixture")
	}
	config, err := json.Marshal(map[string]any{
		"account": map[string]any{"test": map[string]any{"password": "098f6bcd4621d373cade4e832627b4f6"}},
		"config":  map[string]any{"device_discovery": false, "protocols": []any{}},
		"streams": map[string]any{
			"first":  map[string]any{"source": "/tmp/first.mp4", "realtime": true, "process_controlled_realtime": true},
			"second": map[string]any{"source": "/tmp/second.mp4", "realtime": true, "process_controlled_realtime": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-recording-rollover-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-fv", name); err != nil {
			t.Errorf("remove owned recording fixture: %v %s", err, output)
		}
	})
	boot := "ffmpeg -hide_banner -loglevel error -filter_threads 1 -f lavfi -i color=c=red:s=160x90:r=10 -t 10 -c:v libx264 -threads 1 -g 10 -bf 0 -pix_fmt yuv420p /tmp/first.mp4 && " +
		"ffmpeg -hide_banner -loglevel error -filter_threads 1 -f lavfi -i color=c=blue:s=160x90:r=10 -t 1.5 -c:v libx264 -threads 1 -g 10 -bf 0 -pix_fmt yuv420p /tmp/second.mp4 && exec MistController -c /tmp/config.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::4242", "-v", path+":/tmp/config.json:ro", "--entrypoint", "/bin/sh", image, "-c", boot); err != nil {
		t.Fatalf("start recording fixture: %v %s", err, output)
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		logs, _ := dockerpg.CLI("logs", name)
		if strings.Contains(logs, "Controller started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording controller did not start: %s", logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
	exec := func(t *testing.T, command string) string {
		t.Helper()
		output, err := dockerpg.Run("exec", name, "sh", "-c", command)
		if err != nil {
			logs, _ := dockerpg.CLI("logs", "--tail", "35", name)
			t.Fatalf("recording command failed: %v\n%s\n%s", err, output, logs)
		}
		return output
	}
	t.Run("controller writer inventory proves a completed stop", func(t *testing.T) {
		port, err := dockerpg.DiscoverPublishedHostPort(name, "4242/tcp")
		if err != nil {
			t.Fatal(err)
		}
		client := NewClient(logging.NewLogger(), ClientConfig{BaseURL: "http://127.0.0.1:" + port, Username: "test", Password: "test"})
		exec(t, "mkdir -p /tmp/snapshot/segments")
		target := "/tmp/snapshot/segments/probe_$minute_$segmentCounter.ts?m3u8=../recording.m3u8&split=2&append=1&noendlist=1&nounlink=1"
		if err := client.PushStart("first", target); err != nil {
			t.Fatal(err)
		}
		var writer PushInfo
		var controller string
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			snapshot, err := client.GetPushSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			controller = snapshot.ControllerID
			for _, push := range snapshot.Pushes {
				if push.StreamName == "first" && push.TargetURI == target {
					writer = push
					break
				}
			}
			if writer.ID > 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if writer.ID <= 0 || controller == "" {
			t.Fatal("controller did not expose the exact recording writer")
		}
		if err := client.PushStop(writer.ID); err != nil {
			t.Fatal(err)
		}
		for time.Now().Before(until) {
			snapshot, err := client.GetPushSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ControllerID != controller {
				t.Fatal("stop observation changed controller incarnation")
			}
			found := false
			for _, push := range snapshot.Pushes {
				found = found || push.ID == writer.ID
			}
			if !found {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("stopped recording writer never exited its controller inventory")
	})
	checksums := func(t *testing.T, dir string) map[string]string {
		t.Helper()
		out := exec(t, "sha256sum "+dir+"/segments/*.ts")
		result := make(map[string]string)
		for line := range strings.SplitSeq(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				result[fields[1]] = fields[0]
			}
		}
		return result
	}
	for _, isolated := range []bool{false, true} {
		t.Run(fmt.Sprintf("namespaced=%v", isolated), func(t *testing.T) {
			dir := fmt.Sprintf("/tmp/record-%v", isolated)
			exec(t, "mkdir -p "+dir+"/segments")
			first, second := "", ""
			if isolated {
				first, second = "84ed1d3113d2468db7b5ef132d4e3ee9_", "faed1d3113d2468db7b5ef132d4e3ee9_"
			}
			target := func(prefix string) string {
				return "'" + dir + "/segments/" + prefix + "fixture_$segmentCounter.ts?m3u8=../recording.m3u8&split=2&targetAge=4&maxEntries=2&append=1&noendlist=1&nounlink=1'"
			}
			// Each exec returns only after the previous output process exited.
			exec(t, "timeout 40 MistOutHTTPTS -s first "+target(first))
			before := checksums(t, dir)
			playlist := exec(t, "cat "+dir+"/recording.m3u8")
			if len(before) < 4 || strings.Count(playlist, "#EXTINF:") > 2 {
				t.Fatalf("fixture did not retain files outside a pruned playlist: files=%d\n%s", len(before), playlist)
			}
			exec(t, "timeout 25 MistOutHTTPTS -s second "+target(second))
			after := checksums(t, dir)
			changed := 0
			for path, sum := range before {
				if after[path] != sum {
					changed++
				}
			}
			if !isolated {
				if changed == 0 {
					t.Fatal("control did not reproduce segment filename reuse after playlist pruning")
				}
				t.Logf("reproduced modification of %d prior segment(s) with shared filenames", changed)
				return
			}
			if changed != 0 || len(after) <= len(before) {
				t.Fatalf("replacement writer changed old media or wrote no new segment: changed=%d before=%d after=%d", changed, len(before), len(after))
			}
			playlist = exec(t, "cat "+dir+"/recording.m3u8")
			if !strings.Contains(playlist, second) {
				t.Fatalf("same recording manifest did not include its replacement source: %s", playlist)
			}
			exec(t, "timeout 15 ffmpeg -hide_banner -loglevel error -threads 1 -i "+dir+"/recording.m3u8 -frames:v 3 -an -f null -")
		})
	}
}
