//go:build media_verify

package mist

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestHLSChapterRealtime_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact Mist/ffmpeg image under test")
	}
	fixture, err := filepath.Abs("testdata/hls-realtime.json")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-hls-realtime-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 30 -c:v libx264 -g 10 -pix_fmt yuv420p -c:a aac -f hls -hls_time 3 -hls_playlist_type vod /tmp/chapter.m3u8 && exec MistController -c /tmp/hls-realtime.json"
	if output, err := dockerpg.Run("run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080", "-v", fixture+":/tmp/hls-realtime.json:ro", "--entrypoint", "/bin/sh", image, "-c", command); err != nil {
		t.Fatalf("start fixture: %v %s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for _, stream := range []string{"direct", "realtime", "processing"} {
		t.Run(stream, func(t *testing.T) {
			deadline := time.Now().Add(35 * time.Second)
			for time.Now().Before(deadline) {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:"+port+"/json_"+stream+".js", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := client.Do(req)
				if err == nil {
					var info struct {
						Meta struct {
							Tracks map[string]json.RawMessage `json:"tracks"`
						} `json:"meta"`
					}
					decodeErr := json.NewDecoder(resp.Body).Decode(&info)
					resp.Body.Close()
					if decodeErr == nil && len(info.Meta.Tracks) >= 2 {
						return
					}
				}
				time.Sleep(250 * time.Millisecond)
			}
			logs, _ := dockerpg.CLI("logs", "--tail", "50", name)
			t.Fatalf("HLS source did not expose video and audio tracks\n%s", logs)
		})
	}
}
