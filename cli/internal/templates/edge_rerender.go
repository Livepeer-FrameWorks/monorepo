package templates

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// EdgeDeployedStack is what an operator-local container stack (rendered by
// `edge init`) holds on disk. VMAgentConfig and TelemetryToken are empty when
// telemetry is off.
type EdgeDeployedStack struct {
	Env            string // .edge.env
	Compose        string // docker-compose.edge.yml
	VMAgentConfig  string // vmagent-edge.yml
	TelemetryToken string // telemetry/token
}

// edgeOperatorEnvKeys are .edge.env keys the renderer writes a default for
// but that belong to the operator or the running node once deployed: the
// restream policy the template tells operators to edit, and the identity
// rotation flag Helmsman clears after Foghorn accepts it. A re-render keeps
// their deployed values.
var edgeOperatorEnvKeys = []string{
	"HELMSMAN_ROTATE_NODE_IDENTITY",
	"RESTREAM_ALLOW_PRIVATE_DESTINATIONS",
	"RESTREAM_ALLOWED_PRIVATE_CIDRS",
	"RESTREAM_DENIED_CIDRS",
}

var (
	edgeComposeImagePattern  = regexp.MustCompile(`(?m)^    image: *"?([^"\s]+)"?\s*$`)
	edgeVMAgentLabelPattern  = regexp.MustCompile(`(?m)^    (cluster|region): *"([^"]*)"\s*$`)
	edgeEnvAssignmentPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
)

// RerenderEdgeStack renders the compose file and .edge.env of a deployed
// operator-local container stack from this CLI's templates. Deployment
// inputs are recovered from the deployed files: node identity, endpoints and
// settings from .edge.env, the pinned edge image and compose flavor from the
// compose file, telemetry labels from the vmagent config. Everything else
// the renderer owns is refreshed; operator-owned keys and keys the template
// does not render keep their deployed values. The write-once enrollment and
// secrets env files are not part of the result.
func RerenderEdgeStack(d EdgeDeployedStack) (compose, env string, err error) {
	deployed := parseEdgeEnv(d.Env)
	if mode := NormalizeEdgeMode(deployed.value("DEPLOY_MODE")); mode != "container" {
		return "", "", fmt.Errorf("edge stack is %s mode; only container stacks render a compose file", mode)
	}
	vars := EdgeVars{
		NodeID:               deployed.value("NODE_ID"),
		EdgeDomain:           deployed.value("EDGE_DOMAIN"),
		AcmeEmail:            deployed.value("ACME_EMAIL"),
		FoghornGRPCAddr:      deployed.value("FOGHORN_CONTROL_ADDR"),
		GRPCTLSCAPath:        deployed.value("GRPC_TLS_CA_PATH"),
		Mode:                 "container",
		ONNXProfile:          deployed.value("MIST_ONNX_PROFILE"),
		TelemetryURL:         deployed.value("TELEMETRY_URL"),
		TelemetryToken:       strings.TrimSpace(d.TelemetryToken),
		RelayTrustedCIDR:     deployed.value("HELMSMAN_RELAY_TRUSTED_CIDR"),
		StorageCapacityBytes: deployed.value("HELMSMAN_STORAGE_CAPACITY_BYTES"),
		ONNXDRIDevice:        strings.Contains(d.Compose, "/dev/dri:/dev/dri"),
	}
	if vars.NodeID == "" {
		return "", "", fmt.Errorf(".edge.env has no NODE_ID; not an initialized edge stack")
	}
	if m := edgeComposeImagePattern.FindStringSubmatch(d.Compose); m != nil {
		vars.EdgeImage = m[1]
	}
	// The darwin flavor publishes ports (Docker Desktop host networking
	// breaks UDP); linux uses host networking.
	vars.EdgeOS = "linux"
	if strings.Contains(d.Compose, "\n    ports:\n") {
		vars.EdgeOS = "darwin"
	}
	for _, m := range edgeVMAgentLabelPattern.FindAllStringSubmatch(d.VMAgentConfig, -1) {
		switch m[1] {
		case "cluster":
			vars.ClusterID = m[2]
		case "region":
			vars.Region = m[2]
		}
	}

	files, err := RenderEdgeTemplates(vars)
	if err != nil {
		return "", "", err
	}
	for _, f := range files {
		switch f.Path {
		case "docker-compose.edge.yml":
			compose = string(f.Content)
		case ".edge.env":
			env = string(f.Content)
		}
	}
	return compose, mergeDeployedEdgeEnv(env, deployed), nil
}

type edgeEnvFile struct {
	order  []string
	values map[string]string
}

func (e edgeEnvFile) value(key string) string {
	return strings.TrimSpace(e.values[key])
}

func parseEdgeEnv(content string) edgeEnvFile {
	env := edgeEnvFile{values: map[string]string{}}
	for line := range strings.SplitSeq(content, "\n") {
		m := edgeEnvAssignmentPattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		if _, seen := env.values[m[1]]; !seen {
			env.order = append(env.order, m[1])
		}
		env.values[m[1]] = m[2]
	}
	return env
}

// mergeDeployedEdgeEnv puts deployed values back into a freshly rendered
// .edge.env for operator-owned keys, and appends deployed keys the template
// does not render at all.
func mergeDeployedEdgeEnv(rendered string, deployed edgeEnvFile) string {
	renderedKeys := parseEdgeEnv(rendered)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		m := edgeEnvAssignmentPattern.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil || !slices.Contains(edgeOperatorEnvKeys, m[1]) {
			continue
		}
		if value, ok := deployed.values[m[1]]; ok {
			lines[i] = m[1] + "=" + value
		}
	}
	out := strings.Join(lines, "\n")
	var extra []string
	for _, key := range deployed.order {
		if _, ok := renderedKeys.values[key]; !ok {
			extra = append(extra, key+"="+deployed.values[key])
		}
	}
	if len(extra) > 0 {
		out = strings.TrimRight(out, "\n") + "\n\n# Kept from the previous .edge.env (not rendered by the edge templates)\n" + strings.Join(extra, "\n") + "\n"
	}
	return out
}
