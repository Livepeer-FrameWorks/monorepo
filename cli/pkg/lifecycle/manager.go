package lifecycle

import (
	"context"
	"fmt"
)

// ServiceStatus represents the running state of a service.
type ServiceStatus struct {
	Running bool
	Detail  string
}

// CommandRunner executes a shell command and returns its output.
type CommandRunner interface {
	Run(ctx context.Context, command string) (stdout string, exitCode int, err error)
}

// Manager controls service lifecycle (start/stop/restart/status).
type Manager interface {
	Start(ctx context.Context, service string) error
	Stop(ctx context.Context, service string) error
	Restart(ctx context.Context, service string) error
	Status(ctx context.Context, service string) (ServiceStatus, error)
}

// NewManager returns a Manager for the given deployment mode.
func NewManager(mode string, runner CommandRunner) (Manager, error) {
	switch mode {
	case "docker":
		return &DockerManager{runner: runner}, nil
	case "native", "systemd":
		return &NativeManager{runner: runner}, nil
	case "darwin", "launchd":
		return &DarwinManager{runner: runner}, nil
	default:
		return nil, fmt.Errorf("unsupported lifecycle mode: %s", mode)
	}
}
