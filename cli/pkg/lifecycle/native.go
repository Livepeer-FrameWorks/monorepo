package lifecycle

import (
	"context"
	"fmt"
	"strings"
)

// NativeManager manages services via systemd (Linux).
type NativeManager struct {
	runner CommandRunner
}

func (m *NativeManager) unitName(service string) string {
	return fmt.Sprintf("frameworks-%s", service)
}

func (m *NativeManager) Start(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("systemctl start %s", m.unitName(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("systemctl start %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *NativeManager) Stop(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("systemctl stop %s", m.unitName(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("systemctl stop %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *NativeManager) Restart(ctx context.Context, service string) error {
	cmd := fmt.Sprintf("systemctl restart %s", m.unitName(service))
	_, code, err := m.runner.Run(ctx, cmd)
	if err != nil || code != 0 {
		return fmt.Errorf("systemctl restart %s failed (exit %d): %w", service, code, err)
	}
	return nil
}

func (m *NativeManager) Status(ctx context.Context, service string) (ServiceStatus, error) {
	cmd := fmt.Sprintf("systemctl is-active %s", m.unitName(service))
	out, _, err := m.runner.Run(ctx, cmd)
	if err != nil {
		return ServiceStatus{}, err
	}
	state := strings.TrimSpace(out)
	return ServiceStatus{Running: state == "active", Detail: state}, nil
}
