package provisioner

import (
	"fmt"
	"strings"

	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/artifacts"
	"frameworks/cli/pkg/inventory"
)

// BuildEdgeMistserverEnv returns the /etc/frameworks/mistserver.env (linux)
// or {confDir}/mistserver.env (darwin) content.
func BuildEdgeMistserverEnv() []byte {
	return []byte("# MistServer environment\nMIST_DEBUG=3\n")
}

// BuildEdgeHelmsmanEnv returns the helmsman env file content.
func BuildEdgeHelmsmanEnv(vars templates.EdgeVars, domain, mistPass string) []byte {
	lines := []string{
		"# Helmsman edge environment",
		fmt.Sprintf("NODE_ID=%s", vars.NodeID),
		fmt.Sprintf("EDGE_PUBLIC_URL=https://%s/view", domain),
		fmt.Sprintf("FOGHORN_CONTROL_ADDR=%s", vars.FoghornGRPCAddr),
		fmt.Sprintf("EDGE_ENROLLMENT_TOKEN=%s", vars.EnrollmentToken),
		fmt.Sprintf("EDGE_DOMAIN=%s", domain),
		fmt.Sprintf("ACME_EMAIL=%s", vars.AcmeEmail),
		fmt.Sprintf("DEPLOY_MODE=%s", vars.Mode),
		fmt.Sprintf("MISTSERVER_URL=http://%s", vars.MistUpstream),
		fmt.Sprintf("HELMSMAN_WEBHOOK_URL=http://%s", vars.HelmsmanUpstream),
		"CADDY_ADMIN_URL=http://localhost:2019",
		"MIST_API_USERNAME=frameworks",
		fmt.Sprintf("MIST_API_PASSWORD=%s", mistPass),
	}
	if vars.GRPCTLSCAPath != "" {
		lines = append(lines, fmt.Sprintf("GRPC_TLS_CA_PATH=%s", vars.GRPCTLSCAPath))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// BuildEdgeCaddyEnv returns the caddy env file content.
func BuildEdgeCaddyEnv(acmeEmail string) []byte {
	return fmt.Appendf(nil, "# Caddy edge environment\nCADDY_EMAIL=%s\n", acmeEmail)
}

// EdgeArtifactsForMode is the mode descriptor used by ArtifactsForEdge to
// decide which placed-set to produce. Paths vary per Linux native vs
// Darwin native vs Docker — callers pass a descriptor so the edge
// provider doesn't re-derive that knowledge.
type EdgeArtifactsForMode struct {
	Mode         string // "docker" | "native"
	RemoteOS     string // "linux" | "darwin"
	BaseDir      string // darwin only
	ConfDir      string // darwin only
	PlistDir     string // darwin only
	TelemetryOn  bool
	CABundleSet  bool
	Domain       string
	MistPassword string
}

// ArtifactsForEdge returns the files placed on the host for one edge deploy
// configuration. The placed set is NOT the full RenderEdgeTemplates output —
// docker mode never places maintenance.html, and native modes place per-
// service env files that aren't in the template set at all.
func ArtifactsForEdge(host inventory.Host, vars templates.EdgeVars, mode EdgeArtifactsForMode) ([]artifacts.DesiredArtifact, error) {
	rendered, err := templates.RenderEdgeTemplates(vars)
	if err != nil {
		return nil, err
	}
	renderedByPath := map[string][]byte{}
	for _, f := range rendered {
		renderedByPath[f.Path] = f.Content
	}

	var out []artifacts.DesiredArtifact

	switch {
	case mode.Mode == "docker":
		out = appendIf(out, "/opt/frameworks/edge/.edge.env", renderedByPath[".edge.env"], artifacts.KindEnv, edgeEnvIgnoreKeys())
		out = appendHash(out, "/opt/frameworks/edge/docker-compose.edge.yml", renderedByPath["docker-compose.edge.yml"])
		out = appendHash(out, "/opt/frameworks/edge/Caddyfile", renderedByPath["Caddyfile"])
		if mode.CABundleSet {
			out = appendHash(out, "/opt/frameworks/edge/pki/ca.crt", renderedByPath["pki/ca.crt"])
		}
		if mode.TelemetryOn {
			out = appendHash(out, "/etc/frameworks/telemetry/token", renderedByPath["telemetry/token"])
			out = appendHash(out, "/etc/frameworks/vmagent-edge.yml", renderedByPath["vmagent-edge.yml"])
		}

	case mode.Mode == "native" && mode.RemoteOS == "linux":
		out = appendIf(out, "/opt/frameworks/edge/.edge.env", renderedByPath[".edge.env"], artifacts.KindEnv, edgeEnvIgnoreKeys())
		out = appendEnv(out, "/etc/frameworks/mistserver.env", BuildEdgeMistserverEnv(), nil)
		out = appendEnv(out, "/etc/frameworks/helmsman.env", BuildEdgeHelmsmanEnv(vars, mode.Domain, mode.MistPassword), []string{"MIST_API_PASSWORD", "EDGE_ENROLLMENT_TOKEN"})
		out = appendEnv(out, "/etc/frameworks/caddy.env", BuildEdgeCaddyEnv(vars.AcmeEmail), nil)
		out = appendHash(out, "/etc/caddy/Caddyfile", renderedByPath["Caddyfile"])
		if mode.CABundleSet {
			out = appendHash(out, "/etc/frameworks/pki/ca.crt", renderedByPath["pki/ca.crt"])
		}
		if mode.TelemetryOn {
			out = appendHash(out, "/etc/frameworks/telemetry/token", renderedByPath["telemetry/token"])
			out = appendHash(out, "/etc/frameworks/vmagent-edge.yml", renderedByPath["vmagent-edge.yml"])
		}

	case mode.Mode == "native" && mode.RemoteOS == "darwin":
		out = appendIf(out, mode.BaseDir+"/edge/.edge.env", renderedByPath[".edge.env"], artifacts.KindEnv, edgeEnvIgnoreKeys())
		out = appendEnv(out, mode.ConfDir+"/mistserver.env", BuildEdgeMistserverEnv(), nil)
		out = appendEnv(out, mode.ConfDir+"/helmsman.env", BuildEdgeHelmsmanEnv(vars, mode.Domain, mode.MistPassword), []string{"MIST_API_PASSWORD", "EDGE_ENROLLMENT_TOKEN"})
		out = appendEnv(out, mode.ConfDir+"/caddy.env", BuildEdgeCaddyEnv(vars.AcmeEmail), nil)
		if mode.CABundleSet {
			out = appendHash(out, mode.ConfDir+"/pki/ca.crt", renderedByPath["pki/ca.crt"])
		}
		if mode.TelemetryOn {
			out = appendHash(out, mode.ConfDir+"/telemetry/token", renderedByPath["telemetry/token"])
			out = appendHash(out, mode.ConfDir+"/vmagent-edge.yml", renderedByPath["vmagent-edge.yml"])
		}
	}
	return out, nil
}

func appendIf(out []artifacts.DesiredArtifact, path string, content []byte, kind artifacts.ArtifactKind, ignoreKeys []string) []artifacts.DesiredArtifact {
	if content == nil {
		return out
	}
	return append(out, artifacts.DesiredArtifact{Path: path, Kind: kind, Content: content, IgnoreKeys: ignoreKeys})
}

func appendHash(out []artifacts.DesiredArtifact, path string, content []byte) []artifacts.DesiredArtifact {
	if content == nil {
		return out
	}
	return append(out, artifacts.DesiredArtifact{Path: path, Kind: artifacts.KindFileHash, Content: content})
}

func appendEnv(out []artifacts.DesiredArtifact, path string, content []byte, ignoreKeys []string) []artifacts.DesiredArtifact {
	return append(out, artifacts.DesiredArtifact{Path: path, Kind: artifacts.KindEnv, Content: content, IgnoreKeys: ignoreKeys})
}

// edgeEnvIgnoreKeys returns the .edge.env keys that are runtime-injected
// (single-use tokens, minted JWTs, host-discovered values). Drift skips
// these rather than reporting permanent false drift.
func edgeEnvIgnoreKeys() []string {
	return []string{"EDGE_ENROLLMENT_TOKEN"}
}
