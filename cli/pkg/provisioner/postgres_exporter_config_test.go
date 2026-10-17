package provisioner

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// postgres_exporter 0.20.1 opens ./postgres_exporter.yml unless told
// otherwise, and the unit runs it from /, so every start logged
// `level=WARN msg="Error loading config" ... no such file or directory`. An
// empty file is a parse error ("EOF"); `auth_modules: {}` loads silently. The
// role renders that config and the unit points the exporter at it.
func TestPostgresExporterLoadsAnExplicitConfig(t *testing.T) {
	roleVars := readRepoFile(t, testRolesDir+"postgres/vars/main.yml")
	if !strings.Contains(roleVars, "postgres_exporter_config_path: /etc/postgres_exporter.yml") {
		t.Fatalf("postgres role must declare postgres_exporter_config_path:\n%s", roleVars)
	}
	unit := readRepoFile(t, testRolesDir+"postgres/templates/exporter.service.j2")
	if !strings.Contains(unit, "--config.file={{ postgres_exporter_config_path }}") {
		t.Fatalf("postgres exporter unit must pass --config.file:\n%s", unit)
	}

	var tasks []map[string]any
	if err := yaml.Unmarshal([]byte(readRepoFile(t, testRolesDir+"postgres/tasks/exporter.yml")), &tasks); err != nil {
		t.Fatal(err)
	}
	var content string
	for _, task := range tasks {
		copyTask, ok := task["ansible.builtin.copy"].(map[string]any)
		if ok && copyTask["dest"] == "{{ postgres_exporter_config_path }}" {
			content, _ = copyTask["content"].(string)
		}
	}
	if content == "" {
		t.Fatal("postgres exporter tasks do not render postgres_exporter_config_path")
	}
	var config map[string]any
	if err := yaml.Unmarshal([]byte(content), &config); err != nil || config == nil {
		t.Fatalf("rendered exporter config %q must be a non-empty YAML mapping: %v", content, err)
	}

	cleanup := readRepoFile(t, testRolesDir+"postgres/tasks/cleanup.yml")
	if !strings.Contains(cleanup, `- "{{ postgres_exporter_config_path }}"`) {
		t.Fatalf("postgres cleanup must remove the exporter config:\n%s", cleanup)
	}
}
