package cmd

import (
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
