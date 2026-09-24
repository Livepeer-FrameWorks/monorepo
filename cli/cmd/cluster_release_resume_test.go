package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"

	"github.com/spf13/cobra"
)

func TestReleaseApplyUpgradeFailurePrintsResumeHint(t *testing.T) {
	sqlLog := installFakeSQLClient(t, "t", `{"expand_applied":true,"managed_keyless_total":0,"customer_keyless_total":0,"dns_grants_total":0}`)
	out, _, err := applyReleaseForPostExpandReport(t, false, sqlLog)
	if !errors.Is(err, errPostExpandHarnessStoppedAtUpgrades) {
		t.Fatalf("err = %v, want the harness upgrade failure\n%s", err, out)
	}
	for _, want := range []string{
		"postdeploy migrations; they have not run",
		"rerun `frameworks cluster release apply --version v0.3.11` to resume",
		"report up to date and are skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestReleaseApplyDryRunUpgradeFailureHasNoResumeHint(t *testing.T) {
	sqlLog := installFakeSQLClient(t, "f", `{"expand_applied":true,"managed_keyless_total":0,"customer_keyless_total":0,"dns_grants_total":0}`)
	out, _, err := applyReleaseForPostExpandReport(t, true, sqlLog)
	if !errors.Is(err, errPostExpandHarnessStoppedAtUpgrades) {
		t.Fatalf("err = %v, want the harness upgrade failure\n%s", err, out)
	}
	if strings.Contains(out, "to resume") {
		t.Fatalf("dry-run printed a resume hint:\n%s", out)
	}
}

func TestResolveUpgradeVersionChannelAwareWarning(t *testing.T) {
	tests := []struct {
		channel, version string
		warn             bool
	}{
		{"candidate", "v0.3.11-rc1", false},
		{"candidate", "v0.3.11", false},
		{"candidate", "candidate", false},
		{"candidate", "", false},
		{"rc", "v0.3.11-rc1", false},
		{"rc", "v0.3.11", true},
		{"stable", "v0.3.11", false},
		{"stable", "v0.3.11-rc1", true},
		{"stable", "candidate", true},
		{"", "v0.3.11", false},
	}
	for _, tt := range tests {
		cmd := &cobra.Command{}
		var stderr bytes.Buffer
		cmd.SetOut(&stderr)
		cmd.SetErr(&stderr)
		got := resolveUpgradeVersion(cmd, &inventory.Manifest{Channel: tt.channel}, tt.version)
		if tt.version != "" && got != tt.version {
			t.Errorf("channel %q version %q: resolved %q", tt.channel, tt.version, got)
		}
		if warned := strings.Contains(stderr.String(), "Warning: cluster channel"); warned != tt.warn {
			t.Errorf("channel %q version %q: warned=%v, want %v (%q)", tt.channel, tt.version, warned, tt.warn, stderr.String())
		}
	}
}

// release apply and `upgrade --all` hand runUpgrade an already-pinned tag; the
// per-service call must not repeat the channel check the operator selector
// already passed.
func TestRunUpgradeDoesNotRewarnForPinnedTag(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	rc := &resolvedCluster{Manifest: &inventory.Manifest{Channel: "stable"}}
	if _, err := runUpgrade(cmd, rc, "commodore", "v0.3.11-rc1", true, true, true, true, false, false, true); err == nil {
		t.Fatal("expected the harness manifest to have no commodore host")
	}
	if strings.Contains(out.String(), "Warning: cluster channel") {
		t.Fatalf("runUpgrade re-warned about the pinned tag:\n%s", out.String())
	}
}

// inspectingProvisioner reports a fixed check-mode inspection.
type inspectingProvisioner struct {
	fakeTaskProvisioner
	inspection provisioner.ChangeInspection
	inspectErr error
	tags       [][]string
}

func (p *inspectingProvisioner) InspectChanges(_ context.Context, _ inventory.Host, _ provisioner.ServiceConfig, tags []string) (provisioner.ChangeInspection, error) {
	p.tags = append(p.tags, tags)
	return p.inspection, p.inspectErr
}

func TestProvisionTaskPrecheckNamesChangingTasks(t *testing.T) {
	prov := &inspectingProvisioner{inspection: provisioner.ChangeInspection{
		Changed: true,
		Tasks:   []string{"MirrorMaker | render mm2.properties", "MirrorMaker | render mm2.properties", "MirrorMaker | restart"},
	}}
	task := &orchestrator.Task{Name: "kafka-mirrormaker", Type: "kafka-mirrormaker", Host: "kafka-1"}
	var out bytes.Buffer
	changed, err := provisionTaskPrecheck(context.Background(), &out, prov, task, inventory.Host{}, provisioner.ServiceConfig{})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want changed", changed, err)
	}
	want := "  kafka-mirrormaker on kafka-1 differs from desired state; converging\n" +
		"    role check reports changes in:\n" +
		"      - MirrorMaker | render mm2.properties\n" +
		"      - MirrorMaker | restart\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
	if prov.calls != nil {
		t.Fatalf("an inspecting provisioner must not also run WouldChange: %v", prov.calls)
	}

	prov = &inspectingProvisioner{}
	out.Reset()
	changed, err = provisionTaskPrecheck(context.Background(), &out, prov, task, inventory.Host{}, provisioner.ServiceConfig{})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want unchanged", changed, err)
	}
	if !strings.Contains(out.String(), "already matches desired install/config/service state; skipping provision") {
		t.Fatalf("unchanged output = %q", out.String())
	}
}

func TestUpgradeInitializeNeeded(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer

	unchanged := &inspectingProvisioner{}
	if upgradeInitializeNeeded(ctx, &out, unchanged, "clickhouse", inventory.Host{}, provisioner.ServiceConfig{}) {
		t.Fatal("clickhouse init with a clean precheck must be skipped")
	}
	if !reflect.DeepEqual(unchanged.tags, [][]string{{"init"}}) {
		t.Fatalf("precheck tags = %v, want [[init]]", unchanged.tags)
	}

	changed := &inspectingProvisioner{inspection: provisioner.ChangeInspection{Changed: true}}
	if !upgradeInitializeNeeded(ctx, &out, changed, "clickhouse", inventory.Host{}, provisioner.ServiceConfig{}) {
		t.Fatal("clickhouse init with pending changes must run")
	}

	failing := &inspectingProvisioner{inspectErr: errors.New("ansible check failed")}
	if !upgradeInitializeNeeded(ctx, &out, failing, "clickhouse", inventory.Host{}, provisioner.ServiceConfig{}) {
		t.Fatal("a failed init precheck must fall back to initializing")
	}

	for _, deploy := range []string{"postgres", "kafka", "kafka-mirrormaker"} {
		prov := &inspectingProvisioner{}
		if !upgradeInitializeNeeded(ctx, &out, prov, deploy, inventory.Host{}, provisioner.ServiceConfig{}) {
			t.Fatalf("%s init must always run", deploy)
		}
		if prov.tags != nil {
			t.Fatalf("%s init precheck ran; its check mode is not trusted", deploy)
		}
	}
}
