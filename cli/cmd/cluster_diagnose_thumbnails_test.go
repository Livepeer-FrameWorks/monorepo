package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestThumbnailRecoveryDiagnosticsBoundEveryCellPhaseAndKeepSQLReadOnly(t *testing.T) {
	probes := thumbnailRecoveryDatabaseProbes([]string{"foghorn_us", "purser", "foghorn_eu", "foghornish"})
	if len(probes) != 8 {
		t.Fatalf("want four probes per Foghorn cell, got %d", len(probes))
	}
	write := regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|alter|drop|create|grant|vacuum)\b`)
	for _, probe := range probes {
		if !strings.HasPrefix(probe.SQL, "SET default_transaction_read_only = on;\nSET statement_timeout = '5s';") {
			t.Fatal("probe lacks read-only/deadline guards")
		}
		if match := write.FindString(probe.SQL); match != "" {
			t.Fatalf("diagnostic can write: %s", match)
		}
	}
	if strings.Count(thumbnailSupersededPlanSQL, "LIMIT 18") != 1 || !strings.Contains(thumbnailSupersededPlanSQL, "ORDER BY a.superseded_at") {
		t.Fatal("diagnostic does not measure the actual bounded GC selection")
	}
}

func TestThumbnailDiagnosticContinuesAfterOnePhaseTimesOut(t *testing.T) {
	client := filepath.Join(t.TempDir(), "fake-sql")
	body := `#!/bin/sh
sql=$(cat)
case "$sql" in
 *'publishing recovery plan'*) echo 'ERROR: canceling statement due to statement timeout'; exit 3 ;;
 *'expiry recovery plan'*) echo 'expiry plan complete' ;;
 *'superseded-version recovery plan'*) echo 'superseded plan complete' ;;
 *) echo 'metadata complete' ;;
esac
`
	if err := os.WriteFile(client, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	script := mediaAuthorityDatabaseScript(postgresSnapshotTarget{Binary: client, User: "fixture", Port: 5432}, thumbnailRecoveryDatabaseProbes([]string{"foghorn_us", "foghorn_eu"}))
	output, err := exec.CommandContext(t.Context(), "sh", "-c", script).CombinedOutput()
	if err == nil {
		t.Fatalf("timed-out phase was acknowledged as success: %s", output)
	}
	for _, marker := range []string{"statement timeout", "metadata complete", "expiry plan complete", "superseded plan complete", "failed (exit 3)"} {
		if strings.Count(string(output), marker) != 2 {
			t.Fatalf("failed phase prevented the other phases or cell from being checked: want two %q: %s", marker, output)
		}
	}
}
