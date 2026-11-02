package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func TestBackupSelectionFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "nothing selected", args: nil, want: "choose what to cover"},
		{name: "all plus database", args: []string{"--all", "--database", "purser"}, want: "--all already covers"},
		{name: "all plus clickhouse", args: []string{"--all", "--clickhouse"}, want: "--all already covers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newClusterBackupCreateCmd()
			if err := cmd.ParseFlags(append([]string{"--to", t.TempDir()}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			err := cmd.RunE(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
		})
	}

	cmd := newClusterBackupCreateCmd()
	if err := cmd.ParseFlags([]string{"--all"}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("err = %v; --to must be required", err)
	}

	restore := newClusterRestoreCmd()
	if err := restore.ParseFlags([]string{"--database", "purser"}); err != nil {
		t.Fatal(err)
	}
	if err := restore.RunE(restore, nil); err == nil || !strings.Contains(err.Error(), "--from is required") {
		t.Fatalf("err = %v; restore must require --from", err)
	}
	for _, sub := range []string{"finish", "rollback"} {
		c, _, err := newClusterRestoreCmd().Find([]string{sub})
		if err != nil || c.Name() != sub {
			t.Fatalf("restore %s: %v", sub, err)
		}
	}
}

func TestBackupSelectionMatchesPhysicalAndInstanceNames(t *testing.T) {
	sel := backupSelection{databases: []string{"foghorn_eu", "support/chatwoot", "postgres/purser"}}
	for _, tc := range []struct {
		instance, name string
		want           bool
	}{
		{"", "foghorn_eu", true},
		{"", "foghorn_us", false},
		{"", "purser", true},
		{"support", "chatwoot", true},
		{"support", "listmonk", false},
		{"", "chatwoot", false},
	} {
		if got := sel.includes(tc.instance, tc.name); got != tc.want {
			t.Errorf("includes(%q, %q) = %v, want %v", tc.instance, tc.name, got, tc.want)
		}
	}
	if !(backupSelection{all: true}).includes("support", "metabase") {
		t.Error("--all must include instance databases")
	}
}

type recordingControl struct {
	calls   []string
	failFor string
}

func (r *recordingControl) Stop(_ context.Context, svc string) error {
	r.calls = append(r.calls, "stop "+svc)
	if svc == r.failFor {
		return errors.New("role stop failed")
	}
	return nil
}

func (r *recordingControl) Start(ctx context.Context, svc string) error {
	r.calls = append(r.calls, "start "+svc)
	return ctx.Err()
}

func TestRestoreCancellationStillRestartsServices(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	control := &recordingControl{}
	err := stopSwapStart(ctx, io.Discard, control, []string{"purser"}, func() error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "start purser") {
		t.Fatalf("cancellation prevented recovery: %v", err)
	}
	if got := strings.Join(control.calls, ","); got != "stop purser,start purser" {
		t.Fatalf("calls = %s", got)
	}
}

func TestStopSwapStartAlwaysStartsWhatItStopped(t *testing.T) {
	control := &recordingControl{}
	err := stopSwapStart(context.Background(), io.Discard, control, []string{"purser", "commodore"}, func() error {
		control.calls = append(control.calls, "swap")
		return errors.New("rename failed")
	})
	if err == nil || !strings.Contains(err.Error(), "rename failed") {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(control.calls, ","); got != "stop purser,stop commodore,swap,start purser,start commodore" {
		t.Fatalf("calls = %s; a failed swap must still start every stopped service", got)
	}

	control = &recordingControl{failFor: "commodore"}
	swapped := false
	err = stopSwapStart(context.Background(), io.Discard, control, []string{"purser", "commodore", "foghorn"}, func() error {
		swapped = true
		return nil
	})
	if err == nil || swapped {
		t.Fatalf("err = %v swapped = %v; a failed stop must not swap", err, swapped)
	}
	if got := strings.Join(control.calls, ","); got != "stop purser,stop commodore,start purser,start commodore" {
		t.Fatalf("calls = %s; a partially stopped service must also be started again", got)
	}
}

func restoreServicesFor(manifest *inventory.Manifest, name, source, instance string) []string {
	return restoreServices(manifest, []backup.Database{{Name: name, Source: source, Instance: instance}}, false)
}

func TestRestoreServicesFollowDatabaseOwnership(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"purser":           {Enabled: true},
			"foghorn-eu":       {Enabled: true, Deploy: "foghorn", Cluster: "media-eu-1"},
			"foghorn-us":       {Enabled: true, Deploy: "foghorn", Cluster: "media-us-1"},
			"periscope-ingest": {Enabled: true},
			"commodore":        {Enabled: true},
		},
		Interfaces: map[string]inventory.ServiceConfig{"chatwoot": {Enabled: true}},
	}
	got := restoreServices(manifest, nil, false)
	if len(got) != 0 {
		t.Fatalf("no database selected, services = %v", got)
	}
	got = restoreServicesFor(manifest, "purser", "", "")
	if strings.Join(got, ",") != "purser" {
		t.Fatalf("purser: %v", got)
	}
	got = restoreServicesFor(manifest, "foghorn_eu", "foghorn", "")
	if strings.Join(got, ",") != "foghorn-eu" {
		t.Fatalf("foghorn_eu: %v; only the cell that owns the database stops", got)
	}
	got = restoreServicesFor(manifest, "chatwoot", "", "support")
	if strings.Join(got, ",") != "chatwoot" {
		t.Fatalf("support/chatwoot: %v", got)
	}
	got = restoreServices(manifest, nil, true)
	if strings.Join(got, ",") != "periscope-ingest" {
		t.Fatalf("clickhouse: %v", got)
	}
}

func TestBackupCreateAllSkipsDatabasesThePendingReleaseCreates(t *testing.T) {
	m := &inventory.Manifest{
		Profile: "dev",
		Hosts:   map[string]inventory.Host{"db": {Name: "db", ExternalIP: "192.0.2.1"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "db", ID: 1}},
				Databases: []inventory.DatabaseConfig{{Name: "purser", Owner: "purser"}, {Name: "network_observer", Owner: "network_observer"}}},
		},
	}
	oldHost, oldExisting := backupPostgresHostFn, existingPostgresDatabasesFn
	t.Cleanup(func() { backupPostgresHostFn, existingPostgresDatabasesFn = oldHost, oldExisting })
	backupPostgresHostFn = func(_ context.Context, manifest *inventory.Manifest, _ *inventory.PostgresConfig, _ *ssh.Pool) (inventory.Host, error) {
		return manifest.Hosts["db"], nil
	}
	existingPostgresDatabasesFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ *inventory.PostgresConfig) (map[string]struct{}, error) {
		return map[string]struct{}{"purser": {}, "yugabyte": {}}, nil
	}

	plan, err := planBackupTargets(context.Background(), &resolvedCluster{Manifest: m}, nil, backupSelection{all: true, skipAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.databases) != 1 || plan.databases[0].entry.Name != "purser" {
		t.Fatalf("backup create --all covers %+v, want only purser", plan.databases)
	}
	if len(plan.skipped) != 1 || plan.skipped[0] != "postgres/network_observer" {
		t.Fatalf("skipped = %v, want postgres/network_observer", plan.skipped)
	}

	explicit, err := planBackupTargets(context.Background(), &resolvedCluster{Manifest: m}, nil, backupSelection{databases: []string{"network_observer"}, skipAbsent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(explicit.databases) != 1 || len(explicit.skipped) != 0 {
		t.Fatalf("an explicitly named database must stay selected so its dump reports the error: %+v", explicit)
	}

	existingPostgresDatabasesFn = func(context.Context, *ssh.Pool, inventory.Host, *inventory.PostgresConfig) (map[string]struct{}, error) {
		t.Fatal("restore planning must not probe database existence")
		return nil, nil
	}
	restore, err := planBackupTargets(context.Background(), &resolvedCluster{Manifest: m}, nil, backupSelection{all: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(restore.databases) != 2 {
		t.Fatalf("restore --all covers %d databases, want 2", len(restore.databases))
	}
}
