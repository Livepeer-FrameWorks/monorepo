//go:build media_verify

package mist

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestProcessingRecording_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify a full Mist/ffmpeg build")
	}
	for _, fixture := range []struct {
		name     string
		thumbs   bool
		ladder   bool
		duration int
	}{
		{"passthrough", false, false, 12},
		{"thumbnails", true, false, 12},
		{"short-multi-rendition", true, true, 6},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			name := fmt.Sprintf("fw-processing-recording-%d", time.Now().UnixNano())
			t.Cleanup(func() {
				if t.Failed() && os.Getenv("MIST_CONTRACT_KEEP_FAILED") == "1" {
					t.Logf("preserved failed fixture: %s", name)
					return
				}
				if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
					t.Errorf("remove owned fixture: %v %s", err, output)
				}
			})
			stream := map[string]any{"source": "/tmp/source.mp4", "realtime": true, "process_controlled_realtime": true}
			if os.Getenv("MIST_CONTRACT_DEBUG") == "1" {
				stream["debug"] = 5
			}
			var processes []any
			if fixture.thumbs {
				processes = append(processes, map[string]any{"process": "Thumbs", "track_select": "video=H264&audio=none", "interval": 1000, "thumb_width": 80, "thumb_height": 48, "grid_cols": 3, "grid_rows": 2, "restart_type": "disabled"})
			}
			if fixture.ladder {
				processes = append(processes, map[string]any{"process": "AV", "codec": "opus", "track_select": "audio=all&video=none&subtitle=none", "restart_type": "disabled"})
				for _, height := range []int{360, 480, 720, 1080} {
					processes = append(processes, map[string]any{"process": "AV", "codec": "H264", "resolution": fmt.Sprintf("x%d", height), "bitrate": height * 4000, "profile": "high", "source_mask": 4, "track_select": "video=maxbps&audio=none&subtitle=none", "restart_type": "disabled"})
				}
			}
			if len(processes) > 0 {
				stream["processes"] = processes
			}
			config, err := json.Marshal(map[string]any{"config": map[string]any{"device_discovery": false, "debug": 5, "protocols": []any{}}, "streams": map[string]any{"processing": stream}})
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, config, 0o600); err != nil {
				t.Fatal(err)
			}
			command := fmt.Sprintf("ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t %d -c:v libx264 -g 10 -bf 0 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && exec MistController -c /tmp/config.json", fixture.duration)
			if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-v", configPath+":/tmp/config.json:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
				t.Fatalf("start fixture: %v %s", err, output)
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				logs, _ := dockerpg.CLI("logs", name)
				if strings.Contains(logs, "Controller started") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("controller did not start: %s", logs)
				}
				time.Sleep(250 * time.Millisecond)
			}
			output, err := dockerpg.Run("exec", name, "timeout", "65", "MistOutEBML", "-s", "processing", "/tmp/recording.mkv?video=all&audio=all&meta=all")
			if err != nil {
				logs, _ := dockerpg.CLI("logs", "--tail", "60", name)
				t.Fatalf("recording failed: %v\n%s\n%s", err, output, logs)
			}
			probe, err := dockerpg.CLI("exec", name, "sh", "-c", "ffprobe -v error -count_packets -show_entries stream=codec_name,nb_read_packets -of json /tmp/recording.mkv 2>/tmp/probe-errors")
			if err != nil {
				probeErrors, _ := dockerpg.CLI("exec", name, "cat", "/tmp/probe-errors")
				t.Fatalf("probe failed: %v %s\n%s\n%s", err, probe, probeErrors, output)
			}
			probeErrors, err := dockerpg.CLI("exec", name, "cat", "/tmp/probe-errors")
			if err != nil || strings.TrimSpace(probeErrors) != "" {
				t.Fatalf("invalid recording: %v\n%s\n%s\n%s", err, probeErrors, probe, output)
			}
			if !strings.Contains(probe, "h264") || !strings.Contains(probe, "aac") || (fixture.thumbs && !strings.Contains(probe, "mjpeg")) {
				logs, _ := dockerpg.CLI("logs", "--tail", "100", name)
				t.Fatalf("missing expected media/enrichment: %s\n%s\n%s", probe, output, logs)
			}
			var media struct {
				Streams []struct {
					Codec   string `json:"codec_name"`
					Packets string `json:"nb_read_packets"`
				} `json:"streams"`
			}
			if err := json.Unmarshal([]byte(probe), &media); err != nil {
				t.Fatal(err)
			}
			jpegTracks := 0
			videoTracks := 0
			for _, track := range media.Streams {
				packets, err := strconv.Atoi(track.Packets)
				if err != nil || packets == 0 {
					t.Fatalf("empty/unreadable %s track: %s", track.Codec, probe)
				}
				audioPackets := 564
				if fixture.duration == 6 {
					audioPackets = 283
				}
				if (track.Codec == "h264" && packets != fixture.duration*10) || (track.Codec == "aac" && packets != audioPackets) {
					t.Fatalf("recording lost source packets: %s", probe)
				}
				if track.Codec == "mjpeg" {
					jpegTracks++
				}
				if track.Codec == "h264" {
					videoTracks++
				}
			}
			if fixture.ladder && videoTracks != 4 {
				t.Fatalf("recording must retain all four video renditions: %s", probe)
			}
			if fixture.thumbs && jpegTracks != 2 {
				t.Fatalf("recording must retain both sprite and preview JPEG tracks: %s", probe)
			}
			t.Logf("recorded declared media tracks: %s", probe)
		})
	}
}
