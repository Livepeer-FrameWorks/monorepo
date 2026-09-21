package templates

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/internal/configschema"

	"gopkg.in/yaml.v3"
)

// dockerfileEnv parses the ENV instructions of a Dockerfile, joining line
// continuations and resolving ${ARG} references to ARG defaults.
func dockerfileEnv(t *testing.T, content string) map[string]string {
	t.Helper()
	var instructions []string
	var current strings.Builder
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if current.Len() == 0 && (line == "" || strings.HasPrefix(line, "#")) {
			continue
		}
		if cont, ok := strings.CutSuffix(line, "\\"); ok {
			current.WriteString(cont)
			current.WriteString(" ")
			continue
		}
		current.WriteString(line)
		instructions = append(instructions, current.String())
		current.Reset()
	}

	args := map[string]string{}
	env := map[string]string{}
	ref := regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	for _, instruction := range instructions {
		keyword, rest, _ := strings.Cut(instruction, " ")
		switch strings.ToUpper(keyword) {
		case "ARG":
			name, value, _ := strings.Cut(strings.TrimSpace(rest), "=")
			args[name] = value
		case "ENV":
			for _, field := range strings.Fields(rest) {
				key, value, ok := strings.Cut(field, "=")
				if !ok {
					t.Fatalf("ENV entry %q is not KEY=VALUE", field)
				}
				env[key] = ref.ReplaceAllStringFunc(value, func(m string) string {
					return args[ref.FindStringSubmatch(m)[1]]
				})
			}
		}
	}
	return env
}

func TestEdgeImageEnvMatchesDockerfile(t *testing.T) {
	t.Parallel()
	got := dockerfileEnv(t, readFile(t, repositoryPath(t, "edge/Dockerfile")))
	if len(got) == 0 {
		t.Fatal("edge/Dockerfile declares no ENV")
	}
	if !maps.Equal(got, EdgeImageEnv()) {
		t.Fatalf("EdgeImageEnv drifted from edge/Dockerfile ENV:\n  dockerfile %v\n  go         %v", got, EdgeImageEnv())
	}
}

func TestRenderEdgeTemplatesContainerWithoutNodeIDFails(t *testing.T) {
	t.Parallel()
	vars := fixedEdgeVars()
	vars.Mode = "container"
	vars.NodeID = ""

	_, err := RenderEdgeTemplates(vars)
	if err == nil || !strings.Contains(err.Error(), "NODE_ID") {
		t.Fatalf("render without NODE_ID: err = %v, want failure naming NODE_ID", err)
	}
}

func TestRenderEdgeTemplatesNativeWithoutNodeIDFails(t *testing.T) {
	t.Parallel()
	vars := fixedEdgeVars()
	vars.Mode = "native"
	vars.NodeID = "  "

	_, err := RenderEdgeTemplates(vars)
	if err == nil || !strings.Contains(err.Error(), "NODE_ID") {
		t.Fatalf("native render without NODE_ID: err = %v, want failure naming NODE_ID", err)
	}
}

func TestRenderEdgeTemplatesContainerWithoutFoghornAddrFails(t *testing.T) {
	t.Parallel()
	vars := fixedEdgeVars()
	vars.FoghornGRPCAddr = ""

	_, err := RenderEdgeTemplates(vars)
	if err == nil || !strings.Contains(err.Error(), "FOGHORN_CONTROL_ADDR") || strings.Contains(err.Error(), "NODE_ID") {
		t.Fatalf("render without Foghorn address: err = %v, want failure naming only FOGHORN_CONTROL_ADDR", err)
	}
}

// EDGE_PUBLIC_URL is rendered as https://<domain>/view, so a presence check
// alone passes with no domain. Both modes validate the domain component.
func TestRenderEdgeTemplatesRejectsInvalidEdgeDomain(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"container", "native"} {
		for _, domain := range []string{"", "  ", "https://edge.example.com", "edge.example.com/view", "edge example.com", "-edge.example.com"} {
			vars := fixedEdgeVars()
			vars.Mode = mode
			vars.EdgeDomain = domain
			_, err := RenderEdgeTemplates(vars)
			if err == nil || !strings.Contains(err.Error(), "EDGE_PUBLIC_URL") {
				t.Errorf("%s render with domain %q: err = %v, want failure naming EDGE_PUBLIC_URL", mode, domain, err)
			}
		}
		vars := fixedEdgeVars()
		vars.Mode = mode
		if _, err := RenderEdgeTemplates(vars); err != nil {
			t.Errorf("%s render with domain %q: %v", mode, vars.EdgeDomain, err)
		}
	}
}

// The Ansible edge container path and the Helmsman role validate the edge
// domain with the Go pattern before anything starts, since their
// EDGE_PUBLIC_URL is built from it and is never blank.
func TestAnsibleRolesValidateEdgeDomain(t *testing.T) {
	t.Parallel()
	pattern := "'" + edgeDomainPattern.String() + "'"
	container := readFile(t, ansibleEdgeRolePath(t, "tasks/install-container.yml"))
	domainAt := strings.Index(container, "edge_domain | default('', true) | trim is match("+pattern+")")
	composeAt := strings.Index(container, "frameworks.infra.compose_stack")
	if domainAt < 0 || domainAt > composeAt {
		t.Fatalf("edge install-container.yml must assert edge_domain matches %s before compose up", pattern)
	}
	configure := readFile(t, repositoryPath(t, "ansible/collections/ansible_collections/frameworks/infra/roles/helmsman/tasks/configure.yml"))
	domainAt = strings.Index(configure, "helmsman_edge_domain | default('', true) | trim is match("+pattern+")")
	writeAt := strings.Index(configure, "configure-linux.yml")
	if domainAt < 0 || domainAt > writeAt {
		t.Fatalf("helmsman configure.yml must assert helmsman_edge_domain matches %s before writing the env file", pattern)
	}
}

// The rendered env files do not carry MISTSERVER_URL or HELMSMAN_STATE_DIR;
// a container render passes only because the image env supplies them, and a
// blank later layer must still count as unset.
func TestValidateEdgeHelmsmanEnvLayersImageEnvUnderRenderedFiles(t *testing.T) {
	t.Parallel()
	files, err := RenderEdgeTemplates(fixedEdgeVars())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	envFile := parseEdgeEnvFile(renderedFileContent(files, ".edge.env"))
	for _, key := range []string{"MISTSERVER_URL", "HELMSMAN_STATE_DIR"} {
		if _, ok := envFile[key]; ok {
			t.Fatalf(".edge.env renders %s; this test needs a key only the image env supplies", key)
		}
	}

	for i := range files {
		if files[i].Path == ".edge-secrets.env" {
			files[i].Content = append(files[i].Content, []byte("HELMSMAN_STATE_DIR= \n")...)
		}
	}
	err = validateEdgeHelmsmanEnv("container", files)
	if err == nil || !strings.Contains(err.Error(), "HELMSMAN_STATE_DIR") {
		t.Fatalf("blank HELMSMAN_STATE_DIR in a later env_file: err = %v, want failure naming it", err)
	}
}

// The Ansible roles carry the same contract lists as defaults for runs that
// do not come from the CLI; the CLI passes both explicitly.
func TestAnsibleRoleEnvContractDefaultsMatchGo(t *testing.T) {
	t.Parallel()
	schema, err := configschema.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var helmsmanDefaults struct {
		RequiredEnv []string `yaml:"helmsman_required_env"`
	}
	helmsmanPath := repositoryPath(t, "ansible/collections/ansible_collections/frameworks/infra/roles/helmsman/defaults/main.yml")
	if err := yaml.Unmarshal([]byte(readFile(t, helmsmanPath)), &helmsmanDefaults); err != nil {
		t.Fatalf("parse helmsman defaults: %v", err)
	}
	if want := schema.RequiredEnv("helmsman"); len(want) == 0 || !slices.Equal(helmsmanDefaults.RequiredEnv, want) {
		t.Fatalf("helmsman_required_env = %v, want schema %v", helmsmanDefaults.RequiredEnv, want)
	}

	var edgeDefaults struct {
		RequiredEnv []string          `yaml:"edge_helmsman_required_env"`
		ImageEnv    map[string]string `yaml:"edge_image_env"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, ansibleEdgeRolePath(t, "defaults/main.yml"))), &edgeDefaults); err != nil {
		t.Fatalf("parse edge defaults: %v", err)
	}
	if !slices.Equal(edgeDefaults.RequiredEnv, schema.RequiredEnv("helmsman")) {
		t.Fatalf("edge role edge_helmsman_required_env = %v, want schema %v", edgeDefaults.RequiredEnv, schema.RequiredEnv("helmsman"))
	}
	if !maps.Equal(edgeDefaults.ImageEnv, EdgeImageEnv()) {
		t.Fatalf("edge role edge_image_env = %v, want %v", edgeDefaults.ImageEnv, EdgeImageEnv())
	}
}

// Both Ansible render paths assert the Helmsman contract before they start
// anything: the container path before compose up, the native path before the
// Helmsman env file is written.
func TestAnsibleRolesAssertHelmsmanEnvBeforeStart(t *testing.T) {
	t.Parallel()
	container := readFile(t, ansibleEdgeRolePath(t, "tasks/install-container.yml"))
	assertAt := strings.Index(container, "loop: \"{{ edge_helmsman_required_env }}\"")
	composeAt := strings.Index(container, "frameworks.infra.compose_stack")
	envFileAt := strings.Index(container, "src: edge.env.j2")
	if assertAt < 0 || composeAt < 0 || envFileAt < 0 || assertAt > composeAt || assertAt > envFileAt {
		t.Fatalf("edge install-container.yml must assert edge_helmsman_required_env before rendering .edge.env and before compose up")
	}
	for _, path := range []string{"tasks/install-native-linux.yml", "tasks/install-native-darwin.yml"} {
		if !strings.Contains(readFile(t, ansibleEdgeRolePath(t, path)), "helmsman_required_env: \"{{ edge_helmsman_required_env }}\"") {
			t.Fatalf("edge %s must hand edge_helmsman_required_env to the Helmsman configure tasks", path)
		}
	}

	configure := readFile(t, repositoryPath(t, "ansible/collections/ansible_collections/frameworks/infra/roles/helmsman/tasks/configure.yml"))
	buildAt := strings.Index(configure, "helmsman_env_full:")
	requiredAt := strings.Index(configure, "loop: \"{{ helmsman_required_env }}\"")
	writeAt := strings.Index(configure, "configure-linux.yml")
	if buildAt < 0 || requiredAt < buildAt || writeAt < requiredAt {
		t.Fatalf("helmsman configure.yml must assert helmsman_required_env after building helmsman_env_full and before writing the env file")
	}
}
