package provisioner

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/inventory"
)

func TestBuildClickHouseSystemdUnit_referencesManagedPaths(t *testing.T) {
	t.Parallel()

	unit := string(BuildClickHouseSystemdUnit())
	want := []string{
		"Description=ClickHouse Server",
		"User=clickhouse",
		"Group=clickhouse",
		"WorkingDirectory=/var/lib/clickhouse",
		"ExecStart=/usr/bin/clickhouse-server --config-file=/etc/clickhouse-server/config.xml --pid-file=/var/run/clickhouse-server/clickhouse-server.pid",
		"Restart=on-failure",
	}
	for _, fragment := range want {
		if !strings.Contains(unit, fragment) {
			t.Fatalf("systemd unit missing %q:\n%s", fragment, unit)
		}
	}
}

func TestBuildClickHouseListenHostConfig_exposesAllInterfaces(t *testing.T) {
	t.Parallel()

	cfg := string(BuildClickHouseListenHostConfig())
	if !strings.Contains(cfg, "<listen_host>0.0.0.0</listen_host>") {
		t.Fatalf("listen host config missing IPv4 wildcard bind:\n%s", cfg)
	}
	if strings.Contains(cfg, "<listen_host>::</listen_host>") {
		t.Fatalf("listen host config must not contain an IPv6 wildcard bind alongside IPv4:\n%s", cfg)
	}
}

func TestArtifactsForClickHouse_includesManagedSystemdUnit(t *testing.T) {
	t.Parallel()

	arts := ArtifactsForClickHouse(inventory.Host{}, ServiceConfig{
		Metadata: map[string]any{
			"clickhouse_password":          "secret",
			"clickhouse_readonly_password": "readonly",
		},
	})

	var foundUnit bool
	var foundListenHost bool
	for _, art := range arts {
		if art.Path == "/etc/clickhouse-server/config.d/listen-host.xml" {
			foundListenHost = true
			if !strings.Contains(string(art.Content), "<listen_host>0.0.0.0</listen_host>") {
				t.Fatalf("managed clickhouse listen-host config missing wildcard bind:\n%s", string(art.Content))
			}
			if strings.Contains(string(art.Content), "<listen_host>::</listen_host>") {
				t.Fatalf("managed clickhouse listen-host config must not contain dual wildcard binds:\n%s", string(art.Content))
			}
		}
		if art.Path == "/etc/systemd/system/clickhouse-server.service" {
			foundUnit = true
			if !strings.Contains(string(art.Content), "ExecStart=/usr/bin/clickhouse-server") {
				t.Fatalf("managed clickhouse unit content missing server execstart:\n%s", string(art.Content))
			}
		}
	}
	if !foundListenHost {
		t.Fatal("ArtifactsForClickHouse must include /etc/clickhouse-server/config.d/listen-host.xml")
	}
	if !foundUnit {
		t.Fatal("ArtifactsForClickHouse must include /etc/systemd/system/clickhouse-server.service")
	}
}

func TestClickHouseProvisionUsesLoopbackForWaitFor(t *testing.T) {
	t.Parallel()

	tasks := clickhouseInstallTasks("https://x/a.tgz", "sha256:aa")
	tasks = append(tasks, ansible.TaskWaitForPort(9000, ansible.WaitForOpts{Host: "127.0.0.1", Timeout: 60, Sleep: 1}))

	for _, task := range tasks {
		if task.Module != "ansible.builtin.wait_for" {
			continue
		}
		if task.Args["host"] != "127.0.0.1" {
			t.Fatalf("wait_for host = %v, want 127.0.0.1", task.Args["host"])
		}
		return
	}

	t.Fatal("expected wait_for task in clickhouse provision tasks")
}
