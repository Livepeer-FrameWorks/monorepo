package provisioner

import (
	"context"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
)

func caddyRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	version := firstNonEmpty(config.Version, metaString(config.Metadata, "version"))
	if version == "" || version == "stable" || version == "candidate" || version == "latest" || version == "rc" {
		version = "2.8.4"
	}

	vars := map[string]any{
		"caddy_version": version,
		"caddy_credential_uri_redaction": map[string]any{
			"regexp":      ingress.CredentialURIRegexp,
			"replacement": ingress.CredentialURIReplacement,
		},
	}

	if sites := proxySiteMapsForMode(config.Metadata, "native"); len(sites) > 0 {
		vars["caddy_sites"] = sites
	}
	if email, ok := config.Metadata["tls_email"].(string); ok && email != "" {
		vars["caddy_global_options"] = map[string]any{"email": email}
	}
	return vars, nil
}
