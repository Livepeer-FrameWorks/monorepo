package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// sshRunner is the minimal interface Detector needs. Production wraps a
// *fwssh.Pool; tests inject a stub.
type sshRunner interface {
	runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string)
}

// Detector performs multi-method service detection
type Detector struct {
	host   inventory.Host
	runner sshRunner
}

// NewDetector creates a new detector that routes SSH calls through the given
// pool — this ensures alias resolution, host-key policy, and identity
// selection match the rest of the provisioner stack.
func NewDetector(pool *fwssh.Pool, host inventory.Host) *Detector {
	return &Detector{host: host, runner: &poolRunner{pool: pool, host: host}}
}

// runSSH delegates to the configured runner.
func (d *Detector) runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string) {
	return d.runner.runSSH(ctx, cmd)
}

// poolRunner is the production sshRunner backed by *fwssh.Pool.
type poolRunner struct {
	pool *fwssh.Pool
	host inventory.Host
}

// runSSH invokes a command via the shared pool. Non-zero exit codes are
// reported in ExitCode rather than propagated as errors so the detection
// chain can treat "command ran but said no" as "try next method."
func (r *poolRunner) runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string) {
	cfg := &fwssh.ConnectionConfig{
		Address:  r.host.ExternalIP,
		Port:     22,
		User:     r.host.User,
		HostName: r.host.Name,
		Timeout:  10 * time.Second,
	}
	result, _ := r.pool.Run(ctx, cfg, cmd) //nolint:errcheck // detection reads ExitCode; see type doc
	if result == nil {
		return -1, "", ""
	}
	return result.ExitCode, result.Stdout, result.Stderr
}

// Detect attempts to detect a service using multiple methods
func (d *Detector) Detect(ctx context.Context, serviceName string) (*ServiceState, error) {
	state := &ServiceState{
		ServiceName: serviceName,
		DetectedAt:  time.Now(),
		Metadata:    make(map[string]string),
	}

	// Try detection methods in order of reliability
	methods := []func(context.Context, string, *ServiceState) (*DetectionResult, error){
		d.detectFromInventory,
		d.detectFromDocker,
		d.detectFromSystemd,
		d.detectFromPort,
	}

	for _, method := range methods {
		result, err := method(ctx, serviceName, state)
		if err != nil {
			continue // Try next method
		}
		if result.Success && result.State != nil {
			return result.State, nil
		}
	}

	// Not found by any method
	state.Exists = false
	return state, nil
}

// detectFromInventory checks /etc/frameworks/inventory.json
func (d *Detector) detectFromInventory(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	exitCode, stdout, _ := d.runSSH(ctx, "cat /etc/frameworks/inventory.json")

	if exitCode != 0 {
		return &DetectionResult{Method: "inventory", Success: false}, nil
	}

	var inv struct {
		Services map[string]struct {
			Mode          string    `json:"mode"`
			Version       string    `json:"version"`
			ProvisionedAt time.Time `json:"provisioned_at"`
		} `json:"services"`
	}

	if err := json.Unmarshal([]byte(stdout), &inv); err != nil {
		//nolint:nilerr // detection failure is not an error, returned in result
		return &DetectionResult{Method: "inventory", Success: false, Error: err}, nil
	}

	svc, ok := inv.Services[serviceName]
	if !ok {
		return &DetectionResult{Method: "inventory", Success: false}, nil
	}

	state.Exists = true
	state.Mode = svc.Mode
	state.Version = svc.Version
	state.DetectedBy = "inventory"
	state.Metadata["provisioned_at"] = svc.ProvisionedAt.Format(time.RFC3339)

	// Still need to check if it's actually running
	switch svc.Mode {
	case "docker":
		d.checkDockerRunning(ctx, serviceName, state)
	case "native":
		d.checkSystemdRunning(ctx, serviceName, state)
	}

	return &DetectionResult{Method: "inventory", Success: true, State: state}, nil
}

// detectFromDocker checks for Docker container
func (d *Detector) detectFromDocker(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	containerNames := []string{
		fmt.Sprintf("frameworks-%s", serviceName),
		serviceName,
		fmt.Sprintf("frameworks_%s", serviceName),
	}

	for _, containerName := range containerNames {
		cmd := fmt.Sprintf("docker ps -a --filter name=%s --format '{{.Names}}|{{.State}}|{{.Image}}'", containerName)
		exitCode, stdout, _ := d.runSSH(ctx, cmd)

		if exitCode != 0 {
			continue
		}

		if strings.TrimSpace(stdout) == "" {
			continue
		}

		// Parse output: name|status|image. Docker's name filter is substring-
		// based, so require an exact container name before accepting a row.
		parts := exactDockerContainerRow(stdout, containerName)
		if len(parts) < 3 {
			continue
		}

		state.Exists = true
		state.Mode = "docker"
		state.Running = parts[1] == "running"
		state.DetectedBy = "docker"
		state.Metadata["image"] = parts[2]
		state.Metadata["container_name"] = parts[0]

		state.Version = dockerImageVersion(parts[2])

		return &DetectionResult{Method: "docker", Success: true, State: state}, nil
	}

	return &DetectionResult{Method: "docker", Success: false}, nil
}

func exactDockerContainerRow(stdout, containerName string) []string {
	for _, line := range strings.Split(stdout, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) >= 3 && parts[0] == containerName {
			return parts
		}
	}
	return nil
}

func dockerImageVersion(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	if strings.HasPrefix(image, "sha256:") {
		return ""
	}
	if beforeDigest, _, ok := strings.Cut(image, "@"); ok {
		image = beforeDigest
	}
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	if lastColon <= lastSlash {
		return ""
	}
	return strings.TrimSpace(image[lastColon+1:])
}

// detectFromSystemd checks for systemd service
func (d *Detector) detectFromSystemd(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	serviceNames := []string{
		fmt.Sprintf("frameworks-%s", serviceName),
		serviceName,
	}

	for _, svcName := range serviceNames {
		cmd := fmt.Sprintf("systemctl show %s --property=LoadState,ActiveState,SubState,ExecStart", svcName)
		exitCode, stdout, _ := d.runSSH(ctx, cmd)

		if exitCode != 0 {
			continue
		}

		// Parse systemctl output
		props := make(map[string]string)
		for _, line := range strings.Split(stdout, "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
			if len(parts) == 2 {
				props[parts[0]] = parts[1]
			}
		}

		if props["LoadState"] != "loaded" {
			continue
		}

		state.Exists = true
		state.Mode = "native"
		state.Running = props["ActiveState"] == "active" && props["SubState"] == "running"
		state.DetectedBy = "systemd"
		state.Metadata["systemd_service"] = svcName
		state.Metadata["active_state"] = props["ActiveState"]
		state.Metadata["sub_state"] = props["SubState"]
		if props["ExecStart"] != "" {
			state.Metadata["exec_start"] = props["ExecStart"]
			if bin := systemdExecPath(props["ExecStart"]); bin != "" {
				state.Metadata["binary_path"] = bin
				if version := d.readNativePlatformVersion(ctx, bin); version != "" {
					state.Version = version
				}
			}
		}

		return &DetectionResult{Method: "systemd", Success: true, State: state}, nil
	}

	return &DetectionResult{Method: "systemd", Success: false}, nil
}

// detectFromPort checks if service is listening on expected port
func (d *Detector) detectFromPort(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	port := getDefaultPort(serviceName)
	if port == 0 {
		return &DetectionResult{Method: "port", Success: false}, nil
	}

	cmd := fmt.Sprintf("ss -tlnp | grep ':%d ' || lsof -iTCP:%d -sTCP:LISTEN", port, port)
	exitCode, stdout, _ := d.runSSH(ctx, cmd)

	if exitCode != 0 || strings.TrimSpace(stdout) == "" {
		return &DetectionResult{Method: "port", Success: false}, nil
	}

	state.Exists = true
	state.Mode = "unknown"
	state.Running = true
	state.Reachable = true
	state.DetectedBy = "port"
	state.Metadata["port"] = fmt.Sprintf("%d", port)

	return &DetectionResult{Method: "port", Success: true, State: state}, nil
}

func (d *Detector) checkDockerRunning(ctx context.Context, serviceName string, state *ServiceState) {
	containerName := fmt.Sprintf("frameworks-%s", serviceName)
	cmd := fmt.Sprintf("docker inspect -f '{{.State.Running}}' %s", containerName)
	exitCode, stdout, _ := d.runSSH(ctx, cmd)

	if exitCode == 0 {
		state.Running = strings.TrimSpace(stdout) == "true"
	}
}

func (d *Detector) checkSystemdRunning(ctx context.Context, serviceName string, state *ServiceState) {
	svcName := fmt.Sprintf("frameworks-%s", serviceName)
	cmd := fmt.Sprintf("systemctl is-active %s", svcName)
	exitCode, stdout, _ := d.runSSH(ctx, cmd)

	if exitCode == 0 {
		state.Running = strings.TrimSpace(stdout) == "active"
	}
}

func (d *Detector) readNativePlatformVersion(ctx context.Context, binaryPath string) string {
	quoted := shellQuote(binaryPath)
	cmd := fmt.Sprintf("%s version --json 2>/dev/null || %s version 2>/dev/null", quoted, quoted)
	exitCode, stdout, _ := d.runSSH(ctx, cmd)
	if exitCode != 0 {
		return ""
	}
	out := strings.TrimSpace(stdout)
	if out == "" {
		return ""
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err == nil && strings.TrimSpace(payload.Version) != "" {
		return strings.TrimSpace(payload.Version)
	}

	for _, line := range strings.Split(out, "\n") {
		if before, after, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(before) == "platform version" {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

func systemdExecPath(execStart string) string {
	for _, token := range strings.Fields(execStart) {
		if strings.HasPrefix(token, "path=") {
			return strings.Trim(strings.TrimPrefix(token, "path="), " ;")
		}
		if strings.HasPrefix(token, "argv[]=") {
			argv := strings.TrimPrefix(token, "argv[]=")
			if first, _, ok := strings.Cut(argv, " "); ok {
				return strings.Trim(first, " ;")
			}
			return strings.Trim(argv, " ;")
		}
	}
	return ""
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// getDefaultPort returns the default port for a service
func getDefaultPort(serviceName string) int {
	if port, ok := servicedefs.DefaultPort(serviceName); ok {
		return port
	}

	extraPorts := map[string]int{
		"yugabyte": 5433,
	}
	return extraPorts[serviceName]
}
