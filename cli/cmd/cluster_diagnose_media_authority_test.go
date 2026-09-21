package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestMediaAuthorityDiagnosticTargetsProbeEachDatabaseOnceInFlowOrder(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-us":    {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-us-2", "regional-us-1"}},
			"foghorn-eu":    {Enabled: true, Deploy: "foghorn", Hosts: []string{"regional-eu-1", "regional-eu-2"}},
			"commodore":     {Enabled: true, Hosts: []string{"central-eu-1"}},
			"purser":        {Enabled: true, Host: "central-eu-1"},
			"quartermaster": {Enabled: true, Host: "central-eu-1"},
			"bridge":        {Enabled: true, Host: "regional-eu-1"},
			"foghorn-old":   {Enabled: false, Deploy: "foghorn", Host: "regional-eu-3"},
		},
	}

	got := mediaAuthorityDiagnosticTargets(manifest)
	want := []mediaAuthorityDiagnosticTarget{
		{Service: "quartermaster", Deploy: "quartermaster", Host: "central-eu-1"},
		{Service: "purser", Deploy: "purser", Host: "central-eu-1"},
		{Service: "commodore", Deploy: "commodore", Host: "central-eu-1"},
		{Service: "foghorn-eu", Deploy: "foghorn", Host: "regional-eu-1"},
		{Service: "foghorn-us", Deploy: "foghorn", Host: "regional-us-1"},
	}
	if len(got) != len(want) {
		t.Fatalf("targets = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// Each Foghorn cell has its own database; a probe set that only knew the
// logical "foghorn" name would silently skip every cell's apply audit.
func TestMediaAuthorityDatabaseProbesCoverEveryCellDatabaseReadOnly(t *testing.T) {
	probes := mediaAuthorityDatabaseProbes([]string{"foghorn_us", "skipper", "commodore", "foghorn_eu", "purser", "quartermaster"}, 6)
	var order []string
	for _, probe := range probes {
		order = append(order, probe.Database)
		readOnly := strings.Index(probe.SQL, "SET default_transaction_read_only = on;")
		firstProbe := strings.Index(probe.SQL, `\echo ==`)
		if readOnly != 0 || firstProbe < 0 {
			t.Fatalf("%s: the read-only guard must open the session before any probe statement:\n%s", probe.Database, probe.SQL)
		}
		if !strings.Contains(probe.SQL, "6 hours") || strings.Contains(probe.SQL, mediaAuthorityDiagnosticWindowToken) {
			t.Fatalf("%s: window was not expanded into the probe SQL", probe.Database)
		}
	}
	if got, want := strings.Join(order, ","), "quartermaster,purser,commodore,foghorn_us,foghorn_eu"; got != want {
		t.Fatalf("probe order = %s, want %s (authority data flow; unrelated databases skipped)", got, want)
	}
	for _, probe := range probes {
		if strings.HasPrefix(probe.Database, "foghorn") && !strings.Contains(probe.SQL, "foghorn.media_authority_apply_audit") {
			t.Fatalf("%s: cell database is not probed with the Foghorn apply-audit SQL", probe.Database)
		}
	}
}

// The probe SQL carries single quotes, backslash escapes, and psql
// meta-commands. It reaches psql on stdin, so none of it may be re-parsed by
// the remote shell.
func TestMediaAuthorityDatabaseScriptFeedsSQLOnStdin(t *testing.T) {
	script := mediaAuthorityDatabaseScript(
		postgresSnapshotTarget{Name: "primary", HostName: "yuga-eu-1", Port: 5433, User: "yugabyte", Password: "p'w", Binary: "ysqlsh"},
		mediaAuthorityDatabaseProbes([]string{"commodore", "foghorn_eu"}, 24),
	)
	for _, want := range []string{
		"PORT=5433",
		`PASSWORD='p'\''w'`,
		"probe_db='commodore'",
		"probe_db='foghorn_eu'",
		`| run_probe "$probe_db"`,
		"-v ON_ERROR_STOP=1",
		"exit $FAILED",
		`-f - 2>&1`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("database script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, ` -c "`) {
		t.Fatal("probe SQL must not be passed as a shell-interpolated -c argument")
	}
}

func TestMediaAuthorityJournalScriptNormalizesRepeatedLines(t *testing.T) {
	script := mediaAuthorityJournalScript("commodore", diagnoseOptions{Since: "2 hours ago"})
	for _, want := range []string{"SVC='commodore'", "SINCE='2 hours ago'", `frameworks-${SVC}.service`, "uniq -c"} {
		if !strings.Contains(script, want) {
			t.Fatalf("journal script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "DATABASE_URL") || strings.Contains(script, "psql") {
		t.Fatal("service-host probes read journals only; SQL runs on the database host")
	}
}

func TestMediaAuthorityDiagnosticSQLContainsOnlyReadStatements(t *testing.T) {
	write := regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|alter|drop|create|grant|vacuum|analyze\s+[a-z])\b`)
	for _, deploy := range []string{"quartermaster", "purser", "commodore", "foghorn"} {
		sql := mediaAuthorityDiagnosticSQL(deploy)
		if strings.TrimSpace(sql) == "" {
			t.Fatalf("%s: no diagnostic SQL", deploy)
		}
		for _, line := range strings.Split(sql, "\n") {
			if strings.HasPrefix(line, `\echo`) {
				continue
			}
			if match := write.FindString(line); match != "" {
				t.Fatalf("%s: diagnostic SQL must stay read-only, found %q in %q", deploy, match, line)
			}
		}
	}
}

func TestMediaAuthorityDiagnosticUsesRecipientExpiryAndQueueClass(t *testing.T) {
	if !strings.Contains(commodoreMediaAuthorityDiagnosticSQL, "LEAST(version.valid_until, delivery.correction_until) AS recipient_valid_until") {
		t.Fatal("diagnostic hides the recipient's signed expiry")
	}
	if !strings.Contains(commodoreMediaAuthorityDiagnosticSQL, "AND queued.short_lease") ||
		strings.Contains(commodoreMediaAuthorityDiagnosticSQL, "version.valid_until <= version.issued_at") {
		t.Fatal("short-lease diagnostic does not use the delivery queue's classification")
	}
}

func TestDiagnoseMediaAuthorityRejectsOutOfRangeWindow(t *testing.T) {
	for _, hours := range []int{0, -1, mediaAuthorityDiagnosticMaxWindowHours + 1} {
		err := diagnoseMediaAuthority(t.Context(), newClusterDiagnoseCmd(), &resolvedCluster{Manifest: &inventory.Manifest{}}, nil, diagnoseOptions{WindowHours: hours})
		if err == nil || !strings.Contains(err.Error(), "--window-hours") {
			t.Fatalf("window %d: expected --window-hours error, got %v", hours, err)
		}
	}
}

// A probe that cannot connect, or whose statement fails, must fail the
// diagnosis, name the database, and leave the other probes to run. The script
// is run for real, against a stand-in for the SQL client.
func TestMediaAuthorityDatabaseScriptReportsEveryFailedProbe(t *testing.T) {
	dir := t.TempDir()
	client := filepath.Join(dir, "fake-sql")
	// Exits like psql with ON_ERROR_STOP: 3 for a failed statement, 2 for a
	// connection that could not be made; prints the rows otherwise.
	stub := `#!/bin/sh
db=""
stop=0
while [ $# -gt 0 ]; do
  case "$1" in
    -d) db="$2"; shift ;;
    -v) [ "$2" = "ON_ERROR_STOP=1" ] && stop=1; shift ;;
  esac
  shift
done
cat >/dev/null
case "$db" in
  broken) echo 'ERROR:  relation "missing" does not exist'; [ "$stop" = 1 ] && exit 3; exit 0 ;;
  refused) echo 'connection refused'; exit 2 ;;
  *) echo "rows from $db" ;;
esac
`
	if err := os.WriteFile(client, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	script := mediaAuthorityDatabaseScript(postgresSnapshotTarget{Port: 5432, User: "u", Password: "p", Binary: client}, []mediaAuthorityDatabaseProbe{
		{Database: "healthy", SQL: "SELECT 1;"},
		{Database: "broken", SQL: "SELECT * FROM missing;"},
		{Database: "refused", SQL: "SELECT 1;"},
		{Database: "also_healthy", SQL: "SELECT 1;"},
	})
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
		t.Fatalf("script exit = %v, want non-zero; output:\n%s", err, out)
	}
	for _, want := range []string{"rows from healthy", "rows from also_healthy", "probe of database broken failed", "probe of database refused failed"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(string(out), "probe of database healthy failed") {
		t.Fatalf("a healthy probe was reported failed:\n%s", out)
	}
}
