//go:build media_verify

package mist

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestLargeVODPlayback_RealMist(t *testing.T) {
	image, source := os.Getenv("MIST_CONTRACT_IMAGE"), os.Getenv("MIST_LARGE_SOURCE_FILE")
	if image == "" || source == "" {
		t.Fatal("MIST_CONTRACT_IMAGE and MIST_LARGE_SOURCE_FILE must identify the qualified image and a protected large MKV")
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= 1<<31 {
		t.Fatal("the fixture must exercise a file larger than signed 32-bit length")
	}
	config, err := json.Marshal(map[string]any{
		"config":  map[string]any{"device_discovery": false, "debug": 5, "protocols": []any{map[string]any{"connector": "HTTP", "port": 18080}, map[string]any{"connector": "HLS"}}},
		"streams": map[string]any{"large-vod": map[string]any{"source": "/tmp/source.mkv", "realtime": false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-large-vod-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		logs, logErr := dockerpg.CLI("logs", name)
		t.Logf("large VOD log (error=%v):\n%s", logErr, logs)
		if output, removeErr := dockerpg.CLI("rm", "-f", "-v", name); removeErr != nil && !strings.Contains(output, "No such container") {
			t.Errorf("remove owned fixture: %v %s", removeErr, output)
		}
	})
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "--shm-size=512m", "-p", "127.0.0.1::18080", "-v", source+":/tmp/source.mkv:ro", "-v", configPath+":/tmp/config.json:ro", "--entrypoint", "MistController", image, "-c", "/tmp/config.json"); runErr != nil {
		t.Fatalf("start large VOD: %v %s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 25 * time.Second}
	fetch := func(raw string) ([]byte, error) {
		response, fetchErr := client.Get(raw)
		if fetchErr != nil {
			return nil, fetchErr
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, body)
		}
		return body, readErr
	}
	root := "http://127.0.0.1:" + port + "/hls/large-vod/index.m3u8"
	var playlist []byte
	deadline := time.Now().Add(330 * time.Second)
	for time.Now().Before(deadline) {
		playlist, err = fetch(root)
		if err == nil && strings.Contains(string(playlist), "#EXTM3U") && !strings.Contains(string(playlist), "#EXT-X-ERROR") {
			break
		}
		running, stateErr := dockerpg.CLI("inspect", "-f", "{{.State.Running}}", name)
		if stateErr != nil || strings.TrimSpace(running) != "true" {
			t.Fatalf("large VOD fixture stopped before playback: %v %s", stateErr, running)
		}
		time.Sleep(time.Second)
	}
	if err != nil || !strings.Contains(string(playlist), "#EXTM3U") || strings.Contains(string(playlist), "#EXT-X-ERROR") {
		t.Fatalf("large VOD root playlist: %v %s", err, playlist)
	}
	resolve := func(parent, child string) string {
		base, baseErr := url.Parse(parent)
		if baseErr != nil {
			t.Fatal(baseErr)
		}
		ref, parseErr := url.Parse(child)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return base.ResolveReference(ref).String()
	}
	variantURL := resolve(root, firstVODPlaylistReference(string(playlist)))
	variant, err := fetch(variantURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(variant), "#EXT-X-ENDLIST") {
		t.Fatalf("finite large VOD has no end marker: %s", variant)
	}
	if output, headerErr := dockerpg.CLI("exec", name, "test", "-s", "/tmp/source.mkv.dtsh"); headerErr != nil {
		t.Fatalf("controller did not generate the source index: %v %s", headerErr, output)
	}
	probe, probeErr := dockerpg.Run("exec", name, "ffprobe", "-v", "error", "-show_entries", "packet=pts_time,duration_time", "-of", "csv=p=0", "/tmp/source.mkv")
	if probeErr != nil {
		t.Fatalf("scan protected source timestamps: %v", probeErr)
	}
	firstPTS, lastPTS := math.Inf(1), math.Inf(-1)
	for _, packet := range strings.Split(probe, "\n") {
		fields := strings.Split(packet, ",")
		pts, ptsErr := strconv.ParseFloat(fields[0], 64)
		if ptsErr != nil {
			continue
		}
		var duration float64
		if len(fields) > 1 && fields[1] != "N/A" {
			duration, _ = strconv.ParseFloat(fields[1], 64)
		}
		firstPTS = math.Min(firstPTS, pts)
		lastPTS = math.Max(lastPTS, pts+duration)
	}
	expectedDuration := lastPTS - firstPTS
	if math.IsInf(expectedDuration, 0) || math.IsNaN(expectedDuration) || expectedDuration <= 0 {
		t.Fatal("protected source contains no measurable packet duration")
	}
	var playlistDuration, targetDuration float64
	var segments []string
	for _, line := range strings.Split(string(variant), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#EXTINF:") {
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			duration, parseErr := strconv.ParseFloat(value, 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			playlistDuration += duration
		}
		if strings.HasPrefix(line, "#EXT-X-TARGETDURATION:") {
			var parseErr error
			targetDuration, parseErr = strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			segments = append(segments, resolve(variantURL, line))
		}
	}
	t.Logf("indexed %.3fs of source into %.3fs of HLS", expectedDuration, playlistDuration)
	if math.Abs(playlistDuration-expectedDuration) > math.Max(2*targetDuration, 2) {
		t.Fatalf("large-file index is truncated: HLS duration %.3fs, source %.3fs", playlistDuration, expectedDuration)
	}
	if len(segments) < 3 {
		t.Fatalf("expected beginning, middle and end, got %d segments", len(segments))
	}
	for _, index := range []int{0, len(segments) / 2, len(segments) - 1} {
		media, fetchErr := fetch(segments[index])
		if fetchErr != nil || len(media) < 3*188 || media[0] != 0x47 || media[188] != 0x47 || media[376] != 0x47 {
			t.Fatalf("large VOD segment %d: bytes=%d error=%v", index, len(media), fetchErr)
		}
		path := filepath.Join(t.TempDir(), "sample.ts")
		if err = os.WriteFile(path, media, 0o600); err != nil {
			t.Fatal(err)
		}
		if output, copyErr := dockerpg.CLI("cp", path, name+":/tmp/sample.ts"); copyErr != nil {
			t.Fatalf("copy sample: %v %s", copyErr, output)
		}
		if output, decodeErr := dockerpg.Run("exec", name, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-i", "/tmp/sample.ts", "-map", "0:v:0", "-map", "0:a:0", "-t", "2", "-f", "null", "-"); decodeErr != nil {
			t.Fatalf("decode audio/video segment %d: %v %s", index, decodeErr, output)
		}
		t.Logf("decoded audio and video at segment %d/%d (%d bytes)", index, len(segments), len(media))
	}
}
