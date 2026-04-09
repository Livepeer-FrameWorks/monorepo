package lifecycle

import (
	"context"
	"fmt"
	"strings"
)

// DockerManager manages services via Docker Compose.
type DockerManager struct {
	runner CommandRunner
}

func (m *DockerManager) composePath(service string) string {
	return fmt.Sprintf("/opt/frameworks/%s/docker-compose.yml", service)
}

func (m *DockerManager) Start(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("docker compose -f %s up -d", m.composePath(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("docker start %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Stop(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("docker compose -f %s down", m.composePath(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("docker stop %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Restart(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("docker compose -f %s restart", m.composePath(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("docker restart %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DockerManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	cmd := fmt.Sprintf("docker compose -f %s ps --format '{{.State}}'", m.composePath(service))
	out, _, err := m.runner.Run(ctx, cmd)
	if err != nil {
		return ServiceStatus{}, err
	}
	running := strings.Contains(out, "running")
	return ServiceStatus{Running: running, Detail: strings.TrimSpace(out)}, nil
}
