package provisioner

import (
	"context"
	"fmt"
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

// Cleanup stops a service for rollback. Default implementation tries docker/systemd stop.
func (b *BaseProvisioner) Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	serviceName := b.name

	// Try to stop service based on mode
	var stopCmd string
	switch config.Mode {
	case "docker":
		// Try docker compose stop, fall back to docker stop
		stopCmd = fmt.Sprintf("docker compose stop %s 2>/dev/null || docker stop frameworks-%s 2>/dev/null || true", serviceName, serviceName)
	case "native":
		// Try systemd stop
		stopCmd = fmt.Sprintf("systemctl stop frameworks-%s 2>/dev/null || true", serviceName)
	default:
		// Try both
		stopCmd = fmt.Sprintf("docker compose stop %s 2>/dev/null || docker stop frameworks-%s 2>/dev/null || systemctl stop frameworks-%s 2>/dev/null || true", serviceName, serviceName, serviceName)
	}

	_, err := b.RunCommand(ctx, host, stopCmd)
	if err != nil {
		// Don't fail cleanup - best effort
		fmt.Printf("    Warning: cleanup for %s may have failed: %v\n", serviceName, err)
	}

	return nil // Always return nil - cleanup is best-effort
}
