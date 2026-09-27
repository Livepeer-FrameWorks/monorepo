package cmd

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// resolvedClusterWithEnv is a resolvedCluster whose manifest env_files already
// decrypted to sharedEnv.
func resolvedClusterWithEnv(manifest *inventory.Manifest, sharedEnv map[string]string) *resolvedCluster {
	rc := &resolvedCluster{Manifest: manifest}
	rc.sharedEnvOnce.Do(func() { rc.sharedEnv = sharedEnv })
	rc.preparedSharedEnvOnce.Do(func() {})
	rc.clusterEnvsOnce.Do(func() {})
	return rc
}

func renderParityManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.test",
		Hosts: map[string]inventory.Host{
			"core-1": {Name: "core-1", ExternalIP: "10.0.0.1", Cluster: "core-eu"},
			"core-2": {Name: "core-2", ExternalIP: "10.0.0.2", Cluster: "core-eu"},
			"db-1":   {Name: "db-1", ExternalIP: "10.0.0.11"},
			"db-2":   {Name: "db-2", ExternalIP: "10.0.0.12"},
			"db-3":   {Name: "db-3", ExternalIP: "10.0.0.13"},
		},
		Clusters: map[string]inventory.ClusterConfig{"core-eu": {}},
		Services: map[string]inventory.ServiceConfig{
			"commodore": {Enabled: true, Hosts: []string{"core-1", "core-2"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Nodes: []inventory.PostgresNode{
					{Host: "db-1", ID: 1},
					{Host: "db-2", ID: 2},
					{Host: "db-3", ID: 3},
				},
				Databases: []inventory.DatabaseConfig{{Name: "commodore"}},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Nodes:   []inventory.ClickHouseNode{{Host: "db-1", ID: 1}},
			},
		},
	}
}

// `cluster upgrade` and `release apply` render every replica through the
// provision render for the planner's own task, so provision and upgrade hand
// the role identical vars: infrastructure credentials, instance identity, and
// the per-host cluster included.
func TestUpgradeRendersEveryReplicaLikeProvision(t *testing.T) {
	manifest := renderParityManifest()
	sharedEnv := map[string]string{
		"SERVICE_TOKEN":                "service-token",
		"DATABASE_PASSWORD":            "pg-owner",
		"DATABASE_RUNTIME_PASSWORD":    "pg-runtime",
		"CLICKHOUSE_PASSWORD":          "ch-default",
		"CLICKHOUSE_READONLY_PASSWORD": "ch-readonly",
		"JWT_SECRET":                   "jwt",
		"USAGE_HASH_SECRET":            "usage",
		"FIELD_ENCRYPTION_KEY":         "field",
	}
	rc := resolvedClusterWithEnv(manifest, sharedEnv)
	runtimeData := map[string]any{"service_token": "service-token", "system_tenant_id": "system-tenant"}

	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	checked := map[string]bool{}
	for _, task := range plan.AllTasks {
		serviceName := task.ServiceID
		switch task.Type {
		case "yugabyte", "clickhouse":
			serviceName = task.Type
		case "commodore":
		default:
			continue
		}
		host := manifest.Hosts[task.Host]
		provisioned, err := renderTaskConfig(task, manifest, false, runtimeData, "", sharedEnv, nil, nil)
		if err != nil {
			t.Fatalf("provision render %s on %s: %v", task.Name, task.Host, err)
		}
		upgraded, upgradeTask, err := buildUpgradeTaskConfig(rc, manifest, host, serviceName, task.Type, runtimeData)
		if err != nil {
			t.Fatalf("upgrade render %s on %s: %v", task.Name, task.Host, err)
		}
		if !reflect.DeepEqual(upgradeTask, task) {
			t.Fatalf("upgrade of %s on %s rendered task %+v, want the planner task %+v", serviceName, task.Host, upgradeTask, task)
		}
		// Force is the only difference: an upgrade always redeploys.
		provisioned.Force, upgraded.Force = false, false
		if !reflect.DeepEqual(provisioned, upgraded) {
			t.Fatalf("%s on %s renders differently:\nprovision %+v\nupgrade   %+v", task.Name, task.Host, provisioned, upgraded)
		}
		checked[task.Type] = true

		switch task.Type {
		case "clickhouse":
			assertMetadata(t, upgraded, "clickhouse_password", "ch-default")
			assertMetadata(t, upgraded, "clickhouse_readonly_password", "ch-readonly")
		case "yugabyte":
			assertMetadata(t, upgraded, "postgres_password", "pg-owner")
			assertMetadata(t, upgraded, "postgres_runtime_password", "pg-runtime")
		}
	}
	for _, deploy := range []string{"commodore", "yugabyte", "clickhouse"} {
		if !checked[deploy] {
			t.Fatalf("no %s task was compared", deploy)
		}
	}
}

// `cluster upgrade redis` converges every server and Sentinel of every
// instance through the release Redis stage, not only the first instance's host.
func TestUpgradeRedisConvergesEveryServerAndSentinel(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "master", "redis-b": "slave", "redis-c": "slave"}}
	fake := &redisReleaseProvisioner{log: log}
	originalProvisioner, originalConvergence := taskProvisioner, releaseNewHostConvergenceFn
	t.Cleanup(func() { taskProvisioner, releaseNewHostConvergenceFn = originalProvisioner, originalConvergence })
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	releaseNewHostConvergenceFn = func(cmd *cobra.Command, rc *resolvedCluster, _ string, _ *ssh.Pool) (*releaseHostConvergence, error) {
		return &releaseHostConvergence{
			cmd:         cmd,
			manifest:    rc.Manifest,
			runtimeData: map[string]any{},
			sharedEnv:   map[string]string{},
			redisOps:    cluster,
			redisTiming: &redisGateTiming{Sync: 50 * time.Millisecond, Quorum: 50 * time.Millisecond, Failover: 50 * time.Millisecond, Interval: time.Millisecond},
		}, nil
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	rc := &resolvedCluster{Manifest: redisReleaseManifest()}
	if _, err := runUpgrade(cmd, rc, "redis", "v0.3.11", false, false, true, false, false, false, false); err != nil {
		t.Fatalf("upgrade redis: %v\n%s", err, out.String())
	}
	var provisioned []string
	for _, event := range log.snapshot() {
		if host, ok := strings.CutPrefix(event, "provision:"); ok {
			provisioned = append(provisioned, host)
		}
	}
	slices.Sort(provisioned)
	want := []string{
		"foghorn/primary:redis-a",
		"foghorn/replica:redis-b",
		"foghorn/replica:redis-c",
		"foghorn/sentinel:redis-a",
		"foghorn/sentinel:redis-b",
		"foghorn/sentinel:redis-c",
		"platform/primary:redis-a",
	}
	if !slices.Equal(provisioned, want) {
		t.Fatalf("converged %v, want every Redis server and Sentinel %v\n%s", provisioned, want, out.String())
	}
	if !slices.Contains(log.snapshot(), "failover:redis-a") {
		t.Fatalf("the live primary was converged without a Sentinel failover: %v", log.snapshot())
	}
}

// A replica already on the target artifact redeploys only when the role check
// reports a configuration change.
func TestCurrentArtifactConvergesOnlyOnConfigChange(t *testing.T) {
	host := inventory.Host{ExternalIP: "10.0.0.1"}
	var out bytes.Buffer
	unchanged := &inspectingProvisioner{}
	needed, err := currentArtifactNeedsConvergence(context.Background(), &out, unchanged, host, provisioner.ServiceConfig{}, "v0.3.11")
	if err != nil || needed {
		t.Fatalf("unchanged replica: needed=%v err=%v", needed, err)
	}
	if !strings.Contains(out.String(), "already at version v0.3.11 and converged, nothing to do") {
		t.Fatalf("output = %q", out.String())
	}

	out.Reset()
	changed := &inspectingProvisioner{inspection: provisioner.ChangeInspection{Changed: true, Tasks: []string{"Go_service | render env file"}}}
	needed, err = currentArtifactNeedsConvergence(context.Background(), &out, changed, host, provisioner.ServiceConfig{}, "v0.3.11")
	if err != nil || !needed {
		t.Fatalf("changed replica: needed=%v err=%v", needed, err)
	}
	if !strings.Contains(out.String(), "Go_service | render env file") {
		t.Fatalf("output does not name the changing task: %q", out.String())
	}

	failing := &inspectingProvisioner{inspectErr: errors.New("ssh: unreachable")}
	if _, err = currentArtifactNeedsConvergence(context.Background(), &out, failing, host, provisioner.ServiceConfig{}, "v0.3.11"); err == nil {
		t.Fatal("a failed precheck reported the replica converged")
	}
}

func assertMetadata(t *testing.T, config provisioner.ServiceConfig, key, want string) {
	t.Helper()
	if got, _ := config.Metadata[key].(string); got != want {
		t.Fatalf("metadata %s = %q, want %q", key, got, want)
	}
}
