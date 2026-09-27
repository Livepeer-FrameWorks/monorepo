package provisioner

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"frameworks/cli/internal/composelog"
	"frameworks/cli/internal/services"
	"frameworks/cli/internal/templates"

	"gopkg.in/yaml.v3"
)

// assertComposeServicesCapLogs parses a rendered Compose file and requires
// every service to carry the shared log policy.
func assertComposeServicesCapLogs(t *testing.T, name, content string) {
	t.Helper()
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("%s: parse compose: %v\n%s", name, err, content)
	}
	if len(doc.Services) == 0 {
		t.Fatalf("%s: no services rendered:\n%s", name, content)
	}
	for svc, spec := range doc.Services {
		if got := spec["logging"]; !reflect.DeepEqual(got, composelog.Map()) {
			t.Errorf("%s: service %s logging = %#v, want %#v", name, svc, got, composelog.Map())
		}
	}
}

func TestComposeRenderersCapContainerLogs(t *testing.T) {
	t.Run("reverse proxy", func(t *testing.T) {
		for _, svc := range []string{"caddy", "nginx"} {
			content := reverseProxyComposeContent(svc, "image:1", 18090, 80, 443, map[string]string{"a.conf": "/etc/a.conf"}, nil)
			assertComposeServicesCapLogs(t, svc, content)
		}
	})

	t.Run("operator edge templates", func(t *testing.T) {
		for _, edgeOS := range []string{"linux", "darwin"} {
			files, err := templates.RenderEdgeTemplates(templates.EdgeVars{
				NodeID:          "n1",
				EdgeDomain:      "edge.example.test",
				SiteAddress:     "edge.example.test",
				FoghornGRPCAddr: "foghorn.example.test:18008",
				EnrollmentToken: "token",
				Mode:            "container",
				EdgeOS:          edgeOS,
				EdgeImage:       "edge:1",
				TelemetryURL:    "https://telemetry.example.test/api/v1/write",
				TelemetryToken:  "telemetry-token",
			})
			if err != nil {
				t.Fatalf("render edge templates (%s): %v", edgeOS, err)
			}
			found := false
			for _, f := range files {
				if f.Path == "docker-compose.edge.yml" {
					found = true
					assertComposeServicesCapLogs(t, "edge "+edgeOS, string(f.Content))
				}
			}
			if !found {
				t.Fatalf("edge templates (%s) rendered no compose file", edgeOS)
			}
		}
	})

	t.Run("service fragments", func(t *testing.T) {
		dir := t.TempDir()
		if err := services.GenerateFragments(dir, []services.ServiceSpec{{Name: "quartermaster", Image: "qm:1", Role: "control"}}, true); err != nil {
			t.Fatalf("generate fragments: %v", err)
		}
		content, err := os.ReadFile(filepath.Join(dir, "svc-quartermaster.yml"))
		if err != nil {
			t.Fatalf("read fragment: %v", err)
		}
		assertComposeServicesCapLogs(t, "service fragment", string(content))
	})

	// The Jinja templates are not rendered here; each declared container must
	// be followed by the same literal logging stanza the Go renderers emit.
	t.Run("ansible templates", func(t *testing.T) {
		block := composelog.Block("    ")
		for _, path := range []string{
			"roles/compose_stack/templates/generic-compose.yml.j2",
			"roles/chatwoot/templates/compose.yml.j2",
			"roles/listmonk/templates/compose.yml.j2",
			"roles/edge/templates/compose.yml.j2",
		} {
			content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/"+path)
			containers := strings.Count(content, "    container_name: ")
			if containers == 0 {
				t.Fatalf("%s declares no containers", path)
			}
			if got := strings.Count(content, block); got != containers {
				t.Errorf("%s: %d logging stanzas for %d containers; every service needs:\n%s", path, got, containers, block)
			}
		}
	})

	t.Run("docker daemon default", func(t *testing.T) {
		var vars struct {
			Policy struct {
				Driver string            `yaml:"log-driver"`
				Opts   map[string]string `yaml:"log-opts"`
			} `yaml:"compose_stack_docker_log_policy"`
		}
		content := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/compose_stack/vars/main.yml")
		if err := yaml.Unmarshal([]byte(content), &vars); err != nil {
			t.Fatalf("parse compose_stack vars: %v", err)
		}
		want := map[string]string{"max-size": composelog.MaxSize, "max-file": composelog.MaxFile}
		if vars.Policy.Driver != composelog.Driver || !reflect.DeepEqual(vars.Policy.Opts, want) {
			t.Fatalf("daemon log policy = %+v, want driver %s opts %v", vars.Policy, composelog.Driver, want)
		}
		tasks := readRepoFile(t, "ansible/collections/ansible_collections/frameworks/infra/roles/compose_stack/tasks/install.yml")
		for _, want := range []string{"dest: /etc/docker/daemon.json", "combine(compose_stack_docker_log_policy)", "compose_stack_daemon_json is changed"} {
			if !strings.Contains(tasks, want) {
				t.Errorf("compose_stack install.yml missing %q", want)
			}
		}
	})
}
