package templates

import (
	"bufio"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"frameworks/cli/internal/configschema"

	"gopkg.in/yaml.v3"
)

// edgeImageEnv mirrors the ENV instruction of edge/Dockerfile, with build
// arguments resolved to their defaults. TestEdgeImageEnvMatchesDockerfile
// pins it to the Dockerfile.
var edgeImageEnv = map[string]string{
	"DEPLOY_MODE":                      "container",
	"MIST_ONNX_PROFILE":                "cpu",
	"HELMSMAN_SUPERVISOR":              "s6",
	"CADDY_ADMIN_SOCKET":               "/run/caddy/admin.sock",
	"CADDY_CONFIG_PATH":                "/etc/caddy/Caddyfile",
	"CADDY_TLS_GROUP":                  "caddy",
	"MISTSERVER_URL":                   "http://localhost:4242",
	"MISTSERVER_HTTP_URL":              "http://localhost:8080",
	"HELMSMAN_WEBHOOK_URL":             "http://localhost:18007",
	"HELMSMAN_STORAGE_LOCAL_PATH":      "/data/storage",
	"HELMSMAN_STATE_DIR":               "/data/state",
	"S6_BEHAVIOUR_IF_STAGE2_FAILS":     "2",
	"S6_KEEP_ENV":                      "1",
	"S6_CMD_WAIT_FOR_SERVICES_MAXTIME": "0",
}

// edgeDomainPattern is a DNS name or IPv4 address: dot-separated labels of
// letters, digits, and inner hyphens. The Ansible edge and Helmsman roles
// assert the same pattern (TestAnsibleRolesValidateEdgeDomain).
var edgeDomainPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)

// EdgeImageEnv returns the environment the edge image sets for every process
// in the container.
func EdgeImageEnv() map[string]string {
	return maps.Clone(edgeImageEnv)
}

// validateEdgeHelmsmanEnv checks that Helmsman in the rendered edge stack
// receives every variable its schema requires. Container mode layers the
// image env, the compose env_file entries in order, and the compose
// environment list, as Docker Compose does; a blank value in a later layer
// unsets an earlier one. Native mode renders only .edge.env, and the native
// Helmsman role supplies and asserts the rest, so there only the required
// keys that .edge.env declares must be non-blank.
func validateEdgeHelmsmanEnv(mode string, files []EdgeRenderedFile) error {
	schema, err := configschema.Load()
	if err != nil {
		return err
	}
	required := schema.RequiredEnv("helmsman")
	if len(required) == 0 {
		return fmt.Errorf("edge: config schema declares no required Helmsman env")
	}

	var missing []string
	var env map[string]string
	if mode == "native" {
		env = parseEdgeEnvFile(renderedFileContent(files, ".edge.env"))
		for _, key := range schema.Missing("helmsman", env) {
			if _, ok := env[key]; ok {
				missing = append(missing, key)
			}
		}
	} else {
		var compose struct {
			Services map[string]struct {
				EnvFile     []string `yaml:"env_file"`
				Environment []string `yaml:"environment"`
			} `yaml:"services"`
		}
		if err := yaml.Unmarshal([]byte(renderedFileContent(files, "docker-compose.edge.yml")), &compose); err != nil {
			return fmt.Errorf("edge: parse rendered compose file: %w", err)
		}
		edge, ok := compose.Services["edge"]
		if !ok {
			return fmt.Errorf("edge: rendered compose file has no edge service")
		}
		env = EdgeImageEnv()
		for _, name := range edge.EnvFile {
			maps.Copy(env, parseEdgeEnvFile(renderedFileContent(files, name)))
		}
		for _, entry := range edge.Environment {
			key, value, _ := strings.Cut(entry, "=")
			env[strings.TrimSpace(key)] = value
		}
		missing = schema.Missing("helmsman", env)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("edge: rendered %s env leaves Helmsman required variable(s) unset: %s", mode, strings.Join(missing, ", "))
	}
	if value, ok := env["EDGE_PUBLIC_URL"]; ok {
		return validateEdgePublicURL(value)
	}
	return nil
}

// validateEdgePublicURL checks the domain inside the rendered
// https://<edge domain>/view. The URL is never blank, because the template
// supplies the scheme and path, so presence alone proves nothing.
func validateEdgePublicURL(value string) error {
	domain, ok := strings.CutPrefix(value, "https://")
	if ok {
		domain, ok = strings.CutSuffix(domain, "/view")
	}
	if !ok || !edgeDomainPattern.MatchString(domain) {
		return fmt.Errorf("edge: EDGE_PUBLIC_URL %q must be https://<edge domain>/view with a DNS name or IPv4 address as the edge domain", value)
	}
	return nil
}

func renderedFileContent(files []EdgeRenderedFile, path string) string {
	for _, f := range files {
		if f.Path == path {
			return string(f.Content)
		}
	}
	return ""
}

// parseEdgeEnvFile reads KEY=VALUE lines the way Docker Compose env_file
// does for the files this package renders: comments and blank lines are
// skipped and the value is everything after the first '='.
func parseEdgeEnvFile(content string) map[string]string {
	env := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		env[strings.TrimSpace(key)] = value
	}
	return env
}
