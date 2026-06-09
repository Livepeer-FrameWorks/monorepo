package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/structpb"
)

// ============================ cluster_os_update.go ============================

func TestParseOSUpdateCheckOutput(t *testing.T) {
	t.Parallel()
	base := osUpdateCheckResult{Host: "h1", AptListAgeSeconds: -1, UpgradeSummary: "0 upgraded, 0 newly installed, 0 to remove"}

	t.Run("skip family leaves baseline non-pending", func(t *testing.T) {
		t.Parallel()
		got := parseOSUpdateCheckOutput("FW_OS_FAMILY=skip\n", base)
		if !got.Skipped || got.Pending {
			t.Fatalf("skip should set Skipped and stay non-pending: %+v", got)
		}
	})

	t.Run("parses fields and derives pending", func(t *testing.T) {
		t.Parallel()
		out := strings.Join([]string{
			"FW_APT_LIST_AGE_SECONDS=120",
			"FW_UPGRADE_SUMMARY=3 upgraded, 0 newly installed, 0 to remove",
			"FW_NEEDRESTART_UNIT=sshd.service",
			"FW_REBOOT_REQUIRED=true",
			"FW_REBOOT_PKG=linux-image",
			"garbage line without equals",
		}, "\n")
		got := parseOSUpdateCheckOutput(out, base)
		if got.AptListAgeSeconds != 120 {
			t.Fatalf("age = %d", got.AptListAgeSeconds)
		}
		if got.UpgradeSummary != "3 upgraded, 0 newly installed, 0 to remove" {
			t.Fatalf("summary = %q", got.UpgradeSummary)
		}
		if len(got.NeedrestartUnits) != 1 || got.NeedrestartUnits[0] != "sshd.service" {
			t.Fatalf("needrestart = %v", got.NeedrestartUnits)
		}
		if !got.RebootRequired || len(got.RebootRequiredPkgs) != 1 {
			t.Fatalf("reboot fields wrong: %+v", got)
		}
		if !got.Pending {
			t.Fatal("non-zero upgrades / reboot should be Pending")
		}
	})

	t.Run("zero upgrades and no restart is not pending", func(t *testing.T) {
		t.Parallel()
		got := parseOSUpdateCheckOutput("FW_UPGRADE_SUMMARY=0 upgraded, 0 newly installed, 0 to remove\n", base)
		if got.Pending {
			t.Fatalf("should not be pending: %+v", got)
		}
	})
}

// ============================ cluster_nodes.go ============================

func TestParseRemoteEdgeEnvOutput(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"__FRAMEWORKS_ENV_FILE=/opt/frameworks/edge/.edge.env",
		"CLUSTER_ID=cl-1",
		"NODE_ID=node-7",
		"DEPLOY_MODE=native",
		"EDGE_DOMAIN=edge.example.com",
		"FOGHORN_CONTROL_ADDR=foghorn:18019",
		"UNRELATED=ignored",
		"", // trailing blank line
	}, "\n")
	env := parseRemoteEdgeEnvOutput(out)
	if env.File != "/opt/frameworks/edge/.edge.env" || env.ClusterID != "cl-1" || env.NodeID != "node-7" ||
		env.DeployMode != "native" || env.Domain != "edge.example.com" || env.Foghorn != "foghorn:18019" {
		t.Fatalf("parsed env wrong: %+v", env)
	}
	if !env.HasFrameworksEnv() {
		t.Fatal("populated env should report HasFrameworksEnv")
	}
	// Empty / no-match output → zero value.
	if (parseRemoteEdgeEnvOutput("__FRAMEWORKS_ENV_FILE=\n")).HasFrameworksEnv() {
		t.Fatal("empty env file marker → no frameworks env")
	}
}

// ============================ cluster_backup.go ============================

func TestBuildPostgresBackupCommand(t *testing.T) {
	t.Parallel()
	cmd := buildPostgresBackupCommand("/backups", "/backups/postgres-ts.sql")
	for _, want := range []string{"mkdir -p", "pg_dumpall -U postgres", "/opt/frameworks/postgres/docker-compose.yml"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("postgres backup cmd missing %q: %s", want, cmd)
		}
	}
	// Paths are shell-quoted.
	if !strings.Contains(cmd, "'/backups'") || !strings.Contains(cmd, "'/backups/postgres-ts.sql'") {
		t.Fatalf("paths not shell-quoted: %s", cmd)
	}
}

func TestBuildClickHouseBackupScript(t *testing.T) {
	t.Parallel()
	script := buildClickHouseBackupScript("/backups/ch-ts")
	for _, want := range []string{"SHOW DATABASES", "FORMAT TSV", "information_schema", "'/backups/ch-ts'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("clickhouse script missing %q", want)
		}
	}
}

// ============================ admin.go request builders ======================

func TestBuildCreateBootstrapTokenRequest(t *testing.T) {
	t.Parallel()
	// Minimal: optional fields stay nil.
	req := buildCreateBootstrapTokenRequest("tok", "service", "24h", nil, "", "", "", 0)
	if req.Name != "tok" || req.Kind != "service" || req.Ttl != "24h" {
		t.Fatalf("base fields wrong: %+v", req)
	}
	if req.TenantId != nil || req.ClusterId != nil || req.ExpectedIp != nil || req.UsageLimit != nil {
		t.Fatalf("unset optionals should be nil: %+v", req)
	}
	// Full: optional fields populated.
	meta, _ := structpb.NewStruct(map[string]any{"k": "v"})
	req = buildCreateBootstrapTokenRequest("tok", "edge_node", "1h", meta, "tid", "cid", "1.2.3.4", 5)
	if req.GetTenantId() != "tid" || req.GetClusterId() != "cid" || req.GetExpectedIp() != "1.2.3.4" {
		t.Fatalf("optional strings wrong: %+v", req)
	}
	if req.GetUsageLimit() != 5 {
		t.Fatalf("usage limit = %d", req.GetUsageLimit())
	}
	if req.Metadata == nil {
		t.Fatal("metadata should pass through")
	}
}

func TestBuildCreateClusterRequest(t *testing.T) {
	t.Parallel()

	newCmd := func() *cobra.Command {
		c := &cobra.Command{Use: "create"}
		c.Flags().Bool("is-platform-official", false, "")
		c.Flags().Bool("is-default-cluster", false, "")
		return c
	}

	// No bool flags changed → those pointers stay nil; scalars and lists set.
	c := newCmd()
	req := buildCreateClusterRequest(c, "cl-1", "Cluster One", "edge", "https://base", "h1:9092,h2:9092",
		"", "", "", "managed", 10, 20, 100, 0, false, false)
	if req.ClusterId != "cl-1" || req.ClusterName != "Cluster One" || req.DeploymentModel != "managed" {
		t.Fatalf("base fields wrong: %+v", req)
	}
	if len(req.KafkaBrokers) != 2 {
		t.Fatalf("kafka brokers should parse to 2, got %v", req.KafkaBrokers)
	}
	if req.MaxConcurrentStreams != 10 || req.MaxConcurrentViewers != 20 || req.MaxBandwidthMbps != 100 {
		t.Fatalf("limits wrong: %+v", req)
	}
	if req.IsPlatformOfficial != nil || req.IsDefaultCluster != nil {
		t.Fatal("unchanged bool flags should leave pointers nil")
	}
	if req.DatabaseUrl != nil || req.PeriscopeUrl != nil || req.OwnerTenantId != nil {
		t.Fatal("empty optional strings should be nil")
	}

	// Bool flags changed → pointers set; optional strings populated.
	c = newCmd()
	_ = c.Flags().Set("is-platform-official", "true")
	_ = c.Flags().Set("is-default-cluster", "true")
	req = buildCreateClusterRequest(c, "cl-2", "Two", "central", "https://b", "",
		"postgres://db", "https://peri", "owner-1", "shared", 0, 0, 0, 3, true, true)
	if req.GetIsPlatformOfficial() != true || req.GetIsDefaultCluster() != true {
		t.Fatalf("changed bool flags should set pointers: %+v", req)
	}
	if req.GetDatabaseUrl() != "postgres://db" || req.GetPeriscopeUrl() != "https://peri" || req.GetOwnerTenantId() != "owner-1" {
		t.Fatalf("optional strings wrong: %+v", req)
	}
	if req.FoghornCount != 3 {
		t.Fatalf("foghorn count = %d", req.FoghornCount)
	}
}
