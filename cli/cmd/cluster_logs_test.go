package cmd

import (
	"strings"
	"testing"
)

func TestLogsSnapshotScriptIncludesPrivateerAndRedisDiagnostics(t *testing.T) {
	script := logsSnapshotScript(logsSnapshotOptions{Since: "1 hour ago", Tail: 100})
	for _, want := range []string{
		"== privateer sync diagnostics ==",
		"EnvironmentFiles",
		"SYNC_TIMEOUT",
		"SyncMesh|sync infrastructure",
		"http://127.0.0.1:18012/health",
		"== telemetry remote write diagnostics ==",
		"/etc/vmauth/config.yml",
		"VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD",
		"vmagent_remotewrite",
		"== redis sentinel diagnostics ==",
		"frameworks-redis-*sentinel*.service",
		"redis-cli -p \"$port\" SENTINEL masters",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("snapshot script missing %q", want)
		}
	}
}
