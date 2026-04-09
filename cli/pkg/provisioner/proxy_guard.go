package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/inventory"
)

// ensurePublicProxyPortsSafe refuses to provision a proxy onto a host where
// ports 80/443 are already occupied by something other than the already-managed
// target service. This prevents the CLI from clobbering unrelated manual web
// stacks like a forum proxy.
func ensurePublicProxyPortsSafe(ctx context.Context, base *BaseProvisioner, host inventory.Host, serviceName, mode string) error {
	state, err := base.CheckExists(ctx, host, serviceName)
	if err == nil && state != nil && state.Exists && state.Running {
		return nil
	}

	result, err := base.RunCommand(ctx, host, "ss -tlnp 2>/dev/null | grep -E ':(80|443) ' || true")
	if err != nil {
		return fmt.Errorf("failed to inspect public proxy ports: %w", err)
	}

	if strings.TrimSpace(result.Stdout) == "" {
		return nil
	}

	listeners := strings.ToLower(strings.TrimSpace(result.Stdout))
	switch serviceName {
	case "nginx":
		if mode == "native" && strings.Contains(listeners, "nginx") {
			layout, probeErr := base.RunCommand(ctx, host, `
if [ -f /etc/nginx/nginx.conf ] && systemctl show -p FragmentPath nginx 2>/dev/null | grep -q '='; then
  echo PACKAGED
else
  echo CUSTOM
fi`)
			if probeErr != nil {
				return fmt.Errorf("failed to inspect existing nginx layout: %w", probeErr)
			}
			if strings.TrimSpace(layout.Stdout) == "PACKAGED" {
				return nil
			}
			return fmt.Errorf("refusing to provision nginx on %s: an unmanaged nginx is already listening on 80/443", host.ExternalIP)
		}
	case "caddy":
		if mode == "native" && strings.Contains(listeners, "caddy") {
			layout, probeErr := base.RunCommand(ctx, host, `
if [ -f /etc/caddy/Caddyfile ] && systemctl show -p FragmentPath caddy 2>/dev/null | grep -q '='; then
  echo PACKAGED
else
  echo CUSTOM
fi`)
			if probeErr != nil {
				return fmt.Errorf("failed to inspect existing caddy layout: %w", probeErr)
			}
			if strings.TrimSpace(layout.Stdout) == "PACKAGED" {
				return nil
			}
			return fmt.Errorf("refusing to provision caddy on %s: an unmanaged caddy is already listening on 80/443", host.ExternalIP)
		}
	}

	if mode == "docker" {
		return fmt.Errorf(
			"refusing to provision %s in docker mode on %s: ports 80/443 are already occupied.\n"+
				"Active listeners:\n%s",
			serviceName,
			host.ExternalIP,
			strings.TrimSpace(result.Stdout),
		)
	}
	return fmt.Errorf(
		"refusing to provision %s on %s: ports 80/443 are already occupied by a different service.\n"+
			"Inspect or migrate the current proxy first, or skip the interface phase on this host.\n"+
			"Active listeners:\n%s",
		serviceName,
		host.ExternalIP,
		strings.TrimSpace(result.Stdout),
	)
}
