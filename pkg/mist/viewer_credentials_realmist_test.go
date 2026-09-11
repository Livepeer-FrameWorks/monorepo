//go:build media_verify

package mist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// The hook checks transport of the token, not its cryptographic validity. No
// CONN_PLAY gate or client-side credential repair participates in this fixture.
func TestViewerCredentials_RealMist(t *testing.T) {
	image := os.Getenv("MIST_CONTRACT_IMAGE")
	if image == "" {
		t.Fatal("MIST_CONTRACT_IMAGE must identify the exact image under test (Mist and ffmpeg required)")
	}
	fixture, fixtureErr := filepath.Abs("testdata/viewer-credentials.json")
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	hook, hookErr := filepath.Abs("testdata/viewer-credentials-hook.sh")
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	var rewriteMu sync.Mutex
	var rewriteRequests []string
	hookServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 16<<10))
		fields := strings.Split(string(body), "\n")
		if readErr != nil || len(fields) < 4 {
			http.Error(w, "invalid trigger", http.StatusBadRequest)
			return
		}
		rewriteMu.Lock()
		rewriteRequests = append(rewriteRequests, fields[2]+":"+fields[0])
		rewriteMu.Unlock()
		if fields[0] == "blocked-replica" {
			w.Header().Set("X-Mist-Trigger-Action", "deny")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Mist-Trigger-Action", "value")
		_, _ = io.WriteString(w, fields[0])
	}))
	if closeErr := hookServer.Listener.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	listener, listenErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp4", "0.0.0.0:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	hookServer.Listener = listener
	hookServer.Start()
	t.Cleanup(hookServer.Close)
	_, hookPort, splitErr := net.SplitHostPort(listener.Addr().String())
	if splitErr != nil {
		t.Fatal(splitErr)
	}
	configuration, configErr := os.ReadFile(fixture)
	if configErr != nil {
		t.Fatal(configErr)
	}
	// The callback listens on the host so the container exercises the same typed
	// HTTP trigger outcome as Helmsman, including a successful response with deny.
	configuration = []byte(strings.ReplaceAll(string(configuration), "http://host.docker.internal:0/play-rewrite", "http://host.docker.internal:"+hookPort+"/play-rewrite"))
	if !json.Valid(configuration) {
		t.Fatal("invalid runtime Mist fixture")
	}
	fixture = filepath.Join(t.TempDir(), "viewer-credentials.json")
	if writeErr := os.WriteFile(fixture, configuration, 0600); writeErr != nil {
		t.Fatal(writeErr)
	}
	sourceHook, sourceHookErr := filepath.Abs("testdata/source-start-audit-hook.sh")
	if sourceHookErr != nil {
		t.Fatal(sourceHookErr)
	}
	name := fmt.Sprintf("fw-viewer-credentials-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if output, err := dockerpg.CLI("rm", "-f", name); err != nil {
			t.Errorf("remove owned Mist fixture: %v %s", err, output)
		}
	})
	command := "ffmpeg -hide_banner -loglevel error -f lavfi -i testsrc2=size=160x90:rate=10 -t 12 -c:v libx264 -g 10 -pix_fmt yuv420p /tmp/source.mp4 && exec MistController -c /tmp/viewer-credentials.json"
	args := []string{"run", "--pull=never", "-d", "--name", name, "--shm-size=256m", "-p", "127.0.0.1::18080",
		"-v", fixture + ":/tmp/viewer-credentials.json:ro", "-v", hook + ":/tmp/viewer-credentials-hook.sh:ro",
		"-v", sourceHook + ":/tmp/source-start-audit-hook.sh:ro"}
	if runtime.GOOS == "linux" {
		args = append(args, "--add-host", "host.docker.internal:host-gateway")
	}
	args = append(args, "--entrypoint", "/bin/sh", image, "-c", command)
	if output, err := dockerpg.Run(args...); err != nil {
		t.Fatalf("start isolated media fixture: %v %s", err, output)
	}
	port, portErr := dockerpg.DiscoverPublishedHostPort(name, "18080/tcp")
	if portErr != nil {
		t.Fatal(portErr)
	}
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	fetch := func(rawURL string) (string, int, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
		if err != nil {
			return "", 0, err
		}
		response, err := client.Do(req)
		if err != nil {
			return "", 0, err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		return string(body), response.StatusCode, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	playlistURL := base + "/hls/protected-replica/index.m3u8?jwt=fixture-viewer-token"
	mediaURL := ""
	for ctx.Err() == nil && mediaURL == "" {
		body, status, err := fetch(playlistURL)
		if err == nil && status == http.StatusOK {
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parent, _ := url.Parse(playlistURL)
				reference, parseErr := url.Parse(line)
				if parseErr != nil {
					t.Fatal("invalid media reference")
				}
				child := parent.ResolveReference(reference)
				if child.Host != parent.Host || child.Scheme != parent.Scheme {
					t.Fatal("fixture advertised an external media endpoint")
				}
				if child.Query().Get("tkn") != "fixture-viewer-token" && child.Query().Get("jwt") != "fixture-viewer-token" {
					t.Fatal("Mist lost viewer credential in a playlist reference")
				}
				if strings.Contains(child.Path, ".m3u8") {
					playlistURL = child.String()
					break
				}
				segment, code, segmentErr := fetch(child.String())
				if segmentErr == nil && code == http.StatusOK && isViewerFixtureMedia(segment) {
					mediaURL = child.String()
					break
				}
			}
		}
		if mediaURL == "" {
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
	if mediaURL == "" {
		logs, _ := dockerpg.CLI("logs", "--tail", "45", name)
		t.Fatalf("authenticated DTSC replica did not deliver MPEG-TS media: %s", logs)
	}
	// USER_NEW gates media sessions, not all playlist metadata requests. Record
	// that boundary separately; a copied segment path must still require admission.
	for _, raw := range []string{base + "/hls/protected-replica/index.m3u8", mediaURL} {
		for _, token := range []string{"", "wrong-viewer-token"} {
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			query := u.Query()
			query.Del("tkn")
			query.Del("jwt")
			if token != "" {
				query.Set("jwt", token)
			}
			u.RawQuery = query.Encode()
			body, _, err := fetch(u.String())
			if err != nil {
				t.Fatal("could not verify credential rejection")
			}
			if strings.Contains(body, "fixture-viewer-token") || isViewerFixtureMedia(body) {
				t.Errorf("credential rejection exposed an accepted token or media: path=%s supplied=%t", u.Path, token != "")
			}
			if strings.Contains(body, "#EXTM3U") {
				t.Logf("playlist metadata is available without session admission: path=%s supplied=%t", u.Path, token != "")
			}
		}
	}
	segment, status, err := fetch(mediaURL)
	if err != nil || status != http.StatusOK || !isViewerFixtureMedia(segment) {
		t.Fatal("authorized segment is unavailable after rejection checks; cannot attribute rejection to admission")
	}
	audit, err := dockerpg.CLI("exec", name, "cat", "/tmp/viewer-credentials-audit")
	t.Logf("USER_NEW decisions: %s", strings.TrimSpace(audit))
	if err != nil || !strings.Contains(audit, "allow\n") || !strings.Contains(audit, "deny\n") {
		t.Fatalf("did not observe both USER_NEW admission outcomes: %v %s", err, audit)
	}
	for _, path := range []string{"/hls/blocked-replica/index.m3u8", "/json_blocked-replica.js"} {
		body, code, fetchErr := fetch(base + path)
		t.Logf("pre-source denied response: path=%s status=%d body=%q", path, code, body)
		if fetchErr != nil {
			logs, _ := dockerpg.CLI("logs", "--tail", "25", name)
			t.Errorf("pre-source denied request failed transport: path=%s err=%v logs=%s", path, fetchErr, logs)
		}
		if strings.Contains(body, "#EXTM3U") || isViewerFixtureMedia(body) {
			t.Fatalf("PLAY_REWRITE denial exposed media or playlist: %s", path)
		}
	}
	rewriteMu.Lock()
	rewrites := strings.Join(rewriteRequests, "\n") + "\n"
	rewriteMu.Unlock()
	if !strings.Contains(rewrites, "HLS:blocked-replica\n") || !strings.Contains(rewrites, "HTTP:blocked-replica\n") {
		t.Errorf("pre-source gate did not observe both media and metadata requests: %s", rewrites)
	}
	verifyViewerMetadataRequestLifecycle(t, base, func(stream string) int {
		rewriteMu.Lock()
		defer rewriteMu.Unlock()
		count := 0
		for _, request := range rewriteRequests {
			if request == "HTTP:"+stream {
				count++
			}
		}
		return count
	})
	sources, sourceAuditErr := dockerpg.CLI("exec", name, "cat", "/tmp/source-start-audit")
	t.Logf("source starts: %s", strings.TrimSpace(sources))
	if sourceAuditErr != nil || !strings.Contains(sources, "protected-replica\n") || strings.Contains(sources, "blocked-replica") {
		t.Fatalf("denied request reached source startup or positive source control is absent: %v %s", sourceAuditErr, sources)
	}
}

func verifyViewerMetadataRequestLifecycle(t *testing.T, base string, rewriteCount func(string) int) {
	t.Helper()
	transport := &http.Transport{MaxConnsPerHost: 1}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 12 * time.Second, Transport: transport}
	for _, stream := range []string{"protected-replica", "blocked-replica", "protected-replica"} {
		before := rewriteCount(stream)
		var reused atomic.Bool
		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}
		request, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, base+"/json_"+stream+".js", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Errorf("metadata request transport failed for %s: %v", stream, err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		closeErr := response.Body.Close()
		t.Logf("metadata lifecycle: stream=%s reused_connection=%t status=%d", stream, reused.Load(), response.StatusCode)
		if readErr != nil || closeErr != nil || rewriteCount(stream) != before+1 {
			t.Errorf("metadata request did not complete one fresh policy check for %s: read=%v close=%v", stream, readErr, closeErr)
		}
		if stream == "blocked-replica" {
			var result struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &result); err != nil || result.Error != "Playback rejected by PLAY_REWRITE trigger" {
				t.Error("denied metadata request reused a previous request's admission")
			}
		}
	}
}

func isViewerFixtureMedia(body string) bool {
	return len(body) >= 3*188 && body[0] == 0x47 && body[188] == 0x47 && body[376] == 0x47
}
