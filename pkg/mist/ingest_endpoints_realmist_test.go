//go:build media_verify

package mist

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestIngestProtocolReport_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact image under test")
	}
	fixture, err := filepath.Abs("testdata/placement-protocols.json")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-placement-mist-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, cleanupErr := dockerpg.CLI("rm", "-f", name); cleanupErr != nil {
			t.Errorf("remove owned Mist test container: %v %s", cleanupErr, output)
		}
	})
	// The image under test has no ENTRYPOINT (CMD is /bin/sh), so the controller
	// must be named explicitly; otherwise "-c" is exec'd as the program. The other
	// real-Mist tests go through /bin/sh because they first render a source file
	// with ffmpeg; this one needs only the controller against its fixture.
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-p", "127.0.0.1::4242", "-v", fixture+":/tmp/placement.json:ro", "--entrypoint", "MistController", image, "-c", "/tmp/placement.json"); runErr != nil {
		t.Fatalf("start isolated Mist: %v %s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "4242/tcp")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(logging.NewLogger(), ClientConfig{BaseURL: "http://127.0.0.1:" + port, Username: "test", Password: "test"})
	waitReport := func(predicate func(map[string]any, IngestURLs) bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		var last IngestURLs
		var lastOutputs map[string]any
		for ctx.Err() == nil {
			probe, stop := context.WithTimeout(ctx, 2*time.Second)
			data, fetchErr := client.FetchJSONContext(probe, "/placement-contract.json")
			stop()
			if fetchErr == nil {
				outputs, outputsOK := data["outputs"].(map[string]any)
				if !outputsOK {
					outputs = nil
				}
				lastOutputs = outputs
				last = ResolveIngestURLs(outputs, "https://edge.example", "key")
				if predicate(outputs, last) {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		logs, logErr := dockerpg.CLI("logs", "--tail", "20", name)
		t.Fatalf("Mist listener contract did not converge: %+v; outputs=%v; logs error=%v\n%s", last, lastOutputs, logErr, logs)
	}
	waitReport(func(_ map[string]any, urls IngestURLs) bool {
		return urls.RTMP == "rtmp://edge.example:11935/live/key" && urls.SRT == "srt://edge.example:18889/?streamid=key"
	})
	if updateErr := client.UpdateProtocol(
		map[string]any{"connector": "HTTP", "port": 18080, "pubaddr": []string{"https://media.example/view/"}},
		map[string]any{"connector": "HTTP", "port": 18080, "pubaddr": "https://media.example/view/"},
	); updateErr != nil {
		t.Fatal(updateErr)
	}
	waitReport(func(outputs map[string]any, urls IngestURLs) bool {
		// MP4 has method-level URLs, which this metrics report does not emit.
		// A configured but unreported connector cannot establish listener support.
		return urls.WHIP == "https://media.example/view/webrtc/key" && SupportsIngestProtocol(outputs, "https://edge.example", "whip") &&
			ResolvePlaybackURL(outputs, "https://edge.example", "hls", "key") == "https://media.example/view/hls/key/index.m3u8" &&
			ResolvePlaybackURL(outputs, "https://edge.example", "dash", "key") == "https://media.example/view/cmaf/key/index.mpd" &&
			ResolvePlaybackURL(outputs, "https://edge.example", "mp4", "key") == "" &&
			ResolvePlaybackURL(outputs, "https://edge.example", "whep", "key") == "https://media.example/view/whep/key"
	})
	if deleteErr := client.DeleteProtocols([]map[string]any{{"connector": "WebRTC", "port": 18203}}); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	waitReport(func(outputs map[string]any, urls IngestURLs) bool {
		_, advertised := outputs["WebRTC"]
		return !advertised && urls.WHIP == "" && urls.RTMP != "" && urls.SRT != "" &&
			!SupportsIngestProtocol(outputs, "https://edge.example", "whip") && ResolvePlaybackURL(outputs, "https://edge.example", "whep", "key") == ""
	})
}
