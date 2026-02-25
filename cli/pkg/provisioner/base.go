package provisioner

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// BaseProvisioner provides common functionality for all provisioners
type BaseProvisioner struct {
	name    string
	sshPool *ssh.Pool
}

// NewBaseProvisioner creates a new base provisioner
func NewBaseProvisioner(name string, pool *ssh.Pool) *BaseProvisioner {
	if pool == nil {
		pool = ssh.NewPool(30 * time.Second)
	}

	return &BaseProvisioner{
		name:    name,
		sshPool: pool,
	}
}

// GetName returns the provisioner name
func (b *BaseProvisioner) GetName() string {
	return b.name
}

// GetRunner returns an SSH runner for a host
func (b *BaseProvisioner) GetRunner(host inventory.Host) (ssh.Runner, error) {
	// Use local runner for localhost
	if host.Address == "127.0.0.1" || host.Address == "localhost" {
		return ssh.NewLocalRunner(""), nil
	}

	// Get SSH client from pool
	sshConfig := &ssh.ConnectionConfig{
		Address: host.Address,
		Port:    22, // Default SSH port
		User:    host.User,
		KeyPath: host.SSHKey,
		Timeout: 30 * time.Second,
	}

	return b.sshPool.Get(sshConfig)
}

// RunCommand executes a command on a host
func (b *BaseProvisioner) RunCommand(ctx context.Context, host inventory.Host, command string) (*ssh.CommandResult, error) {
	runner, err := b.GetRunner(host)
	if err != nil {
		return nil, fmt.Errorf("failed to get runner: %w", err)
	}

	return runner.Run(ctx, command)
}

// CheckExists checks if a service exists using detector
func (b *BaseProvisioner) CheckExists(ctx context.Context, host inventory.Host, serviceName string) (*detect.ServiceState, error) {
	detector := detect.NewDetector(host)
	return detector.Detect(ctx, serviceName)
}

// WaitForService waits for a service to become available
func (b *BaseProvisioner) WaitForService(ctx context.Context, host inventory.Host, serviceName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s to become available", serviceName)

		case <-ticker.C:
			state, err := b.CheckExists(ctx, host, serviceName)
			if err != nil {
				continue
			}

			if state.Exists && state.Running {
				return nil
			}
		}
	}
}

// ExecuteScript uploads and runs a shell script
func (b *BaseProvisioner) ExecuteScript(ctx context.Context, host inventory.Host, script string) (*ssh.CommandResult, error) {
	runner, err := b.GetRunner(host)
	if err != nil {
		return nil, fmt.Errorf("failed to get runner: %w", err)
	}

	return runner.RunScript(ctx, script)
}

// UploadFile uploads a file to a host
func (b *BaseProvisioner) UploadFile(ctx context.Context, host inventory.Host, opts ssh.UploadOptions) error {
	runner, err := b.GetRunner(host)
	if err != nil {
		return fmt.Errorf("failed to get runner: %w", err)
	}

	return runner.Upload(ctx, opts)
}

// DetectRemoteArch detects the remote host's OS and architecture via SSH.
// For localhost, returns the local runtime values.
func (b *BaseProvisioner) DetectRemoteArch(ctx context.Context, host inventory.Host) (osName, goArch string, err error) {
	if host.Address == "127.0.0.1" || host.Address == "localhost" || host.Address == "" {
		return runtime.GOOS, runtime.GOARCH, nil
	}
	result, err := b.RunCommand(ctx, host, "uname -sm")
	if err != nil {
		return "", "", fmt.Errorf("failed to detect remote architecture: %w", err)
	}
	if result.ExitCode != 0 {
		return "", "", fmt.Errorf("uname failed: %s", result.Stderr)
	}
	return ParseUnameOutput(result.Stdout)
}

// ParseUnameOutput converts `uname -sm` output (e.g. "Linux x86_64") to Go GOOS/GOARCH values.
func ParseUnameOutput(output string) (osName, goArch string, err error) {
	parts := strings.Fields(strings.TrimSpace(output))
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unexpected uname output: %q", output)
	}
	osName = strings.ToLower(parts[0])
	switch parts[1] {
	case "x86_64":
		goArch = "amd64"
	case "aarch64", "arm64":
		goArch = "arm64"
	case "armv7l":
		goArch = "arm"
	default:
		goArch = parts[1]
	}
	return osName, goArch, nil
}

// Cleanup stops a service for rollback. Default implementation tries docker/systemd stop.
func (b *BaseProvisioner) Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	serviceName := b.name

	var attempts []string
	switch config.Mode {
	case "docker":
		attempts = []string{
			fmt.Sprintf("docker compose stop %s", serviceName),
			fmt.Sprintf("docker stop frameworks-%s", serviceName),
			fmt.Sprintf("docker rm -f frameworks-%s", serviceName),
		}
	case "native":
		attempts = []string{
			fmt.Sprintf("systemctl stop frameworks-%s", serviceName),
			fmt.Sprintf("systemctl kill frameworks-%s", serviceName),
		}
	default:
		attempts = []string{
			fmt.Sprintf("docker compose stop %s", serviceName),
			fmt.Sprintf("docker stop frameworks-%s", serviceName),
			fmt.Sprintf("docker rm -f frameworks-%s", serviceName),
			fmt.Sprintf("systemctl stop frameworks-%s", serviceName),
			fmt.Sprintf("systemctl kill frameworks-%s", serviceName),
		}
	}

	var errMessages []string
	for _, cmd := range attempts {
		result, err := b.RunCommand(ctx, host, cmd)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		if err != nil {
			errMessages = append(errMessages, fmt.Sprintf("%s: %v", cmd, err))
		} else if result != nil && result.ExitCode != 0 {
			errMessages = append(errMessages, fmt.Sprintf("%s: %s", cmd, result.Stderr))
		}
	}

	return fmt.Errorf("cleanup failed for %s: %s", serviceName, strings.Join(errMessages, "; "))
}
