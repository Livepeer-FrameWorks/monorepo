package detect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/cli/internal/xexec"
	"frameworks/cli/pkg/inventory"
)

// Detector performs multi-method service detection
type Detector struct {
	host inventory.Host
}

// NewDetector creates a new detector for a host
func NewDetector(host inventory.Host) *Detector {
	return &Detector{host: host}
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
	// Read remote inventory file via SSH
	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)
	exitCode, stdout, _, err := xexec.RunSSH(target, "cat", []string{"/etc/frameworks/inventory.json"}, "")

	if exitCode != 0 || err != nil {
		//nolint:nilerr // detection failure is not an error, returned in result
		return &DetectionResult{Method: "inventory", Success: false, Error: err}, nil
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
	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)

	// Try both naming patterns
	containerNames := []string{
		fmt.Sprintf("frameworks-%s", serviceName),
		serviceName,
		fmt.Sprintf("frameworks_%s", serviceName),
	}

	for _, containerName := range containerNames {
		cmd := fmt.Sprintf("docker ps -a --filter name=%s --format '{{.Names}}|{{.State}}|{{.Image}}'", containerName)
		exitCode, stdout, _, err := xexec.RunSSH(target, "sh", []string{"-c", cmd}, "")

		if exitCode != 0 || err != nil {
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
	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)

	// Try both naming patterns
	serviceNames := []string{
		fmt.Sprintf("frameworks-%s", serviceName),
		serviceName,
	}

	for _, svcName := range serviceNames {
		cmd := fmt.Sprintf("systemctl show %s --property=LoadState,ActiveState,SubState", svcName)
		exitCode, stdout, _, err := xexec.RunSSH(target, "sh", []string{"-c", cmd}, "")

		if exitCode != 0 || err != nil {
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

	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)
	cmd := fmt.Sprintf("ss -tlnp | grep ':%d ' || lsof -iTCP:%d -sTCP:LISTEN", port, port)
	exitCode, stdout, _, err := xexec.RunSSH(target, "sh", []string{"-c", cmd}, "")

	if exitCode != 0 || err != nil || strings.TrimSpace(stdout) == "" {
		//nolint:nilerr // detection failure is not an error
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

// Helper methods

func (d *Detector) checkDockerRunning(ctx context.Context, serviceName string, state *ServiceState) {
	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)
	containerName := fmt.Sprintf("frameworks-%s", serviceName)
	cmd := fmt.Sprintf("docker inspect -f '{{.State.Running}}' %s", containerName)
	exitCode, stdout, _, _ := xexec.RunSSH(target, "sh", []string{"-c", cmd}, "")

	if exitCode == 0 {
		state.Running = strings.TrimSpace(stdout) == "true"
	}
}

func (d *Detector) checkSystemdRunning(ctx context.Context, serviceName string, state *ServiceState) {
	target := fmt.Sprintf("%s@%s", d.host.User, d.host.Address)
	svcName := fmt.Sprintf("frameworks-%s", serviceName)
	cmd := fmt.Sprintf("systemctl is-active %s", svcName)
	exitCode, stdout, _, _ := xexec.RunSSH(target, "sh", []string{"-c", cmd}, "")

	if exitCode == 0 {
		state.Running = strings.TrimSpace(stdout) == "active"
	}
}

// getDefaultPort returns the default port for a service
func getDefaultPort(serviceName string) int {
	ports := map[string]int{
		"bridge":           18000,
		"commodore":        18001,
		"quartermaster":    18002,
		"purser":           18003,
		"periscope-query":  18004,
		"periscope-ingest": 18005,
		"decklog":          18006,
		"helmsman":         18007,
		"foghorn":          18008,
		"signalman":        18009,
		"navigator":        18010,
		"webapp":           18030,
		"website":          18031,
		"forms":            18032,
		"docs":             18033,
		"postgres":         5432,
		"yugabyte":         5433,
		"kafka":            9092,
		"zookeeper":        2181,
		"clickhouse":       9000,
		"listmonk":         9001,
		"nginx":            18090,
		"prometheus":       9090,
		"grafana":          3000,
		"metabase":         3001,
		"privateer":        18012,
	}
	return ports[serviceName]
}
