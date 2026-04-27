package provisioner

import (
	"context"

	"frameworks/cli/pkg/inventory"
	"frameworks/pkg/ingress"
)

func nginxRoleVars(_ context.Context, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	port := config.Port
	if port == 0 {
		port = ServicePorts["nginx"]
	}
	httpsPort := metaInt(config.Metadata, "https_port")
	if httpsPort == 0 {
		httpsPort = 443
	}
	return map[string]any{
		"nginx_http_port":  port,
		"nginx_https_port": httpsPort,
		"nginx_sites":      proxySiteMapsForMode(config.Metadata, "native"),
		// Authoritative source for the on-host paths Privateer also writes
		// to. The Ansible role has matching defaults so molecule and
		// standalone invocations still work, but in CLI-driven runs these
		// values from pkg/ingress always win.
		"nginx_ingress_tls_root":       ingress.TLSRoot,
		"nginx_ingress_reload_trigger": ingress.ReloadTrigger,
	}, nil
}
