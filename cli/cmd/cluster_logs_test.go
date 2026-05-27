package cmd

import (
	"strings"
	"testing"
)

func TestLogsSnapshotScriptIncludesPrivateerAndRedisDiagnostics(t *testing.T) {
	script := logsSnapshotScript(logsSnapshotOptions{Since: "1 hour ago", Tail: 100})
	for _, want := range []string{
		"== privateer sync diagnostics ==",
		"SYNC_TIMEOUT",
		"SyncMesh|sync infrastructure",
		"== redis sentinel diagnostics ==",
		"frameworks-redis-*sentinel*.service",
		"redis-cli -p \"$port\" SENTINEL masters",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("snapshot script missing %q", want)
		}
	}
}
