package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/servicedefs"
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

		// Parse output: name|status|image
		parts := strings.Split(strings.TrimSpace(stdout), "|")
		if len(parts) < 3 {
			continue
		}

		state.Exists = true
		state.Mode = "docker"
		state.Running = parts[1] == "running"
		state.DetectedBy = "docker"
		state.Metadata["image"] = parts[2]
		state.Metadata["container_name"] = parts[0]

		// Extract version from image tag
		if strings.Contains(parts[2], ":") {
			imageParts := strings.Split(parts[2], ":")
			if len(imageParts) == 2 {
				state.Version = imageParts[1]
			}
		}

		return &DetectionResult{Method: "docker", Success: true, State: state}, nil
	}

	return &DetectionResult{Method: "docker", Success: false}, nil
}

// detectFromSystemd checks for systemd service
func (d *Detector) detectFromSystemd(ctx context.Context, serviceName string, state *ServiceState) (*DetectionResult, error) {
	serviceNames := []string{
		fmt.Sprintf("frameworks-%s", serviceName),
		serviceName,
	}

	for _, svcName := range serviceNames {
		cmd := fmt.Sprintf("systemctl show %s --property=LoadState,ActiveState,SubState", svcName)
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
