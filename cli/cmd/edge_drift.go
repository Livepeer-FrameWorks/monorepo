package cmd

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/ux"
	"frameworks/cli/internal/xexec"

	"github.com/spf13/cobra"
)

const edgeDriftEnvFile = ".edge.env"

var edgeDriftServices = []string{"caddy", "mistserver", "helmsman"}

const (
	driftStatusOK                 = "ok"
	driftStatusMissing            = "missing"
	driftStatusStopped            = "stopped"
	driftStatusWrongMode          = "wrong_mode"
	driftConfigPresent            = "present"
	driftConfigEmpty              = "empty"
	driftConfigDomainFlagMismatch = "domain_flag_mismatch"
	driftHealthOK                 = "ok"
	driftHealthMismatch           = "health_mismatch"
)

type edgeDriftServiceStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type edgeDriftConfigStatus struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type edgeDriftHealth struct {
	URL    string `json:"url"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type edgeDriftSummary struct {
	Total       int `json:"total"`
	Divergences int `json:"divergences"`
}

type edgeDriftReport struct {
	Node     string                   `json:"node,omitempty"`
	Domain   string                   `json:"domain,omitempty"`
	Mode     string                   `json:"mode"`
	Services []edgeDriftServiceStatus `json:"services"`
	Config   []edgeDriftConfigStatus  `json:"config"`
	Health   *edgeDriftHealth         `json:"health,omitempty"`
	Summary  edgeDriftSummary         `json:"summary"`
}

func newEdgeDriftCmd() *cobra.Command {
	var dir, domainFlag, sshTarget, sshKey string
	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Observed-state survey: services, .edge.env keys, HTTPS health",
		Long: `Survey the current edge node for divergence from its configured state.

Reports whether each edge service (caddy, mistserver, helmsman) is running in
the expected stack, whether required .edge.env keys are present, and whether
the HTTPS health endpoint is reachable. Probes both docker and native stacks
so a service running under the wrong manager surfaces as wrong_mode.

Role-level config+binary diff is owned by ` + "`edge provision --dry-run`" + `
(ansible-playbook --check --diff). ` + "`edge drift`" + ` is observed-state only.

Exits non-zero on any divergence, so CI can gate on it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if dir == "" {
				dir = "."
			}

			jsonMode := output == "json"
			textOut := cmd.OutOrStdout()
			if jsonMode {
				textOut = io.Discard
			}

			rep := runEdgeDrift(ctx, dir, domainFlag, sshTarget, sshKey)
			renderEdgeDriftText(textOut, rep)

			if jsonMode {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(rep); err != nil {
					return err
				}
			}

			if rep.Summary.Divergences > 0 {
				return &ExitCodeError{Code: 1, Message: fmt.Sprintf("%d divergence(s) detected", rep.Summary.Divergences)}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "directory containing .edge.env")
	cmd.Flags().StringVar(&domainFlag, "domain", "", "override EDGE_DOMAIN for HTTPS check")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "run remotely on user@host via SSH")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	return cmd
}

func runEdgeDrift(ctx context.Context, dir, domainFlag, sshTarget, sshKey string) edgeDriftReport {
	envProbeErr := probeEdgeEnvFile(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile)

	nodeID := readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile, "NODE_ID")
	envDomain := readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile, "EDGE_DOMAIN")
	foghornAddr := readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile, "FOGHORN_CONTROL_ADDR")
	telemetryURL := readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile, "TELEMETRY_URL")
	deployModeRaw := readRemoteEnvFileKey(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile, "DEPLOY_MODE")
	deployMode := "docker"
	if deployModeRaw == "native" {
		deployMode = "native"
	}

	effectiveDomain := strings.TrimSpace(domainFlag)
	if effectiveDomain == "" {
		effectiveDomain = envDomain
	}

	dockerChecks := probeEdgeDockerServices(ctx, dir, sshTarget, sshKey)
	nativeChecks := probeEdgeNativeServices(ctx, sshTarget, sshKey)
	services := classifyEdgeServices(deployMode, dockerChecks, nativeChecks)

	var config []edgeDriftConfigStatus
	if envProbeErr != nil {
		detail := envProbeErr.Error()
		for _, key := range []string{"NODE_ID", "EDGE_DOMAIN", "FOGHORN_CONTROL_ADDR", "TELEMETRY_URL"} {
			config = append(config, edgeDriftConfigStatus{Key: key, Status: "probe_error", Detail: detail})
		}
	} else {
		config = []edgeDriftConfigStatus{
			classifyConfigKey("NODE_ID", nodeID, false),
			classifyEdgeDomain(envDomain, domainFlag),
			classifyConfigKey("FOGHORN_CONTROL_ADDR", foghornAddr, false),
			classifyConfigKey("TELEMETRY_URL", telemetryURL, true),
		}
	}

	var health *edgeDriftHealth
	if effectiveDomain != "" {
		health = probeEdgeHTTPS(ctx, effectiveDomain)
	}

	rep := edgeDriftReport{
		Node:     nodeID,
		Domain:   effectiveDomain,
		Mode:     deployMode,
		Services: services,
		Config:   config,
		Health:   health,
	}
	rep.Summary = edgeDriftSummary{
		Total:       len(services) + len(config) + boolAsInt(health != nil),
		Divergences: countEdgeDriftDivergences(rep),
	}
	return rep
}

// probeEdgeDockerServices returns the docker-stack checks for edgeDriftServices.
// Services absent from docker compose ps are omitted, matching parseEdgeServiceStatus.
func probeEdgeDockerServices(ctx context.Context, dir, sshTarget, sshKey string) []readiness.EdgeCheck {
	compose := "docker-compose.edge.yml"
	var out string
	var err error
	if strings.TrimSpace(sshTarget) != "" {
		_, out, _, err = xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "docker", []string{"compose", "-f", compose, "--env-file", edgeDriftEnvFile, "ps"}, dir)
	} else {
		_, out, _, err = xexec.Run(ctx, "docker", []string{"compose", "-f", compose, "--env-file", edgeDriftEnvFile, "ps"}, dir)
	}
	if err != nil {
		return nil
	}
	return parseEdgeServiceStatus(out, "docker")
}

// probeEdgeNativeServices returns the native-stack checks for edgeDriftServices.
// Services absent from systemctl/launchctl output are omitted, matching parseEdgeServiceStatus.
func probeEdgeNativeServices(ctx context.Context, sshTarget, sshKey string) []readiness.EdgeCheck {
	targetOS := detectEdgeOS(ctx, sshTarget, sshKey)
	var statusCmd string
	if targetOS == "darwin" {
		statusCmd = "launchctl list 2>/dev/null | grep com.livepeer.frameworks"
	} else {
		statusCmd = "systemctl status frameworks-caddy frameworks-helmsman frameworks-mistserver --no-pager 2>&1 | head -40"
	}
	var out string
	var err error
	if strings.TrimSpace(sshTarget) != "" {
		_, out, _, err = xexec.RunSSHWithKey(ctx, sshTarget, sshKey, "sh", []string{"-c", statusCmd}, "")
	} else {
		_, out, _, err = xexec.Run(ctx, "sh", []string{"-c", statusCmd}, "")
	}
	if err != nil && strings.TrimSpace(out) == "" {
		return nil
	}
	return parseEdgeServiceStatus(out, "native")
}

// classifyEdgeServices combines docker and native probe results per service.
// Invariant: a service omitted from both parsed probe outputs is reported as
// `missing` rather than inferred. Services found in the stack opposite the
// configured DEPLOY_MODE are `wrong_mode`, regardless of their run state.
func classifyEdgeServices(deployMode string, docker, native []readiness.EdgeCheck) []edgeDriftServiceStatus {
	dockerByName := map[string]readiness.EdgeCheck{}
	for _, c := range docker {
		dockerByName[c.Name] = c
	}
	nativeByName := map[string]readiness.EdgeCheck{}
	for _, c := range native {
		nativeByName[c.Name] = c
	}

	out := make([]edgeDriftServiceStatus, 0, len(edgeDriftServices))
	for _, svc := range edgeDriftServices {
		expected := dockerByName
		other := nativeByName
		if deployMode == "native" {
			expected = nativeByName
			other = dockerByName
		}
		inExpected, foundExpected := expected[svc]
		inOther, foundOther := other[svc]
		switch {
		case foundExpected && inExpected.OK:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusOK, Detail: inExpected.Detail})
		case foundExpected && !inExpected.OK:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusStopped, Detail: inExpected.Detail})
		case foundOther:
			otherMode := "docker"
			if deployMode == "docker" {
				otherMode = "native"
			}
			detail := inOther.Detail
			if detail == "" {
				detail = "found in " + otherMode + " stack"
			}
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusWrongMode, Detail: detail})
		default:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusMissing})
		}
	}
	return out
}

// classifyConfigKey returns present/empty/missing for a .edge.env key.
// informational=true means the missing/empty state is reported but does not
// count as a divergence.
func classifyConfigKey(key, value string, informational bool) edgeDriftConfigStatus {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed != "":
		return edgeDriftConfigStatus{Key: key, Status: driftConfigPresent}
	case value != "":
		detail := ""
		if informational {
			detail = "informational"
		}
		return edgeDriftConfigStatus{Key: key, Status: driftConfigEmpty, Detail: detail}
	default:
		detail := ""
		if informational {
			detail = "informational"
		}
		return edgeDriftConfigStatus{Key: key, Status: driftStatusMissing, Detail: detail}
	}
}

// classifyEdgeDomain handles the EDGE_DOMAIN + --domain flag interaction.
// Missing or empty EDGE_DOMAIN is informational (no heuristic for public
// vs. private node); only a flag/env disagreement counts as divergence.
func classifyEdgeDomain(envValue, flagValue string) edgeDriftConfigStatus {
	envTrim := strings.TrimSpace(envValue)
	flagTrim := strings.TrimSpace(flagValue)
	if envTrim != "" && flagTrim != "" && envTrim != flagTrim {
		return edgeDriftConfigStatus{
			Key:    "EDGE_DOMAIN",
			Status: driftConfigDomainFlagMismatch,
			Detail: fmt.Sprintf("env=%s flag=%s", envTrim, flagTrim),
		}
	}
	return classifyConfigKey("EDGE_DOMAIN", envValue, true)
}

func probeEdgeHTTPS(ctx context.Context, domain string) *edgeDriftHealth {
	url := "https://" + domain + "/health"
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return &edgeDriftHealth{URL: url, Status: driftHealthMismatch, Detail: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return &edgeDriftHealth{URL: url, Status: driftHealthMismatch, Detail: err.Error()}
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	if resp.StatusCode == http.StatusOK {
		return &edgeDriftHealth{URL: url, Status: driftHealthOK, Detail: fmt.Sprintf("http %d", resp.StatusCode)}
	}
	return &edgeDriftHealth{URL: url, Status: driftHealthMismatch, Detail: fmt.Sprintf("http %d", resp.StatusCode)}
}

func countEdgeDriftDivergences(rep edgeDriftReport) int {
	n := 0
	for _, s := range rep.Services {
		if s.Status != driftStatusOK {
			n++
		}
	}
	for _, c := range rep.Config {
		if c.Detail == "informational" {
			continue
		}
		if c.Status == "probe_error" {
			n++
			continue
		}
		if c.Status != driftConfigPresent {
			n++
		}
	}
	if rep.Health != nil && rep.Health.Status != driftHealthOK {
		n++
	}
	return n
}

func renderEdgeDriftText(w io.Writer, rep edgeDriftReport) {
	header := fmt.Sprintf("Edge drift (mode: %s", rep.Mode)
	if rep.Node != "" {
		header += ", node: " + rep.Node
	}
	if rep.Domain != "" {
		header += ", domain: " + rep.Domain
	}
	header += ")"
	ux.Heading(w, header)
	fmt.Fprintln(w)

	ux.Subheading(w, "Services:")
	for _, s := range rep.Services {
		line := fmt.Sprintf("%-12s %s", s.Name, s.Status)
		if s.Detail != "" {
			line += "  (" + s.Detail + ")"
		}
		switch s.Status {
		case driftStatusOK:
			ux.Success(w, line)
		default:
			ux.Fail(w, line)
		}
	}
	fmt.Fprintln(w)

	ux.Subheading(w, "Configuration:")
	for _, c := range rep.Config {
		line := fmt.Sprintf("%-24s %s", c.Key, c.Status)
		if c.Detail != "" {
			line += "  (" + c.Detail + ")"
		}
		switch {
		case c.Status == driftConfigPresent:
			ux.Success(w, line)
		case c.Detail == "informational":
			ux.Warn(w, line)
		default:
			ux.Fail(w, line)
		}
	}
	fmt.Fprintln(w)

	if rep.Health != nil {
		ux.Subheading(w, "Health:")
		line := fmt.Sprintf("%-30s %s", rep.Health.URL, rep.Health.Status)
		if rep.Health.Detail != "" {
			line += "  (" + rep.Health.Detail + ")"
		}
		if rep.Health.Status == driftHealthOK {
			ux.Success(w, line)
		} else {
			ux.Fail(w, line)
		}
		fmt.Fprintln(w)
	}

	if rep.Summary.Divergences == 0 {
		ux.Success(w, fmt.Sprintf("No drift detected (%d checks)", rep.Summary.Total))
	} else {
		ux.Fail(w, fmt.Sprintf("%d divergence(s) in %d checks", rep.Summary.Divergences, rep.Summary.Total))
	}
}

func boolAsInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
