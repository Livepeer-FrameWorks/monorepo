package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// ============================ cluster_provision.go ============================

func TestBuildControllerQuorum(t *testing.T) {
	t.Parallel()
	if buildControllerQuorum(&inventory.Manifest{}, nil) != "" {
		t.Fatal("nil view → empty")
	}
	m := &inventory.Manifest{}
	c := &kafkaClusterView{
		Brokers: []inventory.KafkaBroker{{ID: 1, Host: "h1"}, {ID: 2, Host: "h2"}},
	}
	// No ControllerPort set → default 9093; MeshAddress with no Hosts → host name.
	got := buildControllerQuorum(m, c)
	if got != "1@h1:9093,2@h2:9093" {
		t.Fatalf("quorum = %q", got)
	}
}

func TestBuildBootstrapServers(t *testing.T) {
	t.Parallel()
	if buildBootstrapServers(&inventory.Manifest{}, nil) != "" {
		t.Fatal("nil view → empty")
	}
	c := &kafkaClusterView{
		Controllers: []inventory.KafkaController{{ID: 1, Host: "c1", Port: 9000}, {ID: 2, Host: "c2"}},
	}
	got := buildBootstrapServers(&inventory.Manifest{}, c)
	// First has explicit port, second defaults to 9093.
	if got != "c1:9000,c2:9093" {
		t.Fatalf("bootstrap = %q", got)
	}
}

func TestBuildDedicatedControllerQuorum(t *testing.T) {
	t.Parallel()
	c := &kafkaClusterView{
		Controllers: []inventory.KafkaController{{ID: 5, Host: "c1"}},
	}
	if got := buildDedicatedControllerQuorum(&inventory.Manifest{}, c); got != "5@c1:9093" {
		t.Fatalf("dedicated quorum = %q", got)
	}
}

func TestKafkaControllersToMetadata(t *testing.T) {
	t.Parallel()
	if kafkaControllersToMetadata(&inventory.Manifest{}, nil) != nil {
		t.Fatal("nil view → nil")
	}
	c := &kafkaClusterView{
		Controllers: []inventory.KafkaController{{ID: 1, Host: "c1", DirID: "uuid-1"}, {ID: 2, Host: "c2"}},
	}
	md := kafkaControllersToMetadata(&inventory.Manifest{}, c)
	if len(md) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(md))
	}
	if md[0]["host"] != "c1" || md[0]["id"] != 1 || md[0]["port"] != 9093 || md[0]["dir_id"] != "uuid-1" {
		t.Fatalf("entry 0 wrong: %v", md[0])
	}
	// dir_id omitted when empty.
	if _, ok := md[1]["dir_id"]; ok {
		t.Fatalf("empty dir_id should be omitted: %v", md[1])
	}
}

func TestKafkaBrokersToMetadata(t *testing.T) {
	t.Parallel()
	if kafkaBrokersToMetadata(&inventory.Manifest{}, nil) != nil {
		t.Fatal("nil view → nil")
	}
	c := &kafkaClusterView{
		Brokers: []inventory.KafkaBroker{{ID: 1, Host: "b1", Port: 9092}},
	}
	md := kafkaBrokersToMetadata(&inventory.Manifest{}, c)
	if len(md) != 1 || md[0]["host"] != "b1" || md[0]["id"] != 1 {
		t.Fatalf("broker metadata wrong: %v", md)
	}
}

func TestKafkaBuildersUseMeshAddress(t *testing.T) {
	t.Parallel()
	// When the host has a WireguardIP, MeshAddress resolves to it.
	m := &inventory.Manifest{Hosts: map[string]inventory.Host{
		"h1": {Name: "h1", WireguardIP: "10.88.0.1"},
	}}
	c := &kafkaClusterView{Brokers: []inventory.KafkaBroker{{ID: 1, Host: "h1"}}}
	if got := buildControllerQuorum(m, c); got != "1@10.88.0.1:9093" {
		t.Fatalf("expected mesh IP in quorum, got %q", got)
	}
}

func TestDatabaseConfigsToMetadata(t *testing.T) {
	t.Parallel()
	dbs := []inventory.DatabaseConfig{
		{Name: "bridge", Owner: "bridge_user"},
		{Name: "commodore"},
	}
	// defaultPassword applies when no env override.
	md := databaseConfigsToMetadata(dbs, "secret")
	if len(md) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(md))
	}
	if md[0]["name"] != "bridge" || md[0]["owner"] != "bridge_user" || md[0]["password"] != "secret" {
		t.Fatalf("row 0 wrong: %v", md[0])
	}
	// Empty default + no env → no password key.
	md = databaseConfigsToMetadata(dbs, "")
	if _, ok := md[0]["password"]; ok {
		t.Fatalf("empty password should be omitted: %v", md[0])
	}
}

func TestServiceConfigForTask(t *testing.T) {
	t.Parallel()
	configs := map[string]inventory.ServiceConfig{
		"commodore":  {Enabled: true},
		"foghorn-eu": {Deploy: "foghorn", Cluster: "eu", Enabled: true},
	}
	// nil task → false.
	if _, _, ok := serviceConfigForTask(configs, nil); ok {
		t.Fatal("nil task → false")
	}
	// Direct ServiceID hit.
	name, _, ok := serviceConfigForTask(configs, &orchestrator.Task{ServiceID: "commodore"})
	if !ok || name != "commodore" {
		t.Fatalf("direct hit failed: %q %v", name, ok)
	}
	// Deploy-name + cluster fallback.
	name, _, ok = serviceConfigForTask(configs, &orchestrator.Task{ServiceID: "foghorn", Type: "foghorn", ClusterID: "eu"})
	if !ok || name != "foghorn-eu" {
		t.Fatalf("deploy fallback failed: %q %v", name, ok)
	}
	// Wrong cluster → no match (config is cluster-scoped to eu).
	if _, _, ok := serviceConfigForTask(configs, &orchestrator.Task{ServiceID: "foghorn", Type: "foghorn", ClusterID: "us"}); ok {
		t.Fatal("cluster mismatch should not match a cluster-scoped config")
	}
}

// ============================ cluster_releases.go ============================

func TestReleaseTargetVersionForSelector(t *testing.T) {
	t.Parallel()
	for _, sel := range []string{"", "latest", "stable", "rc", "  STABLE  "} {
		if got := releaseTargetVersionForSelector(sel, "v1.2.3"); got != "" {
			t.Fatalf("channel selector %q should yield empty (follow head), got %q", sel, got)
		}
	}
	if got := releaseTargetVersionForSelector("v1.2.3", "v9.9.9"); got != "v9.9.9" {
		t.Fatalf("pinned selector should return platform version, got %q", got)
	}
}

func TestPlatformKeyHelpers(t *testing.T) {
	t.Parallel()
	// platformKeyFromArtifactName is covered in cluster_releases_test.go; these
	// two siblings (canonicalization to os/arch and os-arch) are not.
	if platformKey(" Linux ", "AMD64") != "linux/amd64" {
		t.Fatal("platformKey should lowercase/trim/join with slash")
	}
	if platformArtifactName("Linux", "Arm64") != "linux-arm64" {
		t.Fatal("platformArtifactName joins with hyphen")
	}
}

func TestValidateEdgeReleaseChecksum(t *testing.T) {
	t.Parallel()
	valid256 := strings.Repeat("a", 64)
	valid512 := strings.Repeat("b", 128)
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "bare sha256 digest", in: valid256, wantErr: false},
		{name: "prefixed sha256", in: "sha256:" + valid256, wantErr: false},
		{name: "prefixed sha512", in: "sha512:" + valid512, wantErr: false},
		{name: "wrong length", in: "sha256:" + strings.Repeat("a", 10), wantErr: true},
		{name: "non-hex", in: "sha256:" + strings.Repeat("z", 64), wantErr: true},
		{name: "unknown algo", in: "md5:" + strings.Repeat("a", 32), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateEdgeReleaseChecksum(tc.in); (err != nil) != tc.wantErr {
				t.Fatalf("validateEdgeReleaseChecksum(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// (edgeReleaseHasUpdateableComponent already tested in cluster_releases_test.go)

func TestValidateEdgeReleaseComponent(t *testing.T) {
	t.Parallel()
	good := edgeReleaseComponentSpec{
		Version: "v1.0.0",
		Artifacts: map[string]edgeReleaseArtifactSpec{
			"linux-amd64": {ArtifactURL: "https://x/bin", Checksum: "sha256:" + strings.Repeat("a", 64)},
		},
	}
	if err := validateEdgeReleaseComponent("helmsman", good); err != nil {
		t.Fatalf("valid component should pass: %v", err)
	}
	// Missing version.
	if err := validateEdgeReleaseComponent("helmsman", edgeReleaseComponentSpec{Artifacts: good.Artifacts}); err == nil {
		t.Fatal("missing version should error")
	}
	// No artifacts.
	if err := validateEdgeReleaseComponent("helmsman", edgeReleaseComponentSpec{Version: "v1"}); err == nil {
		t.Fatal("missing artifacts should error")
	}
	// Bad platform key.
	bad := edgeReleaseComponentSpec{Version: "v1", Artifacts: map[string]edgeReleaseArtifactSpec{
		"bogus": {ArtifactURL: "u", Checksum: "sha256:" + strings.Repeat("a", 64)},
	}}
	if err := validateEdgeReleaseComponent("helmsman", bad); err == nil {
		t.Fatal("invalid platform key should error")
	}
}

// (upgradeRollbackSupported and collectUpgradeableServices already tested in
// cluster_upgrade_test.go)

// ============================ cluster_snapshot.go ============================

func TestPostgresSnapshotBinary(t *testing.T) {
	t.Parallel()
	if postgresSnapshotBinary(true) != "ysqlsh" || postgresSnapshotBinary(false) != "psql" {
		t.Fatal("binary selection wrong")
	}
}

func TestDatabaseNamesFromConfigs(t *testing.T) {
	t.Parallel()
	got := databaseNamesFromConfigs([]inventory.DatabaseConfig{
		{Name: " bridge "}, {Name: "bridge"}, {Name: ""}, {Name: "commodore"},
	})
	if len(got) != 2 {
		t.Fatalf("expected trim+dedupe to 2, got %v", got)
	}
}

func TestPostgresSnapshotScriptShellQuotes(t *testing.T) {
	t.Parallel()
	target := postgresSnapshotTarget{
		Port: 5432, User: "admin", Password: "p4ss'word", Binary: "psql",
		HostName: "h1", Databases: []string{"bridge"}, UsePeerAuth: false,
	}
	script := postgresSnapshotScript(target)
	// Structure markers.
	for _, want := range []string{"PORT=5432", "PEER=0", "== postgres target ==", "pg_stat_user_tables"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q", want)
		}
	}
	// Password with a single quote must be shell-escaped, never appearing raw.
	if strings.Contains(script, "PASSWORD=p4ss'word\n") {
		t.Fatal("password was not shell-quoted")
	}
	// Peer-auth variant flips PEER.
	target.UsePeerAuth = true
	if !strings.Contains(postgresSnapshotScript(target), "PEER=1") {
		t.Fatal("peer-auth should set PEER=1")
	}
}

func TestClickHouseSnapshotScript(t *testing.T) {
	t.Parallel()
	script := clickHouseSnapshotScript([]string{"analytics"}, 9000, "default", "")
	for _, want := range []string{"PORT=9000", "clickhouse-client", "DATABASES="} {
		if !strings.Contains(script, want) {
			t.Fatalf("clickhouse script missing %q", want)
		}
	}
}

func TestFormatSnapshotCommandResult(t *testing.T) {
	t.Parallel()
	// nil result → placeholder.
	out := formatSnapshotCommandResult("pg", inventory.Host{ExternalIP: "1.2.3.4"}, nil)
	if !strings.Contains(out, "# target: pg") || !strings.Contains(out, "no command result") {
		t.Fatalf("nil-result output wrong:\n%s", out)
	}
}

// ============================ cluster_os_update.go ============================

func TestOSUpdateHostList(t *testing.T) {
	t.Parallel()
	if hosts, err := osUpdateHostList(nil, ""); hosts != nil || err != nil {
		t.Fatal("nil manifest → (nil,nil)")
	}
	m := &inventory.Manifest{Hosts: map[string]inventory.Host{
		"b": {Name: "b"}, "a": {Name: "a"}, "c": {Name: "c"},
	}}
	// No filter → all hosts, sorted by name.
	all, err := osUpdateHostList(m, "")
	if err != nil || len(all) != 3 || all[0].Name != "a" || all[2].Name != "c" {
		t.Fatalf("unfiltered sorted list wrong: %v err %v", all, err)
	}
	// CSV filter selects a subset.
	sub, err := osUpdateHostList(m, "a,c")
	if err != nil || len(sub) != 2 || sub[0].Name != "a" || sub[1].Name != "c" {
		t.Fatalf("filtered list wrong: %v err %v", sub, err)
	}
	// Unknown host → error.
	if _, err := osUpdateHostList(m, "ghost"); err == nil {
		t.Fatal("unknown host should error")
	}
}

func TestOSUpdateCheckScript(t *testing.T) {
	t.Parallel()
	if !strings.Contains(osUpdateCheckScript(true), "true") {
		t.Fatal("refresh=true should appear in script")
	}
	if !strings.Contains(osUpdateCheckScript(false), "false") {
		t.Fatal("refresh=false should appear in script")
	}
}

// ============================ edge.go ============================

// (parseEdgeServiceStatus tested in edge_service_parse_test.go; deriveEdgeNodeName
// and canonicalEdgeNodeID in edge_test.go.)

func TestEdgeManifestNeedsControlPlane(t *testing.T) {
	t.Parallel()
	if edgeManifestNeedsControlPlane(nil) {
		t.Fatal("nil → false")
	}
	m := &inventory.EdgeManifest{Nodes: []inventory.EdgeNode{{RegisterQM: false}}}
	if edgeManifestNeedsControlPlane(m) {
		t.Fatal("no RegisterQM node → false")
	}
	m.Nodes = append(m.Nodes, inventory.EdgeNode{RegisterQM: true})
	if !edgeManifestNeedsControlPlane(m) {
		t.Fatal("a RegisterQM node → true")
	}
}
