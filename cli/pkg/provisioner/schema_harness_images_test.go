//go:build schema_verify

package provisioner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type harnessInfrastructure struct {
	Name           string `yaml:"name"`
	Image          string `yaml:"image"`
	Digest         string `yaml:"digest"`
	ContractImage  string `yaml:"contract_image"`
	ContractDigest string `yaml:"contract_digest"`
}

func infrastructureHarnessImage(t *testing.T, name string) string {
	t.Helper()
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found; real-engine tests require the release image authority")
	}

	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	var doc struct {
		Infrastructure []harnessInfrastructure `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", manifestPath, err)
	}

	for _, infra := range doc.Infrastructure {
		if infra.Name != name {
			continue
		}
		if strings.TrimSpace(infra.Image) == "" || strings.TrimSpace(infra.Digest) == "" {
			t.Fatalf("infrastructure/%s must declare both image and digest", name)
		}
		return infra.Image + "@" + infra.Digest
	}
	t.Fatalf("infrastructure/%s is absent from %s", name, manifestPath)
	return ""
}

func infrastructureContractImage(t *testing.T, name string) string {
	t.Helper()
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found; compatibility tests require the engine authority")
	}
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read %s: %v", manifestPath, err)
	}
	var doc struct {
		Infrastructure []harnessInfrastructure `yaml:"infrastructure"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", manifestPath, err)
	}
	for _, infra := range doc.Infrastructure {
		if infra.Name != name {
			continue
		}
		if infra.ContractImage == "" || infra.ContractDigest == "" {
			t.Fatalf("infrastructure/%s must pin contract_image and contract_digest", name)
		}
		return infra.ContractImage + "@" + infra.ContractDigest
	}
	t.Fatalf("infrastructure/%s is absent from %s", name, manifestPath)
	return ""
}

func TestComposeUsesSchemaHarnessImages(t *testing.T) {
	manifestPath := findInfrastructureYaml(t)
	if manifestPath == "" {
		t.Fatal("config/infrastructure.yaml not found")
	}
	composePath := filepath.Join(filepath.Dir(filepath.Dir(manifestPath)), "docker-compose.yml")
	b, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read %s: %v", composePath, err)
	}
	compose := string(b)
	for _, name := range []string{"postgresql", "clickhouse"} {
		image := infrastructureHarnessImage(t, name)
		if !strings.Contains(compose, "image: "+image) {
			t.Errorf("docker-compose.yml does not use release-authority image %q for %s", image, name)
		}
	}
}

func TestPostgresServiceDatabaseInitialization(t *testing.T) {
	requireDocker(t)
	const name = "fw-sv-pg-service-dbs"
	pgStart(t, name)

	manifestPath := findInfrastructureYaml(t)
	root := filepath.Dir(filepath.Dir(manifestPath))
	for source, destination := range map[string]string{
		filepath.Join(root, "pkg", "database", "sql", "schema"):          name + ":/frameworks-schema",
		filepath.Join(root, "pkg", "database", "sql", "seeds", "static"): name + ":/frameworks-static-seeds",
		filepath.Join(root, "infrastructure", "postgres"):                name + ":/frameworks-postgres",
	} {
		if _, err := docker(t, "", "cp", source, destination); err != nil {
			t.Fatalf("copy %s into database harness: %v", source, err)
		}
	}
	if out, err := docker(t, "", "exec",
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD=harness",
		"-e", "POSTGRES_DB=postgres",
		name, "sh", "/frameworks-postgres/init-service-databases.sh"); err != nil {
		t.Fatalf("initialize service databases: %v\n%s", err, out)
	}

	services := []string{"quartermaster", "purser", "foghorn", "commodore", "periscope", "navigator", "skipper"}
	for _, service := range services {
		out, err := docker(t, "", "exec", name, "psql", "-U", service, "-d", service, "-tAc",
			"SELECT current_user || '|' || count(*) FROM information_schema.schemata WHERE schema_name = current_user")
		if err != nil {
			t.Errorf("connect to %s as its runtime owner: %v", service, err)
			continue
		}
		if got := strings.TrimSpace(out); got != service+"|1" {
			t.Errorf("%s ownership probe = %q, want %q", service, got, service+"|1")
		}

		runtimeRole := service + "_runtime"
		privilegeProbe := fmt.Sprintf(
			"SELECT current_user || '|' || has_schema_privilege(current_user, '%s', 'CREATE') || '|' || bool_and(has_table_privilege(current_user, quote_ident(schemaname) || '.' || quote_ident(tablename), 'SELECT,INSERT,UPDATE,DELETE')) FROM pg_tables WHERE schemaname = '%s' GROUP BY current_user",
			service, service,
		)
		out, err = docker(t, "", "exec", name, "psql", "-U", runtimeRole, "-d", service, "-tAc", privilegeProbe)
		if err != nil {
			t.Errorf("connect to %s and inspect runtime privileges: %v", runtimeRole, err)
			continue
		}
		if got := strings.TrimSpace(out); got != runtimeRole+"|false|true" {
			t.Errorf("%s privilege probe = %q, want %q", runtimeRole, got, runtimeRole+"|false|true")
		}
		if out, err := docker(t, "", "exec", name, "psql", "-U", runtimeRole, "-d", service, "-v", "ON_ERROR_STOP=1", "-c",
			fmt.Sprintf("CREATE TABLE %s.runtime_role_must_not_create (id integer)", service)); err == nil {
			t.Errorf("%s unexpectedly created a table: %s", runtimeRole, out)
		}
	}
	if out, err := docker(t, "", "exec", name, "psql", "-U", "purser", "-d", "purser", "-v", "ON_ERROR_STOP=1", "-c",
		"CREATE TABLE purser.runtime_default_privilege_probe (id integer PRIMARY KEY)"); err != nil {
		t.Fatalf("create migration-owned default-privilege probe: %v\n%s", err, out)
	}
	if out, err := docker(t, "", "exec", name, "psql", "-U", "purser_runtime", "-d", "purser", "-v", "ON_ERROR_STOP=1", "-tAc",
		"INSERT INTO purser.runtime_default_privilege_probe (id) VALUES (1); SELECT id FROM purser.runtime_default_privilege_probe"); err != nil {
		t.Fatalf("runtime role cannot write/read an object created after grants: %v\n%s", err, out)
	} else if got := strings.TrimSpace(out); got != "INSERT 0 1\n1" {
		t.Fatalf("runtime default-privilege probe = %q, want INSERT 0 1 then 1", got)
	}

	out, err := docker(t, "", "exec", name, "psql", "-U", "purser", "-d", "purser", "-tAc",
		"SELECT count(*) FROM information_schema.schemata WHERE schema_name IN ('quartermaster','commodore','foghorn','periscope','navigator','skipper')")
	if err != nil {
		t.Fatalf("probe Purser database isolation: %v", err)
	}
	if got := strings.TrimSpace(out); got != "0" {
		t.Fatalf("Purser database contains %s foreign service schemas, want 0", got)
	}

	if out, err := docker(t, "", "exec", name, "psql", "-U", "purser", "-d", "quartermaster", "-tAc", "SELECT 1"); err == nil {
		t.Fatalf("Purser unexpectedly connected to Quartermaster database: %s", out)
	}
	if out, err := docker(t, "", "exec", name, "psql", "-U", "frameworks_analytics_ro", "-d", "quartermaster", "-tAc", "SELECT 1"); err != nil {
		t.Fatalf("approved analytics role cannot connect to Quartermaster: %v\n%s", err, out)
	}
}
