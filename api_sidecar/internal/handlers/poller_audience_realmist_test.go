//go:build media_verify

package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/gin-gonic/gin"
)

func TestAudienceViewers_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact Mist/ffmpeg image under test")
	}
	setupTriggerTest(t, "audience-tenant")
	pm := newStreamWSTestMonitor(t)
	pm.updateChannel = make(chan models.NodeUpdate, 1)
	oldMonitor := prometheusMonitor
	prometheusMonitor = pm
	t.Cleanup(func() { prometheusMonitor = oldMonitor })
	stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return &control.MistTriggerResult{Response: "true"}, nil
	})
	router := gin.New()
	router.POST("/user-new", HandleUserNew)
	router.POST("/push-rewrite", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		fields := strings.Split(string(body), "\n")
		if err != nil || len(fields) < 3 {
			c.String(400, "bad push")
			return
		}
		c.String(200, fields[2])
	})
	hook := httptest.NewUnstartedServer(router)
	if err := hook.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	hook.Listener = listener
	hook.Start()
	t.Cleanup(hook.Close)
	_, hookPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	webhook := "http://host.docker.internal:" + hookPort
	config := map[string]any{
		"account": map[string]any{"test": map[string]any{"password": "098f6bcd4621d373cade4e832627b4f6"}},
		"config": map[string]any{"device_discovery": false, "prometheus": "metrics", "sessionViewerMode": 14, "tknMode": 15,
			"protocols": []any{map[string]any{"connector": "HTTP", "port": 18080}, map[string]any{"connector": "RTMP", "port": 1935}, map[string]any{"connector": "HLS"}, map[string]any{"connector": "EBML"}},
			"triggers":  map[string]any{"USER_NEW": []any{map[string]any{"handler": webhook + "/user-new", "sync": true, "onfail": "deny"}}, "PUSH_REWRITE": []any{map[string]any{"handler": webhook + "/push-rewrite", "sync": true, "onfail": "deny"}}},
		},
		"streams": map[string]any{"source": map[string]any{"source": "push://", "DVR": 120000, "bufferTime": 120000}},
	}
	payload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audience.json")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-audience-contract-%d", time.Now().UnixNano())
	var runtimes []*mistNodeRuntime
	t.Cleanup(func() {
		if t.Failed() {
			if logs, err := dockerpg.CLI("logs", "--tail", "40", name); err == nil {
				t.Logf("Mist audience fixture: %s", logs)
			}
		}
		if out, err := dockerpg.CLI("rm", "-fv", name); err != nil {
			t.Errorf("remove owned fixture: %v %s", err, out)
		}
		for _, nodeRuntime := range runtimes {
			nodeRuntime.stop()
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -f lavfi -i sine=frequency=440:sample_rate=48000 -t 4 -c:v libx264 -g 10 -bf 0 -pix_fmt yuv420p -c:a aac /tmp/source.mp4 && exec MistController -c /tmp/audience.json"
	args := []string{"run", "--pull=never", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::4242", "-v", path + ":/tmp/audience.json:ro"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(args, "--entrypoint", "/bin/sh", image, "-c", command)
	if out, err := dockerpg.Run(args...); err != nil {
		t.Fatalf("start owned Mist fixture: %v %s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "4242/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + port
	replaceRuntime := func(generation uint64) {
		nodeRuntime := newMistNodeRuntime(generation, triggerTestNodeID, base, "test", "test")
		runtimes = append(runtimes, nodeRuntime)
		pm.mutex.Lock()
		pm.nodeID, pm.baseURL = triggerTestNodeID, base
		pm.mutex.Unlock()
		pm.nodeRuntimeMu.Lock()
		pm.nodeRuntime = nodeRuntime
		pm.nodeRuntimeMu.Unlock()
	}
	replaceRuntime(1)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := dockerpg.CLI("exec", name, "sh", "-c", "test -s /tmp/source.mp4 && pgrep MistController >/dev/null"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Mist controller did not start")
		}
		time.Sleep(250 * time.Millisecond)
	}
	publisher := "for attempt in 1 2 3 4 5; do ffmpeg -hide_banner -loglevel error -re -stream_loop -1 -i /tmp/source.mp4 -t 90 -c copy -flvflags no_metadata -f flv rtmp://127.0.0.1:1935/live/source && exit; sleep 1; done"
	hls := "for attempt in 1 2 3 4 5; do ffmpeg -hide_banner -loglevel error -re -i 'http://127.0.0.1:18080/hls/source/index.m3u8?tkn=audience-viewer' -t 90 -f null - && exit; sleep 1; done"
	processing := "for attempt in 1 2 3 4 5; do curl -fsS --max-time 90 'http://127.0.0.1:18080/source.mkv?tkn=fwproc-audience' -o /dev/null && exit; sleep 1; done"
	for _, reader := range []string{publisher, hls, processing} {
		if out, err := dockerpg.CLI("exec", "-d", name, "sh", "-c", reader); err != nil {
			t.Fatalf("launch media connection: %v %s", err, out)
		}
	}
	// The clients API samples five seconds in the past; wait for both readers' first sample.
	select {
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-time.After(8 * time.Second):
	}
	awaitAudience := func() {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		var last *ipcpb.StreamData
		var lastErr error
		for {
			pm.emitNodeLifecycleRuntime(pm.currentNodeRuntime())
			update := <-pm.updateChannel
			lastErr = update.Error
			if lastErr == nil {
				last = pm.nodeLifecycleTrigger(triggerTestNodeID, update.JSONData).GetNodeLifecycleUpdate().GetStreams()["source"]
			}
			if last != nil && last.AudienceViewers != nil && last.GetAudienceViewers() == 1 && last.GetTotal() == 2 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("raw Mist viewers / audience: %v (poll error %v), want 2 / 1", last, lastErr)
			}
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(10 * time.Second):
			}
		}
	}
	// Restart before the first census can repair a tag rejected during USER_NEW.
	replaceRuntime(2)
	awaitAudience()
	replaceRuntime(3)
	awaitAudience()
}
