package cmd

import (
	"errors"
	"strings"
	"testing"

	"frameworks/cli/internal/readiness"
	"frameworks/cli/pkg/inventory"
)

func TestDoctorServiceRemediation_mapsKnownServicesToRunnableCmd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		wantCmd string
	}{
		{"Postgres/Yugabyte", "frameworks cluster logs postgres"},
		{"postgres", "frameworks cluster logs postgres"},
		{"Kafka Broker 1", "frameworks cluster diagnose kafka"},
		{"ClickHouse", "frameworks cluster logs clickhouse"},
		{"Redis", "frameworks cluster logs redis"},
	}
	for _, tc := range cases {
		step := doctorServiceRemediation(tc.name)
		if step.Cmd == "" {
			t.Errorf("%q: expected a runnable Cmd, got empty", tc.name)
			continue
		}
		if !strings.Contains(step.Cmd, tc.wantCmd) {
			t.Errorf("%q: Cmd = %q, want contains %q", tc.name, step.Cmd, tc.wantCmd)
		}
		if step.Why == "" {
			t.Errorf("%q: expected a Why explanation, got empty", tc.name)
		}
	}
}

func TestDoctorServiceRemediation_appServiceFallsBackToGenericLogs(t *testing.T) {
	t.Parallel()
	step := doctorServiceRemediation("bridge")
	if step.Cmd != "frameworks cluster logs bridge" {
		t.Errorf("expected cluster-logs fallback for app service, got Cmd=%q", step.Cmd)
	}
}

func TestDoctorServiceRemediationReplicaUsesLogicalServiceName(t *testing.T) {
	t.Parallel()
	step := doctorServiceRemediation("periscope-ingest@regional-eu-2")
	if step.Cmd != "frameworks cluster logs periscope-ingest" {
		t.Fatalf("replica remediation Cmd = %q, want logical service logs command", step.Cmd)
	}
}

func TestDoctorServiceHostNamesIncludesEveryReplica(t *testing.T) {
	t.Parallel()
	svc := inventory.ServiceConfig{Hosts: []string{"regional-eu-1", "regional-eu-2", "regional-eu-3"}}
	got := doctorServiceHostNames("periscope-ingest", svc, &inventory.Manifest{})
	if len(got) != 3 {
		t.Fatalf("host count = %d, want 3: %v", len(got), got)
	}
	for i, want := range svc.Hosts {
		if got[i] != want {
			t.Fatalf("host[%d] = %q, want %q", i, got[i], want)
		}
	}
}

func TestDoctorServiceLabelIdentifiesReplicas(t *testing.T) {
	t.Parallel()
	if got := doctorServiceLabel("bridge", "regional-eu-1", 6); got != "bridge@regional-eu-1" {
		t.Fatalf("multi-replica label = %q", got)
	}
	if got := doctorServiceLabel("quartermaster", "central-eu-1", 1); got != "quartermaster" {
		t.Fatalf("single-replica label = %q", got)
	}
}

func TestDoctorControlPlaneDetail_distinguishesAllStates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		r    readiness.Report
		deep bool
		want string
	}{
		{"default mode + no check", readiness.Report{Checked: false}, false, "not verified (pass --deep"},
		{"deep mode + still no check (insufficient context)", readiness.Report{Checked: false}, true, "not verified (insufficient context"},
		{"checked + healthy", readiness.Report{Checked: true}, false, "healthy"},
		{"checked + 1 warning", readiness.Report{Checked: true, Warnings: []readiness.Warning{{Subject: "x", Detail: "d"}}}, true, "1 warning"},
		{"checked + 3 warnings", readiness.Report{Checked: true, Warnings: []readiness.Warning{{}, {}, {}}}, true, "3 warnings"},
	}
	for _, tc := range cases {
		got := doctorControlPlaneDetail(tc.r, tc.deep)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: detail = %q, want contains %q", tc.name, got, tc.want)
		}
	}
}

func TestClickHouseDoctorCheckerUsesSharedCredentials(t *testing.T) {
	t.Parallel()

	checker := clickHouseDoctorChecker(
		&inventory.ClickHouseConfig{Databases: []string{"periscope"}},
		map[string]string{
			"CLICKHOUSE_USER":     "frameworks",
			"CLICKHOUSE_PASSWORD": "secret",
		},
	)
	if checker.User != "frameworks" {
		t.Fatalf("User = %q, want frameworks", checker.User)
	}
	if checker.Password != "secret" {
		t.Fatalf("Password = %q, want secret", checker.Password)
	}
	if checker.Database != "periscope" {
		t.Fatalf("Database = %q, want periscope", checker.Database)
	}
}

func TestDoctorServiceProbeUsesDeployHealthPathForAlias(t *testing.T) {
	t.Parallel()

	probe := doctorServiceProbe("livepeer-gateway-eu", inventory.ServiceConfig{Deploy: "livepeer-gateway"})
	if probe.Protocol != "http" {
		t.Fatalf("Protocol = %q, want http", probe.Protocol)
	}
	if probe.Path != "/healthz" {
		t.Fatalf("Path = %q, want /healthz", probe.Path)
	}
}

func TestDoctorServiceProbeUsesTCPForGRPCAndNoHTTPHealthPath(t *testing.T) {
	t.Parallel()

	decklogProbe := doctorServiceProbe("decklog", inventory.ServiceConfig{})
	if decklogProbe.Protocol != "tcp" {
		t.Fatalf("decklog Protocol = %q, want tcp", decklogProbe.Protocol)
	}

	nginxProbe := doctorServiceProbe("nginx", inventory.ServiceConfig{})
	if nginxProbe.Protocol != "tcp" {
		t.Fatalf("nginx Protocol = %q, want tcp", nginxProbe.Protocol)
	}
}

func TestDoctorHTTPProbeCommandUsesHostLoopback(t *testing.T) {
	t.Parallel()

	got := doctorHTTPProbeCommand(8935, "/healthz")
	if !strings.Contains(got, "http://127.0.0.1:8935/healthz") {
		t.Fatalf("probe command = %q, want host-loopback health URL", got)
	}
	if strings.Contains(got, "0.0.0.0") {
		t.Fatalf("probe command must not dial a wildcard listener: %q", got)
	}
}

func TestDoctorTCPProbeCommandUsesHostLoopback(t *testing.T) {
	t.Parallel()

	got := doctorTCPProbeCommand(18006)
	if !strings.Contains(got, "/dev/tcp/127.0.0.1/18006") {
		t.Fatalf("probe command = %q, want host-loopback TCP address", got)
	}
}

// The doctor trusts a saved system tenant only when the saved context also
// supplied the manifest; a manifest passed with --gitops-dir resolves its own.
func TestDoctorContextSystemTenantIDOnlyFromContextManifest(t *testing.T) {
	manifest := restartFoghornCellManifest("eu", "us")
	cases := []struct {
		source inventory.ManifestSource
		want   string
	}{
		{inventory.SourceContext, "ctx-tenant"},
		{inventory.SourceContextLastManifest, "ctx-tenant"},
		{inventory.SourceGitopsDirFlag, ""},
		{inventory.SourceManifestFlag, ""},
	}
	for _, tc := range cases {
		rc := &resolvedCluster{Manifest: manifest, Source: tc.source, ContextSystemTenantID: " ctx-tenant "}
		if got := doctorContextSystemTenantID(rc); got != tc.want {
			t.Fatalf("source %s: system tenant = %q, want %q", tc.source, got, tc.want)
		}
	}
}

// Without a saved system tenant, doctor --deep resolves the bootstrap system
// tenant alias through Quartermaster instead of leaving the control plane
// unverified; a failed resolution is a warning, not a pass.
func TestDoctorResolveSystemTenantIDFromBootstrapAlias(t *testing.T) {
	calls := 0
	id, warning := doctorResolveSystemTenantID("", func() (string, error) {
		calls++
		return "11111111-2222-3333-4444-555555555555", nil
	})
	if warning != nil || id != "11111111-2222-3333-4444-555555555555" || calls != 1 {
		t.Fatalf("resolved = %q, %+v after %d calls", id, warning, calls)
	}

	id, warning = doctorResolveSystemTenantID("saved", func() (string, error) {
		t.Fatal("saved system tenant must not dial Quartermaster")
		return "", nil
	})
	if warning != nil || id != "saved" {
		t.Fatalf("saved = %q, %+v", id, warning)
	}

	for name, resolve := range map[string]func() (string, error){
		"rpc error":  func() (string, error) { return "", errors.New("unavailable") },
		"empty UUID": func() (string, error) { return " ", nil },
	} {
		_, warning = doctorResolveSystemTenantID("", resolve)
		if warning == nil || !strings.Contains(warning.Detail, `alias "frameworks"`) {
			t.Fatalf("%s: warning = %+v, want an unresolved system tenant warning", name, warning)
		}
		report := readiness.Report{Checked: true, Warnings: []readiness.Warning{*warning}}
		if report.OK() || doctorControlPlaneDetail(report, true) != "1 warning" {
			t.Fatalf("%s: report OK=%v detail=%q", name, report.OK(), doctorControlPlaneDetail(report, true))
		}
	}
}
