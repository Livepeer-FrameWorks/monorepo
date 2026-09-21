package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

func TestRelayoutStepResultNeverReportsSuccessAfterLeaseLoss(t *testing.T) {
	lost := errors.New("relayout lease for purser lost: the lease expired or another owner holds it")
	held, release := context.WithCancelCause(context.Background())
	release(lost)
	if err := relayoutStepResult(nil, held); !errors.Is(err, lost) {
		t.Fatalf("successful step after lease loss = %v, want the lease error", err)
	}
	stepErr := errors.New("restore failed")
	if err := relayoutStepResult(stepErr, held); !errors.Is(err, stepErr) || !errors.Is(err, lost) {
		t.Fatalf("failed step after lease loss = %v, want both errors", err)
	}

	stopped, stop := context.WithCancelCause(context.Background())
	stop(nil)
	if err := relayoutStepResult(nil, stopped); err != nil {
		t.Fatalf("step whose hold was stopped normally = %v, want success", err)
	}
	if err := relayoutStepResult(nil, context.Background()); err != nil {
		t.Fatalf("step under a live lease = %v, want success", err)
	}
}

func TestRelayoutConsumersMapCellsAndSharedDatabases(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":         {Enabled: true, Deploy: "foghorn", Cluster: "media-eu-1", Hosts: []string{"edge-eu-1", "edge-eu-2"}},
			"foghorn-us":         {Enabled: true, Deploy: "foghorn", Cluster: "media-us-1", Host: "edge-us-1"},
			"periscope-query":    {Enabled: true, Host: "control-1"},
			"periscope-ingest":   {Enabled: true, Host: "control-2"},
			"periscope-metering": {Enabled: false, Host: "control-3"},
			"quartermaster":      {Enabled: true, Cluster: "control-1", Host: "control-1"},
			"bridge":             {Enabled: true, Host: "control-1"},
		},
	}
	ids := func(consumers []relayoutConsumer) []string {
		out := make([]string, 0, len(consumers))
		for _, consumer := range consumers {
			out = append(out, consumer.ServiceID)
		}
		return out
	}

	eu := relayoutConsumers(manifest, "foghorn_eu")
	if !slices.Equal(ids(eu), []string{"foghorn-eu"}) || eu[0].Deploy != "foghorn" || !slices.Equal(eu[0].HostNames, []string{"edge-eu-1", "edge-eu-2"}) {
		t.Fatalf("foghorn_eu consumers = %+v", eu)
	}
	if got := relayoutConsumers(manifest, "foghorn"); len(got) != 0 {
		t.Fatalf("logical foghorn database has consumers %+v, but every Foghorn cell owns its own database", got)
	}
	if got := ids(relayoutConsumers(manifest, "periscope")); !slices.Equal(got, []string{"periscope-ingest", "periscope-query"}) {
		t.Fatalf("periscope consumers = %v, want the enabled Periscope services", got)
	}
	if got := ids(relayoutConsumers(manifest, "quartermaster")); !slices.Equal(got, []string{"quartermaster"}) {
		t.Fatalf("quartermaster consumers = %v", got)
	}
}

func TestRelayoutDryRunStepsNameEveryDestructiveAction(t *testing.T) {
	for step, wants := range map[string][]string{
		"preflight": {"drop quartermaster__relayout_check and the preflight dump"},
		"prepare":   {"drop any previous quartermaster__relayout, create it colocated"},
		"cutover": {
			"REVOKE ALL ON DATABASE quartermaster FROM PUBLIC",
			"session_replication_role=replica",
			"ALTER DATABASE quartermaster RENAME TO quartermaster__pre_relayout",
			"ALTER DATABASE quartermaster__relayout RENAME TO quartermaster",
		},
		"rollback": {"DROP DATABASE quartermaster__relayout"},
		"finish":   {"DROP DATABASE quartermaster__pre_relayout"},
	} {
		steps := strings.Join(relayoutDryRunSteps(step, "quartermaster", relayoutDefaultDumpDir), "\n")
		for _, want := range wants {
			if !strings.Contains(steps, want) {
				t.Errorf("%s dry run does not mention %q:\n%s", step, want, steps)
			}
		}
	}
	for _, removed := range []string{"fence", "verify", "unknown"} {
		if steps := relayoutDryRunSteps(removed, "quartermaster", relayoutDefaultDumpDir); steps != nil {
			t.Fatalf("%s step rendered %v", removed, steps)
		}
	}
}

func TestRelayoutBytesFormatting(t *testing.T) {
	for bytes, want := range map[int64]string{512: "512 B", 2048: "2.0 KiB", 5 * 1024 * 1024 * 1024: "5.0 GiB"} {
		if got := relayoutBytes(bytes); got != want {
			t.Errorf("relayoutBytes(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func TestRelayoutOwnerIsDeadLocalProcess(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no host name")
	}
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}
	cases := []struct {
		owner string
		dead  bool
	}{
		{fmt.Sprintf("%s:%d:abcd", host, child.Process.Pid), true},
		{fmt.Sprintf("%s:%d:abcd", host, os.Getpid()), false},
		{fmt.Sprintf("%s:%d:abcd", host, os.Getppid()), false},
		{fmt.Sprintf("other-host:%d:abcd", child.Process.Pid), false},
		{"not-an-owner", false},
		{fmt.Sprintf("%s:zero:abcd", host), false},
	}
	for _, tc := range cases {
		if got := relayoutOwnerIsDeadLocalProcess(tc.owner); got != tc.dead {
			t.Errorf("relayoutOwnerIsDeadLocalProcess(%q) = %v, want %v", tc.owner, got, tc.dead)
		}
	}
}

// TestRelayoutCleanupSuccessorIsRecognizedWhenItDies covers a cleanup that takes the lease over as a successor and
// then dies before releasing it. The successor is minted like any owner, so once its process is gone the next
// invocation on this host recognizes it as dead and takes over at once instead of waiting for the lease to expire.
func TestRelayoutCleanupSuccessorIsRecognizedWhenItDies(t *testing.T) {
	successor, err := relayoutLeaseOwner()
	if err != nil {
		t.Fatalf("mint successor: %v", err)
	}
	parts := strings.Split(successor, ":")
	if len(parts) != 3 || parts[1] != strconv.Itoa(os.Getpid()) {
		t.Fatalf("successor %q is not host:pid:nonce for this process", successor)
	}
	if relayoutOwnerIsDeadLocalProcess(successor) {
		t.Fatal("a successor of this live process reads as dead")
	}
	child := exec.Command("true")
	if err = child.Run(); err != nil {
		t.Fatalf("run child: %v", err)
	}
	crashed := strings.Join([]string{parts[0], strconv.Itoa(child.Process.Pid), parts[2]}, ":")
	if !relayoutOwnerIsDeadLocalProcess(crashed) {
		t.Fatalf("the successor of a crashed cleanup (%q) is not recognized as dead", crashed)
	}
}

func TestInitializeOutsideRelayoutRefusesBeforeCreatingDatabases(t *testing.T) {
	original := refuseDuringYugabyteRelayoutFn
	t.Cleanup(func() { refuseDuringYugabyteRelayoutFn = original })
	inProgress := errors.New("relayout in progress for purser (renamed_old)")
	refuseDuringYugabyteRelayoutFn = func(context.Context, *fwssh.Pool, inventory.Host, *inventory.PostgresConfig) error { return inProgress }

	yugabyte := &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte"}
	initialized := false
	err := initializeOutsideRelayout(context.Background(), nil, inventory.Host{Name: "yb-1"}, yugabyte, func() error { initialized = true; return nil })
	if !errors.Is(err, inProgress) || initialized {
		t.Fatalf("initialize during a relayout = %v, initialized=%v; want the guard's refusal before any database is created", err, initialized)
	}
	postgres := &inventory.PostgresConfig{Enabled: true, Engine: "postgres"}
	if err = initializeOutsideRelayout(context.Background(), nil, inventory.Host{Name: "pg-1"}, postgres, func() error { initialized = true; return nil }); err != nil || !initialized {
		t.Fatalf("vanilla Postgres initialize = %v, initialized=%v; it has no relayouts to guard", err, initialized)
	}
}

// TestDatabaseInitializingCommandsUseTheRelayoutGuard pins that cluster init and cluster upgrade create databases only
// through initializeOutsideRelayout, as provision and migrate check the journal before theirs.
func TestDatabaseInitializingCommandsUseTheRelayoutGuard(t *testing.T) {
	for file, want := range map[string]string{
		"cluster_init.go":    "initializeOutsideRelayout(ctx, pool, host, pg,",
		"cluster_upgrade.go": "initializeOutsideRelayout(ctx, sshPool, host, manifest.Infrastructure.Postgres,",
	} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if !strings.Contains(string(source), want) {
			t.Fatalf("%s does not initialize databases through the relayout guard", file)
		}
	}
}
