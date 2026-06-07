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
		"== durable trigger WAL diagnostics ==",
		"http://127.0.0.1:18007/triggers/wal",
		"trigger ack failure context",
		"== telemetry remote write diagnostics ==",
		"/etc/vmauth/config.yml",
		"VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD",
		"vmagent_remotewrite",
		"backend failure context",
		"all the [0-9]+ backends",
		"== redis sentinel diagnostics ==",
		"frameworks-redis-*sentinel*.service",
		"redis-cli -p \"$port\" SENTINEL masters",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("snapshot script missing %q", want)
		}
	}
}
