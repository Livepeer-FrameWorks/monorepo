package cmd

import (
	"context"
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

// Expected service names per stack shape: the container stack runs one
// "edge" compose service (caddy/mistserver/helmsman live inside it under
// s6); the native stack runs three host services.
var (
	edgeDriftContainerServices = []string{"edge"}
	edgeDriftNativeServices    = []string{"caddy", "mistserver", "helmsman"}
)

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

Reports whether the expected edge services are running in the configured
stack (container mode: the single frameworks-edge container; native mode:
caddy, mistserver, helmsman), whether required .edge.env keys are present,
and whether the HTTPS health endpoint is reachable. Probes both stacks so a
service running under the wrong manager surfaces as wrong_mode.

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
	// Ansible-provisioned nodes keep the stack (and .edge.env) under
	// /opt/frameworks/edge; resolve before probing so drift surveys the
	// deployed state instead of the operator's cwd.
	dir, composeFile := resolveEdgeComposeContext(ctx, dir, sshTarget, sshKey)
	// detectEdgeMode recognizes native installs via their deployed-state
	// markers; a bare NormalizeEdgeMode(.edge.env) would misreport native
	// Ansible nodes (which never render .edge.env) as container.
	deployMode := detectEdgeMode(ctx, dir, edgeDriftEnvFile, sshTarget, sshKey)

	// Container installs carry .edge.env next to the compose file; native
	// installs keep metadata in helmsman.env, so a missing .edge.env is
	// only a probe failure in container mode.
	var envProbeErr error
	if deployMode != "native" {
		envProbeErr = probeEdgeEnvFile(ctx, sshTarget, sshKey, dir, edgeDriftEnvFile)
	}

	nodeID := readEdgeMetadataKey(ctx, sshTarget, sshKey, dir, deployMode, "NODE_ID")
	envDomain := readEdgeMetadataKey(ctx, sshTarget, sshKey, dir, deployMode, "EDGE_DOMAIN")
	foghornAddr := readEdgeMetadataKey(ctx, sshTarget, sshKey, dir, deployMode, "FOGHORN_CONTROL_ADDR")
	telemetryURL := readEdgeMetadataKey(ctx, sshTarget, sshKey, dir, deployMode, "TELEMETRY_URL")

	effectiveDomain := strings.TrimSpace(domainFlag)
	if effectiveDomain == "" {
		effectiveDomain = envDomain
	}

	containerChecks := probeEdgeContainerServices(ctx, dir, composeFile, sshTarget, sshKey)
	nativeChecks := probeEdgeNativeServices(ctx, sshTarget, sshKey)
	services := classifyEdgeServices(deployMode, containerChecks, nativeChecks)

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

// probeEdgeContainerServices returns the container-stack checks (the single
// edge compose service). Services absent from docker compose ps are omitted,
// matching parseEdgeServiceStatus.
func probeEdgeContainerServices(ctx context.Context, dir, compose, sshTarget, sshKey string) []readiness.EdgeCheck {
	out, _, err := runEdgeDocker(ctx, sshTarget, sshKey, []string{"compose", "-f", compose, "--env-file", edgeDriftEnvFile, "ps"}, dir)
	if err != nil {
		return nil
	}
	return parseEdgeServiceStatus(out, "container")
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

// classifyEdgeServices combines container and native probe results.
// Invariants: an expected service absent from its stack's probe output is
// `missing` rather than inferred, and any service found in the stack
// opposite the configured DEPLOY_MODE is `wrong_mode` regardless of run
// state. Service names differ per stack (container: "edge"; native: the
// three host services), so the expected and stray lists are mode-dependent.
func classifyEdgeServices(deployMode string, container, native []readiness.EdgeCheck) []edgeDriftServiceStatus {
	containerByName := map[string]readiness.EdgeCheck{}
	for _, c := range container {
		containerByName[c.Name] = c
	}
	nativeByName := map[string]readiness.EdgeCheck{}
	for _, c := range native {
		nativeByName[c.Name] = c
	}

	expectedServices := edgeDriftContainerServices
	expected := containerByName
	strayServices := edgeDriftNativeServices
	stray := nativeByName
	strayMode := "native"
	if deployMode == "native" {
		expectedServices = edgeDriftNativeServices
		expected = nativeByName
		strayServices = edgeDriftContainerServices
		stray = containerByName
		strayMode = "container"
	}

	out := make([]edgeDriftServiceStatus, 0, len(expectedServices)+len(strayServices))
	for _, svc := range expectedServices {
		check, found := expected[svc]
		switch {
		case found && check.OK:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusOK, Detail: check.Detail})
		case found:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusStopped, Detail: check.Detail})
		default:
			out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusMissing})
		}
	}
	for _, svc := range strayServices {
		check, found := stray[svc]
		if !found {
			continue
		}
		detail := check.Detail
		if detail == "" {
			detail = "found in " + strayMode + " stack"
		}
		out = append(out, edgeDriftServiceStatus{Name: svc, Status: driftStatusWrongMode, Detail: detail})
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
	client := &http.Client{Timeout: 3 * time.Second}
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
