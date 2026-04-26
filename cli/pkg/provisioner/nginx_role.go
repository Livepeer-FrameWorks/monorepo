package provisioner

import (
	"context"

	"frameworks/cli/pkg/inventory"
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
	}, nil
}
