package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestYugabyteRoleCatalogPreloadIsExplicit(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
			Metadata: map[string]any{"catalog_preload_additional_tables": enabled},
		}, mockPrivateerHelpers())
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := vars["yugabyte_catalog_preload_additional_tables"]; !ok || got != enabled {
			t.Fatalf("explicit catalog preload %v was not rendered: %v", enabled, got)
		}
	}
	vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{}, mockPrivateerHelpers())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vars["yugabyte_catalog_preload_additional_tables"]; ok {
		t.Fatal("omitted flag must preserve engine default")
	}
}

func TestYugabyteRoleVarsPassesDatabaseOwnerPassword(t *testing.T) {
	vars, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"postgres_password":         "shared-secret",
			"postgres_runtime_password": "runtime-default",
			"databases": []map[string]string{
				{"name": "foghorn_eu", "owner": "foghorn_eu", "password": "cluster-secret", "runtime_role": "foghorn_app", "runtime_password": "runtime-secret"},
			},
		},
	}, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("yugabyteRoleVars: %v", err)
	}
	dbs, ok := vars["yugabyte_databases"].([]map[string]any)
	if !ok || len(dbs) != 1 {
		t.Fatalf("yugabyte_databases = %#v, want one database", vars["yugabyte_databases"])
	}
	if got := dbs[0]["password"]; got != "cluster-secret" {
		t.Fatal("database owner password did not match metadata")
	}
	if got := dbs[0]["runtime_role"]; got != "foghorn_app" {
		t.Fatalf("runtime role = %v, want foghorn_app", got)
	}
	if got := dbs[0]["runtime_password"]; got != "runtime-secret" {
		t.Fatal("database runtime password did not match metadata")
	}
}

func TestYugabyteRoleVarsPassesRelayoutOperatorUser(t *testing.T) {
	host := nilHost()
	host.User = "mistserver"
	vars, err := yugabyteRoleVars(context.Background(), host, ServiceConfig{}, mockPrivateerHelpers())
	if err != nil {
		t.Fatalf("yugabyteRoleVars: %v", err)
	}
	if got := vars["yugabyte_relayout_operator_user"]; got != "mistserver" {
		t.Fatalf("yugabyte_relayout_operator_user = %v, want mistserver", got)
	}
}

func TestYugabyteRoleVarsRejectsRuntimeOwnerCollision(t *testing.T) {
	_, err := yugabyteRoleVars(context.Background(), nilHost(), ServiceConfig{
		Metadata: map[string]any{
			"databases": []map[string]string{{
				"name": "quartermaster", "owner": "quartermaster", "runtime_role": "quartermaster",
			}},
		},
	}, mockPrivateerHelpers())
	if err == nil || !strings.Contains(err.Error(), "runtime role \"quartermaster\" must differ from owner") {
		t.Fatalf("yugabyteRoleVars() error = %v, want owner/runtime collision", err)
	}
}

func TestYugabyteRoleUsesPerDatabasePasswords(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/init.yml")
	for _, want := range []string{
		`password: "{{ item.password | default(yugabyte_application_password) }}"`,
		`password: "{{ item.runtime_password | default(yugabyte_runtime_password) }}"`,
		`!= (item.password | default(yugabyte_application_password))`,
		`!= (item.owner | default(item.name, true))`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("yugabyte init should keep owner/runtime credentials independent; missing %q:\n%s", want, content)
		}
	}
}

func TestYugabyteRoleProvisionsRelayoutDirectories(t *testing.T) {
	content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/tasks/install.yml")
	for _, want := range []string{
		"Ensure private Yugabyte relayout directories",
		`owner: "{{ yugabyte_relayout_operator_user }}"`,
		`mode: "0700"`,
		`"{{ yugabyte_relayout_dump_dir }}"`,
		`"{{ yugabyte_relayout_worker_dir }}"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("yugabyte install should provision private relayout paths; missing %q:\n%s", want, content)
		}
	}
}

// glog and the YSQL postmaster never delete old files, and a host-wide
// core_pattern into the data disk bypasses systemd-coredump's caps for every
// process on the host.
func TestYugabyteRoleBoundsLogsAndCores(t *testing.T) {
	role := "ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/"
	for _, template := range []string{"templates/master.conf.j2", "templates/tserver.conf.j2"} {
		if content := readRepoFile(t, role+template); !strings.Contains(content, "\n--max_log_size=256\n") {
			t.Fatalf("%s must cap glog files at 256 MB:\n%s", template, content)
		}
	}
	tserver := readRepoFile(t, role+"templates/tserver.conf.j2")
	if !strings.Contains(tserver, "--ysql_pg_conf_csv=password_encryption=scram-sha-256,log_rotation_age=1d,log_rotation_size=256MB\n") {
		t.Fatalf("tserver.conf must rotate the YSQL log:\n%s", tserver)
	}
	if sysctl := readRepoFile(t, role+"templates/sysctl.conf.j2"); strings.Contains(sysctl, "core_pattern") {
		t.Fatalf("sysctl.conf must leave kernel.core_pattern to systemd-coredump:\n%s", sysctl)
	}
	prune := readRepoFile(t, role+"templates/yugabyte-log-prune.service.j2")
	for _, want := range []string{"{{ yugabyte_master_log_dir }}", "{{ yugabyte_tserver_log_dir }}", "{{ yugabyte_cores_dir }}", "-mtime +{{ yugabyte_log_retention_days }} -delete"} {
		if !strings.Contains(prune, want) {
			t.Fatalf("log prune unit missing %q:\n%s", want, prune)
		}
	}
	if timer := readRepoFile(t, role+"templates/yugabyte-log-prune.timer.j2"); !strings.Contains(timer, "OnCalendar=daily") {
		t.Fatalf("log prune must run daily:\n%s", timer)
	}
	if vars := readRepoFile(t, role+"vars/main.yml"); !strings.Contains(vars, "yugabyte_log_retention_days: 7\n") {
		t.Fatalf("logs must be kept 7 days:\n%s", vars)
	}
	configure := readRepoFile(t, role+"tasks/configure.yml")
	for _, want := range []string{"name: systemd-coredump", "name: yugabyte-log-prune.timer"} {
		if !strings.Contains(configure, want) {
			t.Fatalf("configure.yml missing %q", want)
		}
	}
}
