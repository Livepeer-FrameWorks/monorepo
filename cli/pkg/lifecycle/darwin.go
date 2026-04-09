package lifecycle

import (
	"context"
	"fmt"
	"strings"
)

// DarwinManager manages services via launchd (macOS).
type DarwinManager struct {
	runner CommandRunner
}

func (m *DarwinManager) plistLabel(service string) string {
	return fmt.Sprintf("com.livepeer.frameworks.%s", service)
}

func (m *DarwinManager) plistPath(service string) string {
	return fmt.Sprintf("/Library/LaunchDaemons/%s.plist", m.plistLabel(service))
}

func (m *DarwinManager) Start(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("launchctl bootstrap system %s", m.plistPath(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("launchctl bootstrap %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DarwinManager) Stop(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("launchctl bootout system/%s", m.plistLabel(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("launchctl bootout %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *DarwinManager) Restart(ctx context.Context, service string) error {
	if err := m.Stop(ctx, service); err != nil {
		return err
	}
	return m.Start(ctx, service)
}

func (m *DarwinManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	cmd := fmt.Sprintf("launchctl print system/%s 2>/dev/null", m.plistLabel(service))
	out, code, err := m.runner.Run(ctx, cmd)
	if err != nil {
		return ServiceStatus{}, err
	}
	if code != 0 {
		return ServiceStatus{Running: false, Detail: "not loaded"}, nil
	}
	running := strings.Contains(out, "state = running")
	detail := "loaded"
	if running {
		detail = "running"
	}
	return ServiceStatus{Running: running, Detail: detail}, nil
}
