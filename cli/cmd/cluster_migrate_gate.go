package cmd

import (
	"context"
	"fmt"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/datamigrate"

	"github.com/spf13/cobra"
)

// runPhaseDataMigrationGate refuses to apply SQL for phases whose safety
// depends on catalog-declared data migrations having completed first.
//
// Honest empty-state behavior:
//   - empty catalog → explicit "no required data migrations declared" line,
//     then proceeds.
//   - declared but unreportable → blocker, never silent pass.
func runPhaseDataMigrationGate(
	ctx context.Context,
	cmd *cobra.Command,
	rc *resolvedCluster,
	sshPool *ssh.Pool,
	phase string,
	targetVersion string,
	skip bool,
) error {
	if skip {
		fmt.Fprintf(cmd.OutOrStderr(), "[gate] WARNING: --skip-data-migration-check active; pre-%s gate bypassed.\n", phase)
		return nil
	}
	catalog := releases.Catalog()
	if len(catalog) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "[gate] release catalog is empty; no %s data migrations to check.\n", phase)
		return nil
	}
	reqs := preflight.CatalogRequirements(catalog, targetVersion)
	if len(reqs) == 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "[gate] no required data migrations declared up to %s.\n", targetVersion)
		return nil
	}
	src := preflight.SSHStateSource(sshPool, manifestHostFor(rc.Manifest), manifestRuntimeFor(rc.Manifest))
	blockers, err := datamigrate.PrePhaseBlockers(ctx, src, reqs, phase, targetVersion, releases.CompareSemver)
	if err != nil {
		return fmt.Errorf("[gate] check %s data migrations: %w", phase, err)
	}
	if len(blockers) > 0 {
		return fmt.Errorf("[gate] %s blocked: required data migrations not completed:\n%s\n\nrun: frameworks cluster data-migrate run <id>",
			phase, formatBlockers(blockers))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "[gate] %d required data migration(s) checked for %s.\n", len(reqs), phase)
	return nil
}
