package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/inventory"
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
