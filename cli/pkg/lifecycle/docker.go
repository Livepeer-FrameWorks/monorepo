package lifecycle

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/system"
)

// DockerManager manages services via Docker Compose.
type DockerManager struct {
	runner CommandRunner
}

func (m *DockerManager) composePath(service string) string {
	return fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", service)
}

func (m *DockerManager) compose(service, args string) string {
	return system.DockerCommand(fmt.Sprintf("compose -f %s %s", m.composePath(service), args))
}

func (m *DockerManager) Start(ctx context.Context, service string) error {
	_, code, err := m.runner.Run(ctx, m.compose(service, "up -d"))
	if err != nil || code != 0 {
		return fmt.Errorf("docker start %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Stop(ctx context.Context, service string) error {
	_, code, err := m.runner.Run(ctx, m.compose(service, "down"))
	if err != nil || code != 0 {
		return fmt.Errorf("docker stop %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Restart(ctx context.Context, service string) error {
	_, code, err := m.runner.Run(ctx, m.compose(service, "restart"))
	if err != nil || code != 0 {
		return fmt.Errorf("docker restart %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	out, _, err := m.runner.Run(ctx, m.compose(service, "ps --format '{{.State}}'"))
	if err != nil {
		return ServiceStatus{}, err
	}
	running := strings.Contains(out, "running")
	return ServiceStatus{Running: running, Detail: strings.TrimSpace(out)}, nil
}
