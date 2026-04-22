package cmd

import (
	"fmt"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/ux"

	"github.com/spf13/cobra"
)

// rememberInActiveContext mutates the active context and writes it back.
// Silent no-op when no context is configured (a fresh machine with only
// explicit flags must work). A save failure is logged as a faint warning
// but never masks the command's outcome — the command already succeeded,
// the persistence is just a side-channel for future runs.
//
// NOTE: read-path resolvers (resolveClusterManifest) must not call this;
// persistence belongs to success paths only, so dry-runs and --help
// paths don't haunt the operator with speculative defaults.
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
