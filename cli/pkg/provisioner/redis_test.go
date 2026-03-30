package provisioner

import (
	"strings"
	"testing"
)

func TestResolveRedisEngine(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]interface{}
		want     string
		wantErr  bool
	}{
		{name: "default", metadata: map[string]interface{}{}, want: "valkey"},
		{name: "explicit valkey", metadata: map[string]interface{}{"engine": "valkey"}, want: "valkey"},
		{name: "explicit redis", metadata: map[string]interface{}{"engine": "redis"}, want: "redis"},
		{name: "case insensitive", metadata: map[string]interface{}{"engine": " Redis "}, want: "redis"},
		{name: "invalid", metadata: map[string]interface{}{"engine": "dragonfly"}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRedisEngine(tc.metadata)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRedisEngine returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestBuildRedisDockerImage(t *testing.T) {
	tests := []struct {
		name        string
		engine      string
		version     string
		wantImage   string
		wantVersion string
		wantErr     bool
	}{
		{
			name:        "valkey default version",
			engine:      "valkey",
			wantImage:   "valkey/valkey:8.1-alpine",
			wantVersion: "8.1",
		},
		{
			name:        "valkey explicit version",
			engine:      "valkey",
			version:     "8.2",
			wantImage:   "valkey/valkey:8.2-alpine",
			wantVersion: "8.2",
		},
		{
			name:        "redis default version",
			engine:      "redis",
			wantImage:   "redis:7.2.4-alpine",
			wantVersion: "7.2.4",
		},
		{
			name:        "redis explicit version",
			engine:      "redis",
			version:     "8.0",
			wantImage:   "redis:8.0-alpine",
			wantVersion: "8.0",
		},
		{
			name:    "invalid engine",
			engine:  "dragonfly",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			image, version, err := buildRedisDockerImage(tc.engine, tc.version)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildRedisDockerImage returned error: %v", err)
			}
			if image != tc.wantImage {
				t.Fatalf("expected image %q, got %q", tc.wantImage, image)
			}
			if version != tc.wantVersion {
				t.Fatalf("expected version %q, got %q", tc.wantVersion, version)
			}
		})
	}
}

func TestBuildRedisCommandArgs(t *testing.T) {
	tests := []struct {
		name     string
		engine   string
		password string
		metadata map[string]interface{}
		want     string
	}{
		{
			name:   "redis",
			engine: "redis",
			want:   "redis-server --appendonly yes",
		},
		{
			name:   "valkey",
			engine: "valkey",
			want:   "valkey-server --appendonly yes",
		},
		{
			name:     "with password and directives",
			engine:   "valkey",
			password: "secret",
			metadata: map[string]interface{}{"redis_maxmemory": "256mb"},
			want:     "valkey-server --appendonly yes --requirepass secret --maxmemory 256mb",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := buildRedisCommandArgs(tc.engine, tc.metadata, tc.password)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestBuildRedisNativeSpec(t *testing.T) {
	redisSpec := buildRedisNativeSpec("redis", "cache")
	if redisSpec.serviceUser != "redis" || redisSpec.serverBinary != "/usr/bin/redis-server" {
		t.Fatalf("unexpected redis spec: %+v", redisSpec)
	}

	valkeySpec := buildRedisNativeSpec("valkey", "cache")
	if valkeySpec.serviceUser != "valkey" {
		t.Fatalf("expected valkey user, got %+v", valkeySpec)
	}
	if valkeySpec.serverBinary != "/usr/bin/valkey-server" {
		t.Fatalf("expected valkey server binary, got %+v", valkeySpec)
	}
	if valkeySpec.configPath != "/etc/valkey/valkey-cache.conf" {
		t.Fatalf("unexpected valkey config path: %+v", valkeySpec)
	}
}

func TestGenerateRedisPlaybook_ValkeyNative(t *testing.T) {
	playbook := GenerateRedisPlaybook("host-1", "valkey", "cache", 6379, "", nil)
	if len(playbook.Plays) != 1 {
		t.Fatalf("expected 1 play, got %d", len(playbook.Plays))
	}

	tasks := playbook.Plays[0].Tasks
	if len(tasks) < 5 {
		t.Fatalf("expected tasks to be generated, got %d", len(tasks))
	}

	installArgs := tasks[0].Args["name"]
	pkgs, ok := installArgs.([]string)
	if !ok {
		t.Fatalf("expected package list, got %#v", installArgs)
	}
	if len(pkgs) != 2 || pkgs[0] != "valkey" || pkgs[1] != "valkey-redis-compat" {
		t.Fatalf("unexpected valkey package list: %#v", pkgs)
	}

	unitContent, ok := tasks[4].Args["content"].(string)
	if !ok {
		t.Fatalf("expected systemd unit content, got %#v", tasks[4].Args["content"])
	}
	if unitContent == "" || !containsAll(unitContent, []string{"User=valkey", "ExecStart=/usr/bin/valkey-server /etc/valkey/valkey-cache.conf"}) {
		t.Fatalf("unexpected valkey unit content: %s", unitContent)
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
