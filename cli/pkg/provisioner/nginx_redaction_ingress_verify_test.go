//go:build ingress_verify

package provisioner

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The release-pinned Nginx writes the access log through the
// $frameworks_logged_uri map; no publishing credential may reach it.
func TestNginxAccessLogRedactsPublishingCredentials(t *testing.T) {
	image := os.Getenv("FRAMEWORKS_NGINX_TEST_IMAGE")
	if image == "" {
		t.Fatal("run through make verify-hosted-alias-ingress for the pinned image")
	}
	var nativeVars map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, testRolesDir+"nginx/defaults/main.yml")), &nativeVars); err != nil {
		t.Fatal(err)
	}
	nativeVars["nginx_worker_user"] = "nginx"
	nativeVars["nginx_module_includes"] = []string{}
	nativeVars["nginx_effective_http_include_path"] = "/fixture/sites.conf"
	configs := map[string]string{
		"docker": renderNginxRootConfig("/fixture/sites.conf"),
		"native": renderRoleTemplate(t, "nginx/templates/nginx.conf.j2", nativeVars),
	}
	const key = "sk_live_realnginx"
	paths := map[string]string{
		"/webrtc/" + key:           "/webrtc/REDACTED",
		"/whip/" + key:             "/whip/REDACTED",
		"/ingest/" + key + "?p=1":  "/ingest/REDACTED?p=1",
		"/" + key + ".mkv":         "/REDACTED.mkv",
		"/view/" + key + ".mkv":    "/view/REDACTED.mkv",
		"/" + key + ".webm":        "/REDACTED.webm",
		"/" + key + "%2Emkv":       "/REDACTED%2Emkv",
		"/view/" + key + "%2Ewebm": "/view/REDACTED%2Ewebm",
		"/%77ebrtc/" + key:         "/%77ebrtc/REDACTED",
		"/webrtc%2F" + key:         "/webrtc%2FREDACTED",
		"/hls/playback/index.m3u8": "/hls/playback/index.m3u8",
	}
	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "nginx.conf")
			sites := filepath.Join(dir, "sites.conf")
			if err := os.WriteFile(root, []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sites, []byte("server { listen 8080; return 204; }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			container := fmt.Sprintf("fw-nginx-redact-%s-%d", name, time.Now().UnixNano())
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", container).Run() })
			if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", container,
				"-p", "127.0.0.1::8080/tcp",
				"-v", root+":/etc/nginx/nginx.conf:ro", "-v", sites+":/fixture/sites.conf:ro",
				image).CombinedOutput(); err != nil {
				t.Fatalf("docker run: %v\n%s", err, out)
			}
			out, err := exec.CommandContext(ctx, "docker", "port", container, "8080/tcp").CombinedOutput()
			if err != nil {
				t.Fatalf("docker port: %v\n%s", err, out)
			}
			addr := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			client := &http.Client{Timeout: 2 * time.Second}
			deadline := time.Now().Add(30 * time.Second)
			for {
				resp, getErr := client.Get("http://" + addr + "/healthz")
				if getErr == nil {
					_ = resp.Body.Close()
					break
				}
				if time.Now().After(deadline) {
					logs, _ := exec.Command("docker", "logs", container).CombinedOutput()
					t.Fatalf("nginx never answered: %v\n%s", getErr, logs)
				}
				time.Sleep(250 * time.Millisecond)
			}
			for path := range paths {
				resp, getErr := client.Get("http://" + addr + path)
				if getErr != nil {
					t.Fatalf("GET %s: %v", path, getErr)
				}
				_ = resp.Body.Close()
			}
			var logs string
			for deadline = time.Now().Add(10 * time.Second); ; {
				raw, _ := exec.Command("docker", "logs", container).CombinedOutput()
				logs = string(raw)
				missing := ""
				for _, want := range paths {
					if !strings.Contains(logs, `"GET `+want+` HTTP/1.1"`) {
						missing = want
					}
				}
				if missing == "" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("access log never recorded %s:\n%s", missing, logs)
				}
				time.Sleep(250 * time.Millisecond)
			}
			if strings.Contains(logs, key) {
				t.Fatalf("access log carries the stream key:\n%s", logs)
			}
		})
	}
}
