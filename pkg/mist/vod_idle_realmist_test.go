//go:build media_verify

package mist

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// Header generation does not constitute a media reader. A finished file must
// remain playable when its first HLS segment arrives after that input idles out.
func TestVODFirstSegmentAcrossIdle_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact Mist image under test")
	}
	config, err := json.Marshal(map[string]any{
		"config": map[string]any{"device_discovery": false, "debug": 5, "tknMode": 15, "sessionViewerMode": 14, "sessionStreamInfoMode": 3, "protocols": []any{
			map[string]any{"connector": "HTTP", "port": 18080, "debug": 5},
			map[string]any{"connector": "HLS"}, map[string]any{"connector": "EBML"},
		}, "triggers": map[string]any{"STREAM_SOURCE": []any{map[string]any{"handler": "/tmp/source-hook.sh", "sync": true, "onfail": "offline"}}}},
		"streams": map[string]any{"vod": map[string]any{"source": "/tmp/none", "realtime": false, "debug": 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(filepath.Dir(configPath), "source-hook.sh")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nIFS= read -r stream\nprintf '/tmp/%s.mkv' \"${stream#vod+}\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("fw-vod-idle-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		logs, logErr := dockerpg.CLI("logs", name)
		t.Logf("Mist lifecycle log (error=%v):\n%s", logErr, logs)
		if output, removeErr := dockerpg.CLI("rm", "-f", "-v", name); removeErr != nil && !strings.Contains(output, "No such container") {
			t.Errorf("remove owned Mist fixture: %v %s", removeErr, output)
		}
	})
	args := []string{"run", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080", "-v", configPath + ":/tmp/config.json:ro", "-v", hookPath + ":/tmp/source-hook.sh:ro"}
	prepare := "cp /fixture/source.mkv /tmp/source.mkv"
	// A retained clip can be substituted for diagnosis;
	// without it the committed contract generates its own finite fixture.
	if source := os.Getenv("MIST_IDLE_SOURCE_FILE"); source != "" {
		for _, suffix := range []string{"", ".dtsh"} {
			if _, statErr := os.Stat(source + suffix); statErr != nil {
				t.Fatal(statErr)
			}
			args = append(args, "-v", source+suffix+":/fixture/source.mkv"+suffix+":ro")
		}
		prepare = "cp /fixture/source.mkv /tmp/source.mkv && cp /fixture/source.mkv.dtsh /tmp/source.mkv.dtsh"
	} else {
		toolsImage := os.Getenv("MEDIA_TOOLS_IMAGE")
		if toolsImage == "" {
			t.Fatal("MEDIA_TOOLS_IMAGE must identify the existing ffmpeg tools image for the generated fixture")
		}
		mediaDir := t.TempDir()
		generator := name + "-media"
		t.Cleanup(func() {
			output, removeErr := dockerpg.CLI("rm", "-f", "-v", generator)
			if removeErr != nil && !strings.Contains(output, "No such container") {
				t.Errorf("remove owned media generator: %v %s", removeErr, output)
			}
		})
		if output, generateErr := dockerpg.Run("run", "--rm", "--name", generator, "-v", mediaDir+":/fixture", "--entrypoint", "ffmpeg", toolsImage,
			"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=10", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
			"-t", "7", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-g", "10", "-bf", "0", "-pix_fmt", "yuv420p", "-c:a", "aac", "/fixture/source.mkv"); generateErr != nil {
			t.Fatalf("generate finite media fixture: %v %s", generateErr, output)
		}
		args = append(args, "-v", filepath.Join(mediaDir, "source.mkv")+":/fixture/source.mkv:ro")
	}
	bootstrap := "if test -x /usr/share/frameworks/dist/mistserver/bin/MistController; then export PATH=/usr/share/frameworks/dist/mistserver/bin:$PATH; export LD_LIBRARY_PATH=/usr/share/frameworks/dist/mistserver/opt/mist-onnx/lib:/usr/share/frameworks/dist/mistserver/lib:/usr/share/frameworks/dist/mistserver/lib/x86_64-linux-gnu:/usr/share/frameworks/dist/mistserver/lib/aarch64-linux-gnu; fi; "
	command := bootstrap + prepare + "; for stream in idle-early idle-natural idle-graceful idle-restarted idle-scheduled; do cp /tmp/source.mkv /tmp/$stream.mkv; if test -f /tmp/source.mkv.dtsh; then cp /tmp/source.mkv.dtsh /tmp/$stream.mkv.dtsh; fi; done; exec MistController -c /tmp/config.json"
	args = append(args, "--entrypoint", "/bin/sh", image, "-ec", command)
	if output, runErr := dockerpg.Run(args...); runErr != nil {
		t.Fatalf("start owned Mist fixture: %v %s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + port
	// The stack's urllib viewer closes each connection after consuming its
	// response. A persistent manifest connection would keep Mist's page user
	// alive and refresh the very idle deadline this contract exercises.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Timeout: 20 * time.Second, Transport: transport}
	fetch := func(rawURL string) ([]byte, int, error) {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
		if reqErr != nil {
			return nil, 0, reqErr
		}
		response, fetchErr := client.Do(req)
		if fetchErr != nil {
			return nil, 0, fetchErr
		}
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		return body, response.StatusCode, readErr
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		// A controller process exists before its HTTP connector can answer.
		// Probe a non-stream path so readiness cannot pre-boot a test input.
		if _, status, probeErr := fetch(base + "/crossdomain.xml"); probeErr == nil && status > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned Mist controller did not start")
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, scenario := range []string{"early", "natural", "graceful", "restarted", "scheduled"} {
		t.Run(scenario, func(t *testing.T) {
			stream := "vod+idle-" + scenario
			body, code, bootErr := fetch(base + "/json_" + stream + ".js")
			if bootErr != nil || code != http.StatusOK || bytes.Contains(body, []byte(`"error"`)) {
				t.Fatalf("header boot: status=%d error=%v body=%s", code, bootErr, body)
			}
			bootedAt := time.Now()
			originalInput := 0
			if scenario == "restarted" {
				originalInput = ownedVODInputPID(t, name, stream)
				waitVODIdleBoundary(t.Context(), bootedAt.Add(31500*time.Millisecond))
			}
			var resumed <-chan vodInputResume
			if scenario == "scheduled" {
				waitVODIdleBoundary(t.Context(), bootedAt.Add(27500*time.Millisecond))
				pid := ownedVODInputPID(t, name, stream)
				if output, pauseErr := dockerpg.CLI("exec", name, "kill", "-STOP", strconv.Itoa(pid)); pauseErr != nil {
					t.Fatalf("pause owned input: %v %s", pauseErr, output)
				}
				t.Logf("paused owned input PID%d at %s", pid, time.Since(bootedAt))
				resumed = scheduleVODInputResume(t, name, pid, bootedAt.Add(31100*time.Millisecond))
			}
			if scenario == "natural" || scenario == "scheduled" {
				waitVODIdleBoundary(t.Context(), bootedAt.Add(29*time.Second))
			}
			segment := base + "/hls/" + stream + "/index.m3u8"
			for depth := 0; depth < 2; depth++ {
				playlist, status, playlistErr := fetch(segment)
				if playlistErr != nil || status != http.StatusOK {
					t.Fatalf("playlist depth=%d elapsed=%s status=%d error=%v body=%s", depth, time.Since(bootedAt), status, playlistErr, playlist)
				}
				t.Logf("playlist depth=%d status=%d elapsed=%s", depth, status, time.Since(bootedAt))
				reference := firstVODPlaylistReference(string(playlist))
				if reference == "" {
					t.Fatalf("playlist has no child: %s", playlist)
				}
				parent, _ := url.Parse(segment)
				child, parseErr := url.Parse(reference)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				segment = parent.ResolveReference(child).String()
			}
			if scenario == "natural" || scenario == "scheduled" {
				waitVODIdleBoundary(t.Context(), bootedAt.Add(30500*time.Millisecond))
			}
			if scenario == "graceful" {
				stopOwnedVODInput(t, name, stream)
			}
			if scenario == "restarted" {
				currentInput := ownedVODInputPID(t, name, stream)
				if originalInput == currentInput {
					t.Fatalf("idle input PID%d was not replaced before later playback", originalInput)
				}
				t.Logf("idle input generation changed PID%d -> PID%d", originalInput, currentInput)
			}
			state, stateErr := dockerpg.CLI("exec", name, "sh", "-c", "ps -eo pid,ppid,stat,args; ls -l /dev/shm")
			t.Logf("before first segment elapsed=%s state error=%v:\n%s", time.Since(bootedAt), stateErr, state)
			media, status, segmentErr := fetch(segment)
			if resumed != nil {
				result := <-resumed
				t.Logf("owned input resumed at %s error=%v output=%s", result.at.Sub(bootedAt), result.err, result.output)
				if result.err != nil {
					t.Fatalf("resume owned input: %v %s", result.err, result.output)
				}
			}
			if segmentErr != nil || status != http.StatusOK || len(media) < 3*188 || media[0] != 0x47 || media[188] != 0x47 || media[376] != 0x47 {
				t.Fatalf("first segment must serve MPEG-TS without retry: elapsed=%s url=%s status=%d bytes=%d error=%v", time.Since(bootedAt), segment, status, len(media), segmentErr)
			}
			t.Logf("first segment served %d bytes after %s", len(media), time.Since(bootedAt))
		})
	}
}

func waitVODIdleBoundary(ctx context.Context, deadline time.Time) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func firstVODPlaylistReference(playlist string) string {
	for _, line := range strings.Split(playlist, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

func ownedVODInputPID(t *testing.T, container, stream string) int {
	t.Helper()
	processes, err := dockerpg.CLI("exec", container, "ps", "-eo", "pid,ppid,args")
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[int]int{}
	for _, line := range strings.Split(processes, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.Contains(line, "MistInEBML") || !strings.Contains(line, stream) {
			continue
		}
		candidate, parseErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if parseErr != nil || parentErr != nil || candidate <= 1 {
			t.Fatalf("ambiguous owned input process: %s", processes)
		}
		inputs[candidate] = parent
	}
	// The input launcher remains as the parent of its actual reader. Signal
	// only the single leaf reader, never the controller or an unrelated stream.
	pid := 0
	for candidate := range inputs {
		isParent := false
		for _, parent := range inputs {
			isParent = isParent || parent == candidate
		}
		if !isParent {
			if pid != 0 {
				t.Fatalf("ambiguous owned input readers: %s", processes)
			}
			pid = candidate
		}
	}
	if pid == 0 {
		t.Fatalf("owned stream input process was not found: %s", processes)
	}
	return pid
}

type vodInputResume struct {
	at     time.Time
	output string
	err    error
}

// This forces the normal idle-check ordering where an input was descheduled
// before readers enrolled. The pause ends independently of any blocked HTTP
// read, and cleanup also resumes the exact owned reader on early test failure.
func scheduleVODInputResume(t *testing.T, container string, pid int, at time.Time) <-chan vodInputResume {
	t.Helper()
	var once sync.Once
	var result vodInputResume
	resume := func() {
		once.Do(func() {
			result.output, result.err = dockerpg.CLI("exec", container, "kill", "-CONT", strconv.Itoa(pid))
			result.at = time.Now()
		})
	}
	finished := make(chan vodInputResume, 1)
	cancel := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(cancel)
		resume()
		<-done
	})
	go func() {
		defer close(done)
		timer := time.NewTimer(time.Until(at))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-cancel:
		}
		resume()
		finished <- result
	}()
	return finished
}

func stopOwnedVODInput(t *testing.T, container, stream string) {
	t.Helper()
	pid := ownedVODInputPID(t, container, stream)
	if output, signalErr := dockerpg.CLI("exec", container, "kill", "-TERM", strconv.Itoa(pid)); signalErr != nil {
		t.Fatalf("request graceful exit of owned input: %v %s", signalErr, output)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, aliveErr := dockerpg.CLI("exec", container, "kill", "-0", strconv.Itoa(pid)); aliveErr != nil {
			t.Logf("owned input PID%d exited gracefully after playlist metadata", pid)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("owned input PID%d did not exit after SIGTERM", pid)
}
