package cmd

import (
	"fmt"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

// rememberInActiveContext mutates the active context and writes it back.
// No-op when no context is configured. A save failure is reported as a
// warning but does not fail the caller. Call only from success paths.
func rememberInActiveContext(cmd *cobra.Command, mutate func(*fwcfg.Context)) {
	cfg, err := fwcfg.Load()
	if err != nil {
		return
	}
	rt := fwcfg.GetRuntimeOverrides()
	active, err := fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, cfg)
	if err != nil || active.Name == "" {
		return
	}
	ctx, ok := cfg.Contexts[active.Name]
	if !ok {
		return
	}
	if ctx.Name == "" {
		ctx.Name = active.Name
	}
	mutate(&ctx)
	cfg.Contexts[active.Name] = ctx
	if err := fwcfg.Save(cfg); err != nil && cmd != nil {
		ux.Warn(cmd.ErrOrStderr(), fmt.Sprintf("context persistence skipped: %v (future commands will need explicit flags)", err))
	}
}

// rememberLastManifest persists the manifest path into the active context.
func rememberLastManifest(cmd *cobra.Command, path string) {
	if path == "" {
		return
	}
	rememberInActiveContext(cmd, func(c *fwcfg.Context) {
		c.LastManifestPath = path
	})
}

// rememberSystemTenantID persists the freshly-provisioned system tenant ID.
func rememberSystemTenantID(cmd *cobra.Command, tenantID string) {
	if tenantID == "" {
		return
	}
	rememberInActiveContext(cmd, func(c *fwcfg.Context) {
		c.SystemTenantID = tenantID
	})
}
