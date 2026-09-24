package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/system"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// sshRunner is the minimal interface Detector needs. Production wraps a
// *fwssh.Pool; tests inject a stub.
type sshRunner interface {
	runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string, err error)
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
func (d *Detector) runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string, err error) {
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
func (r *poolRunner) runSSH(ctx context.Context, cmd string) (exitCode int, stdout, stderr string, runErr error) {
	cfg := &fwssh.ConnectionConfig{
		Address:  r.host.ExternalIP,
		Port:     22,
		User:     r.host.User,
		HostName: r.host.Name,
		Timeout:  10 * time.Second,
	}
	result, err := r.pool.Run(ctx, cfg, cmd)
	if result == nil {
		if err == nil {
			err = fmt.Errorf("SSH command returned no result")
		}
		return -1, "", "", err
	}
	// A remote command's ordinary non-zero exit is a valid negative probe.
	// Process failures use a negative code and OpenSSH reserves 255 for
	// transport failures; both must remain visible.
	if err != nil && (result.ExitCode < 0 || result.ExitCode == 255) {
		return result.ExitCode, result.Stdout, result.Stderr, err
	}
	return result.ExitCode, result.Stdout, result.Stderr, nil
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
			return nil, err
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
	exitCode, stdout, _, err := d.runSSH(ctx, "cat /etc/frameworks/inventory.json")
	if err != nil {
		return nil, fmt.Errorf("inspect %s inventory on %s: %w", serviceName, d.host.Name, err)
	}

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
		if err := d.inspectDockerRuntime(ctx, fmt.Sprintf("frameworks-%s", serviceName), state); err != nil {
			return nil, err
		}
	case "native":
		if err := d.checkSystemdRunning(ctx, serviceName, state); err != nil {
			return nil, err
		}
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
		// Without the sudo fallback a running container is invisible to a
		// provisioning user outside the docker group, and the service is
		// misdetected by port as mode "unknown".
		cmd := system.DockerCommand(fmt.Sprintf("ps -a --filter name=%s --format '{{.Names}}|{{.State}}|{{.Image}}'", containerName))
		exitCode, stdout, _, err := d.runSSH(ctx, cmd)
		if err != nil {
			return nil, fmt.Errorf("inspect %s Docker state on %s: %w", serviceName, d.host.Name, err)
		}

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

		if err := d.inspectDockerRuntime(ctx, parts[0], state); err != nil {
			return nil, err
		}
		state.Version = dockerImageVersion(state.Metadata["image"])

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
		cmd := fmt.Sprintf("systemctl show %s --property=LoadState,ActiveState,SubState,ExecStart,EnvironmentFiles,WorkingDirectory,User", svcName)
		exitCode, stdout, _, err := d.runSSH(ctx, cmd)
		if err != nil {
			return nil, fmt.Errorf("inspect %s systemd state on %s: %w", serviceName, d.host.Name, err)
		}

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
		state.Mode = systemdRuntimeMode(props["ExecStart"])
		state.Running = props["ActiveState"] == "active" && (props["SubState"] == "running" || (state.Mode == "docker" && props["SubState"] == "exited"))
		state.DetectedBy = "systemd"
		state.Metadata["systemd_service"] = svcName
		state.Metadata["active_state"] = props["ActiveState"]
		state.Metadata["sub_state"] = props["SubState"]
		state.Metadata["service_user"] = props["User"]
		state.Metadata["working_directory"] = props["WorkingDirectory"]
		state.Metadata["environment_file"] = systemdEnvironmentFile(props["EnvironmentFiles"])
		if props["ExecStart"] != "" {
			state.Metadata["exec_start"] = props["ExecStart"]
			if bin := systemdExecPath(props["ExecStart"]); bin != "" && state.Mode == "native" {
				state.Metadata["binary_path"] = bin
				version, err := d.readNativePlatformVersion(ctx, bin)
				if err != nil {
					return nil, err
				}
				if version != "" {
					state.Version = version
				}
			}
		}

		return &DetectionResult{Method: "systemd", Success: true, State: state}, nil
	}

	return &DetectionResult{Method: "systemd", Success: false}, nil
}

func systemdRuntimeMode(execStart string) string {
	command := strings.ToLower(execStart)
	if strings.Contains(command, "docker compose") || strings.Contains(command, "docker-compose") {
		return "docker"
	}
	return "native"
}

func systemdEnvironmentFile(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	first, _, _ := strings.Cut(value, " ")
	return strings.TrimPrefix(first, "-")
}

// detectFromPort checks if service is listening on expected port
func (d *Detector) detectFromPort(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	port := getDefaultPort(serviceName)
	if port == 0 {
		return &DetectionResult{Method: "port", Success: false}, nil
	}

	cmd := fmt.Sprintf("ss -tlnp | grep ':%d ' || lsof -iTCP:%d -sTCP:LISTEN", port, port)
	exitCode, stdout, _, err := d.runSSH(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("inspect %s listener on %s: %w", serviceName, d.host.Name, err)
	}

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

func (d *Detector) inspectDockerRuntime(ctx context.Context, containerName string, state *ServiceState) error {
	cmd := system.DockerCommand(fmt.Sprintf("inspect -f '{{.State.Running}}|{{.Config.Image}}' %s", shellQuote(containerName)))
	exitCode, stdout, _, err := d.runSSH(ctx, cmd)
	if err != nil {
		return fmt.Errorf("inspect %s Docker runtime on %s: %w", containerName, d.host.Name, err)
	}

	if exitCode == 0 {
		parts := strings.SplitN(strings.TrimSpace(stdout), "|", 2)
		state.Running = parts[0] == "true"
		if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
			state.Metadata["image"] = strings.TrimSpace(parts[1])
		}
	}
	return nil
}

func (d *Detector) checkSystemdRunning(ctx context.Context, serviceName string, state *ServiceState) error {
	svcName := fmt.Sprintf("frameworks-%s", serviceName)
	cmd := fmt.Sprintf("systemctl is-active %s", svcName)
	exitCode, stdout, _, err := d.runSSH(ctx, cmd)
	if err != nil {
		return fmt.Errorf("inspect %s systemd runtime on %s: %w", serviceName, d.host.Name, err)
	}

	if exitCode == 0 {
		state.Running = strings.TrimSpace(stdout) == "active"
	}
	return nil
}

func (d *Detector) readNativePlatformVersion(ctx context.Context, binaryPath string) (string, error) {
	quoted := shellQuote(binaryPath)
	cmd := fmt.Sprintf("%s version --json 2>/dev/null || %s version 2>/dev/null", quoted, quoted)
	exitCode, stdout, _, err := d.runSSH(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("inspect native version on %s: %w", d.host.Name, err)
	}
	if exitCode != 0 {
		return "", nil
	}
	out := strings.TrimSpace(stdout)
	if out == "" {
		return "", nil
	}

	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err == nil && strings.TrimSpace(payload.Version) != "" {
		return strings.TrimSpace(payload.Version), nil
	}

	for _, line := range strings.Split(out, "\n") {
		if before, after, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(before) == "platform version" {
			return strings.TrimSpace(after), nil
		}
	}
	return "", nil
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
