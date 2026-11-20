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

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/transcode/contract"
)

func TestProcessingRenditionGeometry_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify a full Mist/ffmpeg build")
	}
	var catalog []struct {
		Profiles []LivepeerJSONProfile `json:"target_profiles"`
	}
	if err := json.Unmarshal([]byte(catalogLivepeerProcesses), &catalog); err != nil {
		t.Fatal(err)
	}
	rungs := make([]contract.RenditionSpec, 0, len(catalog[0].Profiles)+1)
	for _, profile := range catalog[0].Profiles {
		rungs = append(rungs, contract.RenditionSpec{
			Name: profile["name"].(string), Codec: "H264", Profile: "high",
			Resolution: fmt.Sprintf("x%.0f", profile["height"]), Bitrate: int64(profile["bitrate"].(float64)),
			TrackInhibit: profile["track_inhibit"].(string),
		})
	}
	rungs = append(rungs, contract.RenditionSpec{Name: "1440p", Codec: "H264", Profile: "high", Resolution: "x1440", Bitrate: 10800000, TrackInhibit: "video=<2560x1440"})
	ladder, err := SetTranscodeLadder(catalogLivepeerProcesses, rungs)
	if err != nil {
		t.Fatal(err)
	}
	for _, dimensions := range [][2]int{{360, 360}, {960, 540}, {1920, 1200}, {2560, 1440}, {3840, 2160}, {720, 1280}, {1080, 1920}, {1080, 1080}, {1440, 1080}, {3840, 1080}} {
		t.Run(fmt.Sprintf("%dx%d", dimensions[0], dimensions[1]), func(t *testing.T) {
			source := SourceMediaInfo{Width: dimensions[0], Height: dimensions[1], FPS: 10}
			policy := SoftwareEncodingOnly(DisableProcessRestarts(ReplaceLivepeerWithLocal(MaskLivepeerSourceForVOD(ladder))))
			expected, err := ExpectedVideoRenditions(policy, source)
			if err != nil {
				t.Fatal(err)
			}
			var processes []any
			if err := json.Unmarshal([]byte(policy), &processes); err != nil {
				t.Fatal(err)
			}
			config, err := json.Marshal(map[string]any{
				"config": map[string]any{"device_discovery": false, "debug": 5, "protocols": []any{}},
				"streams": map[string]any{"processing": map[string]any{
					"source": "/tmp/source.mp4", "realtime": true, "process_controlled_realtime": true, "processes": processes,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(configPath, config, 0o600); err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("fw-rendition-geometry-%d", time.Now().UnixNano())
			t.Cleanup(func() {
				if t.Failed() && os.Getenv("MIST_CONTRACT_KEEP_FAILED") == "1" {
					t.Logf("preserved fixture: %s", name)
					return
				}
				if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
					t.Errorf("remove owned fixture: %v %s", err, output)
				}
			})
			command := fmt.Sprintf("ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=%dx%d:rate=10 -t 6 -c:v libx264 -threads 2 -preset ultrafast -g 10 -bf 0 -pix_fmt yuv420p /tmp/source.mp4 && exec MistController -c /tmp/config.json", source.Width, source.Height)
			if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-v", configPath+":/tmp/config.json:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
				t.Fatalf("start fixture: %v %s", err, output)
			}
			deadline := time.Now().Add(40 * time.Second)
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
			if output, err := dockerpg.Run("exec", name, "timeout", "90", "MistOutEBML", "-s", "processing", "/tmp/output.mkv?video=all&audio=none&meta=none&subtitle=none"); err != nil {
				logs, _ := dockerpg.CLI("logs", "--tail", "100", name)
				t.Fatalf("recording failed: %v\n%s\n%s", err, output, logs)
			}
			probe, err := dockerpg.CLI("exec", name, "ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=codec_name,width,height", "-of", "json", "/tmp/output.mkv")
			if err != nil {
				t.Fatalf("probe failed: %v %s", err, probe)
			}
			var media struct {
				Streams []struct {
					Codec         string `json:"codec_name"`
					Width, Height int
				}
			}
			if err := json.Unmarshal([]byte(probe), &media); err != nil {
				t.Fatal(err)
			}
			if len(media.Streams) != len(expected) {
				t.Fatalf("recorded tracks = %s; expected %v (source must be masked)", probe, expected)
			}
			counts := map[[2]int]int{}
			for _, track := range media.Streams {
				if track.Codec != "h264" {
					t.Fatalf("unexpected output codec: %s", probe)
				}
				counts[[2]int{track.Width, track.Height}]++
			}
			for _, r := range expected {
				size := [2]int{r.Width, r.Height}
				if counts[size] == 0 {
					t.Fatalf("missing modeled %dx%d rendition: %s", r.Width, r.Height, probe)
				}
				counts[size]--
			}
		})
	}
}
