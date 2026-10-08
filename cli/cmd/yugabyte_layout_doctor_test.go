package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/health"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
)

func TestSummarizeYugabyteLayoutWarnsOnDrift(t *testing.T) {
	matching := yugabyteLayoutReport{Database: "quartermaster", Declared: "colocated", Observed: "colocated", Tablets: 7, Peers: 21}
	drifted := yugabyteLayoutReport{
		Database: "foghorn_eu", Declared: "colocated", Observed: "distributed", Tablets: 120, Peers: 340,
		Drift:            []string{"database is distributed, layout declares colocated"},
		UnreachableNodes: []string{"yuga-eu-3"},
	}

	result := summarizeYugabyteLayout([]yugabyteLayoutReport{drifted, matching})
	if result.OK || result.Status != "degraded" {
		t.Fatalf("drift with incomplete tablet evidence = ok %v status %q, want degraded", result.OK, result.Status)
	}
	if !strings.Contains(result.Error, "foghorn_eu: database is distributed, layout declares colocated") {
		t.Fatalf("drift error = %q", result.Error)
	}
	onlyDrift := drifted
	onlyDrift.UnreachableNodes = nil
	warning := summarizeYugabyteLayout([]yugabyteLayoutReport{onlyDrift, matching})
	if warning.OK || warning.Status != yugabyteLayoutWarning || !strings.Contains(warning.Error, "until they are relaid out") {
		t.Fatalf("drift alone = %+v, want a warning, not a failure", warning)
	}
	if step := doctorServiceRemediation("Yugabyte layout"); !strings.Contains(step.Cmd, "relayout plan") {
		t.Fatalf("layout remediation = %+v, want relayout plan", step)
	}
	if got := result.Metadata["quartermaster"]; got != "layout=colocated observed=colocated tablets=7 peers=21 drift=0" {
		t.Fatalf("quartermaster metadata = %q", got)
	}
	if got := result.Metadata["foghorn_eu"]; !strings.HasSuffix(got, "tablet_counts_exclude=yuga-eu-3") {
		t.Fatalf("foghorn_eu metadata = %q, want unreachable nodes noted", got)
	}

	clean := summarizeYugabyteLayout([]yugabyteLayoutReport{matching})
	if !clean.OK || clean.Status != "healthy" || clean.Message != "1 database(s) match their declared layout" {
		t.Fatalf("matching layout result = %+v", clean)
	}

	partial := matching
	partial.UnreachableNodes = []string{"yuga-eu-2"}
	incomplete := summarizeYugabyteLayout([]yugabyteLayoutReport{partial})
	if incomplete.OK || incomplete.Status != "degraded" || !strings.Contains(incomplete.Error, "tablet evidence is incomplete (quartermaster: tablets not read from yuga-eu-2)") {
		t.Fatalf("matching layout with an unreachable tserver = %+v, want degraded incomplete evidence", incomplete)
	}
}

func TestSummarizeYugabyteLayoutTruncatesLongDrift(t *testing.T) {
	report := yugabyteLayoutReport{Database: "purser", Declared: "colocated", Observed: "colocated", Drift: []string{"a", "b", "c", "d", "e"}}
	result := summarizeYugabyteLayout([]yugabyteLayoutReport{report})
	if !strings.Contains(result.Error, "purser: a; b; c; 2 more") {
		t.Fatalf("drift error = %q, want the first three entries and a remainder count", result.Error)
	}
}

func TestSummarizeYugabyteLayoutKeepsOtherDatabasesWhenOneIsMissingOrUnreadable(t *testing.T) {
	matching := yugabyteLayoutReport{Database: "quartermaster", Declared: "colocated", Observed: "colocated", Tablets: 7, Peers: 21}
	missing := yugabyteLayoutReport{Database: "lookout", Missing: true}
	result := summarizeYugabyteLayout([]yugabyteLayoutReport{matching, missing})
	if !result.OK || result.Metadata["lookout"] != "not created yet" || !strings.Contains(result.Message, "not created yet: lookout") {
		t.Fatalf("missing database result = %+v, want a healthy check that names it", result)
	}
	failed := yugabyteLayoutReport{Database: "purser", Err: "read relation placement: timeout"}
	result = summarizeYugabyteLayout([]yugabyteLayoutReport{matching, failed})
	if result.OK || result.Metadata["quartermaster"] == "" || !strings.Contains(result.Error, "could not inspect purser") {
		t.Fatalf("unreadable database result = %+v, want the others reported and the failure named", result)
	}
}

// fakeYsqlsh answers the layout doctor's queries the way a tserver's ysqlsh would. Every database has one tablet
// replicated on all tservers plus one tablet local to the answering tserver.
const fakeYsqlsh = `#!/bin/sh
db=""; file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -d) db="$2"; shift 2 ;;
    -f) file="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [ -z "$file" ] || [ "$file" = "-" ]; then sql="$(cat)"; else sql="$(cat "$file")"; fi
if [ -n "$FAKE_BROKEN_DATABASE" ] && [ "$db" = "$FAKE_BROKEN_DATABASE" ]; then
  echo "FATAL:  database \"$db\" is not accepting connections" >&2
  exit 2
fi
case "$sql" in
  *yb_local_tablets*current_database*) echo "$db-shared"; echo "$db-$FAKE_TSERVER" ;;
  *yb_local_tablets*) for d in $FAKE_DATABASES; do echo "$d|$d-shared"; echo "$d|$d-$FAKE_TSERVER"; done; echo "system|system-$FAKE_TSERVER" ;;
  *pg_database*) echo yugabyte; for d in $FAKE_DATABASES; do echo "$d"; done ;;
  *yb_is_database_colocated*) echo t ;;
  *yb_table_properties*) : ;;
  *"version()"*) echo "PostgreSQL 15.12-YB-2025.2.0.0 YugabyteDB" ;;
  *"SELECT 1"*) echo 1 ;;
  *) echo "unexpected sql: $sql" >&2; exit 3 ;;
esac
`

// countingTserverRunner executes remote commands locally against fakeYsqlsh and counts every SSH round trip. An
// upload is counted once although Client.Upload spends three ssh/scp processes on it.
type countingTserverRunner struct {
	host      string
	bin       string
	databases string
	broken    string
	mu        *sync.Mutex
	runs      map[string]int
	uploads   map[string]int
}

func (r *countingTserverRunner) Run(ctx context.Context, command string) (*fwssh.CommandResult, error) {
	r.mu.Lock()
	r.runs[r.host]++
	r.mu.Unlock()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Env = append(os.Environ(),
		"PATH="+r.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_TSERVER="+r.host, "FAKE_DATABASES="+r.databases, "FAKE_BROKEN_DATABASE="+r.broken)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return fwssh.CompleteRun(&fwssh.CommandResult{Command: command}, r.host, command, []byte(stdout.String()), []byte(stderr.String()), err)
}

func (r *countingTserverRunner) RunScript(ctx context.Context, script string) (*fwssh.CommandResult, error) {
	return r.Run(ctx, script)
}

func (r *countingTserverRunner) Upload(_ context.Context, opts fwssh.UploadOptions) error {
	r.mu.Lock()
	r.uploads[r.host]++
	r.mu.Unlock()
	content, err := os.ReadFile(opts.LocalPath)
	if err != nil {
		return err
	}
	return os.WriteFile(opts.RemotePath, content, 0o600)
}

func (r *countingTserverRunner) Close() error { return nil }

type layoutDoctorFixture struct {
	names     []string
	databases []provisioner.SchemaDatabase
	pg        *inventory.PostgresConfig
	hosts     []inventory.Host
	runs      map[string]int
	uploads   map[string]int
	runnerFor yugabyteHostRunner
}

// newLayoutDoctorFixture builds a 3-tserver cluster serving the first ten catalog databases that declare a layout.
func newLayoutDoctorFixture(t *testing.T, broken string) *layoutDoctorFixture {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ysqlsh"), []byte(fakeYsqlsh), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &layoutDoctorFixture{
		pg:      &inventory.PostgresConfig{Engine: "yugabyte", Port: 5433},
		hosts:   []inventory.Host{{Name: "yuga-1"}, {Name: "yuga-2"}, {Name: "yuga-3"}},
		runs:    map[string]int{},
		uploads: map[string]int{},
	}
	// Cluster-scoped databases such as foghorn_<cluster> follow their logical database's layout, which is how a
	// production manifest reaches ten physical databases.
	for _, name := range releases.ServiceDatabaseNames() {
		if layout, err := provisioner.YugabyteLayoutForDatabase(name); err == nil && layout != nil {
			f.databases = append(f.databases, provisioner.SchemaDatabase{Name: name})
		}
	}
	for i := 0; len(f.databases) < 10; i++ {
		source := f.databases[i].Name
		f.databases = append(f.databases, provisioner.SchemaDatabase{Name: fmt.Sprintf("%s_cell%d", source, i), SourceName: source})
	}
	f.databases = f.databases[:10]
	for _, database := range f.databases {
		f.names = append(f.names, database.Name)
	}
	var mu sync.Mutex
	f.runnerFor = func(host inventory.Host) (fwssh.Runner, error) {
		return &countingTserverRunner{host: host.Name, bin: bin, databases: strings.Join(f.names, " "), broken: broken, mu: &mu, runs: f.runs, uploads: f.uploads}, nil
	}
	return f
}

func (f *layoutDoctorFixture) inspect() *health.CheckResult {
	return inspectYugabyteLayouts(context.Background(), f.runnerFor, f.hosts, f.pg, f.databases)
}

// TestYugabyteLayoutDoctorReadsEachTserverOnce pins the SSH cost of the layout check on a 10-database, 3-tserver
// cluster: the primary answers the database list and every database's placement, and each tserver is asked for its
// tablets once for all databases.
func TestYugabyteLayoutDoctorReadsEachTserverOnce(t *testing.T) {
	f := newLayoutDoctorFixture(t, "")
	result := f.inspect()
	if result.Status == "unhealthy" || result.Status == "degraded" {
		t.Fatalf("layout result = %+v", result)
	}
	for _, name := range f.names {
		if got := result.Metadata[name]; !strings.Contains(got, "observed=colocated tablets=4 peers=6") {
			t.Fatalf("%s metadata = %q, want 4 distinct tablets and 6 peers over 3 tservers", name, got)
		}
	}

	calls := 0
	for _, host := range f.hosts {
		calls += f.runs[host.Name] + f.uploads[host.Name]
	}
	t.Logf("ssh round trips: runs=%v uploads=%v total=%d", f.runs, f.uploads, calls)
	// Primary: the database list and one placement read; every tserver, the primary included: one tablet read.
	const want = 2 + 3
	if calls > want {
		t.Fatalf("layout check made %d SSH round trips (runs %v, uploads %v), want at most %d", calls, f.runs, f.uploads, want)
	}
}

// TestYugabyteLayoutDoctorIsolatesOneUnreadableDatabase proves a database whose ysqlsh fails is reported on its own
// while the other databases sharing the same SSH command are still inspected.
func TestYugabyteLayoutDoctorIsolatesOneUnreadableDatabase(t *testing.T) {
	broken := newLayoutDoctorFixture(t, "").names[3]
	f := newLayoutDoctorFixture(t, broken)
	result := f.inspect()
	if result.Status != "degraded" || !strings.Contains(result.Error, "could not inspect "+broken+": read database colocation: ysqlsh -d "+broken+" exited 2: FATAL:") {
		t.Fatalf("layout result = %+v, want %s named as unreadable with its ysqlsh error", result, broken)
	}
	for _, name := range f.names {
		if name == broken {
			continue
		}
		if got := result.Metadata[name]; !strings.Contains(got, "tablets=4 peers=6") {
			t.Fatalf("%s metadata = %q, want it inspected despite %s failing", name, got, broken)
		}
	}
}
