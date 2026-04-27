package provisioner

import (
	"context"

	"frameworks/cli/pkg/inventory"
)

func caddyRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" || version == "stable" || version == "latest" {
		version = "2.8.4"
	}

	vars := map[string]any{
		"caddy_version": version,
	}

	if sites := proxySiteMapsForMode(config.Metadata, "native"); len(sites) > 0 {
		vars["caddy_sites"] = sites
	}
	if email, ok := config.Metadata["tls_email"].(string); ok && email != "" {
		vars["caddy_global_options"] = map[string]any{"email": email}
	}
	return vars, nil
}
