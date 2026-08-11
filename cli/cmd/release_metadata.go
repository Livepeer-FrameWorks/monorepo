package cmd

import (
	"fmt"

	"frameworks/cli/internal/releases"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/spf13/cobra"
)

// newReleaseMetadataCmd emits the release-manifest compatibility metadata (min_cli_version + required_transitions +
// rollback_disabled) for a target platform version, as YAML lines the release pipeline appends to the published
// manifest. Publishing these in the FETCHED manifest is what lets an OUTDATED CLI fail closed before migrations (see
// validateFetchedReleaseCompatibility) and lets `cluster upgrade` honor per-release rollback policy; this command
// derives them from the SAME embedded catalog the CLI validates against, so publish and validate never drift. It reads
// only the embedded catalog — no cluster, SOPS, or network — so it is safe to run in CI.
func newReleaseMetadataCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "release-metadata <version>",
		Short:  "Emit release-manifest compatibility metadata (min_cli_version + required_transitions + rollback_disabled) for a version",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			version := args[0]
			// A release MUST be explicitly declared in the catalog before we publish its metadata. Otherwise an
			// undeclared target emits only accumulated transitions (no min_cli floor, no rollback policy) and silently
			// loses per-release policy — e.g. a v0.3.0 that never restates the Chandler rollback cut. Failing here
			// forces every new release to declare its entry (min_cli_version, rollback_disabled, required data migrations).
			if releases.Lookup(version) == nil {
				return fmt.Errorf("release-metadata: target %s is not declared in the release catalog (cli/internal/releases/catalog.yaml); add its entry — including min_cli_version and any rollback_disabled — before publishing", version)
			}
			ids, err := releases.RequiredTransitionIDs(version)
			if err != nil {
				return fmt.Errorf("resolve required transitions for %s: %w", version, err)
			}
			// Reject a catalog typo BEFORE emitting: an unknown deploy name would silently never match a real service,
			// so the intended rollback policy would be lost. Fail the release generation instead.
			rollbackDisabled := releases.RollbackDisabledFor(version)
			if err := validateRollbackDisabledNames(rollbackDisabled); err != nil {
				return fmt.Errorf("%s: %w", version, err)
			}
			out := cmd.OutOrStdout()
			if minCLI := releases.MinCLIVersionFor(version); minCLI != "" {
				fmt.Fprintf(out, "min_cli_version: %s\n", minCLI)
			}
			if len(ids) > 0 {
				fmt.Fprintln(out, "required_transitions:")
				for _, id := range ids {
					fmt.Fprintf(out, "  - %s\n", id)
				}
			}
			if len(rollbackDisabled) > 0 {
				fmt.Fprintln(out, "rollback_disabled:")
				for _, name := range rollbackDisabled {
					fmt.Fprintf(out, "  - %s\n", name)
				}
			}
			return nil
		},
	}
	return cmd
}

// validateRollbackDisabledNames rejects any rollback_disabled entry that is not a canonical servicedefs deploy id, so
// a catalog typo fails release generation rather than silently disabling rollback for nothing.
func validateRollbackDisabledNames(names []string) error {
	for _, name := range names {
		if _, ok := servicedefs.Lookup(name); !ok {
			return fmt.Errorf("rollback_disabled lists unknown deploy name %q (must be a canonical servicedefs id, e.g. chandler)", name)
		}
	}
	return nil
}
