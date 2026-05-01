package cmd

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/preflight"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/datamigrate"
	fwv "frameworks/pkg/version"

	"github.com/spf13/cobra"
)

// runUpgradePreDeployGate is the pre-deploy gate every cluster upgrade goes
// through:
//
//	(a) every expand migration up to targetPlatformVersion must be applied
//	(b) every required postdeploy migration in the catalog path EXCLUDING
//	    targetPlatformVersion must be applied — target postdeploy waits
//	    until after deploy + data migration
//	(c) prior-version required data migrations must be completed
//	(d) catalog hard-skip rules (compatible_from, requires_intermediate,
//	    min_cli_version) must be satisfied
//
// DB ownership is explicit catalog metadata. Generated DATABASE_* env vars are
// intentionally ignored because they are injected more broadly than ownership.
func runUpgradePreDeployGate(
	ctx context.Context,
	cmd *cobra.Command,
	rc *resolvedCluster,
	sshPool *ssh.Pool,
	manifest *inventory.Manifest,
	targetPlatformVersion string,
	currentPlatformVersion string,
	serviceName string,
	skipMigrationCheck bool,
	skipDataMigrationCheck bool,
) error {
	dbName := releases.ServiceDatabase(serviceName)
	if dbName == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[gate] %s is not declared as a DB-backed service in the release catalog; skipping migration gate.\n", serviceName)
		return nil
	}

	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled {
		fmt.Fprintf(cmd.OutOrStdout(), "\n[gate] postgres not enabled in manifest; skipping migration gate.\n")
		return nil
	}

	dbHost, ok := resolvePGHost(manifest, pg)
	if !ok {
		return fmt.Errorf("[gate] cannot resolve postgres host from manifest")
	}

	password, _ := resolveYugabytePassword(pg, sharedEnvForGate(rc)) //nolint:errcheck // missing yugabyte password is reported by ReadMigrationLedger

	fmt.Fprintf(cmd.OutOrStdout(), "\n[gate] Pre-deploy migration gate (target %s, current platform %s)\n", targetPlatformVersion, currentPlatformVersion)

	// Catalog hard-skip refusals.
	if catalogErr := enforceCatalogPath(cmd, currentPlatformVersion, targetPlatformVersion); catalogErr != nil {
		return catalogErr
	}

	// (a) expand <= target.
	if !skipMigrationCheck {
		missingExpand, err := provisioner.MissingMigrations(ctx, sshPool, dbHost, pg, password, []string{dbName}, "expand", targetPlatformVersion)
		if err != nil {
			return fmt.Errorf("[gate] check expand migrations: %w", err)
		}
		// (b) prior REQUIRED postdeploy in the path, excluding target.
		var missingPriorPostdeploy []provisioner.MigrationKey
		path, pathErr := releases.Path(currentPlatformVersion, targetPlatformVersion)
		if pathErr != nil {
			return fmt.Errorf("[gate] %w", pathErr)
		}
		for _, rel := range path {
			if rel.Version == targetPlatformVersion {
				continue
			}
			missing, err := provisioner.MissingMigrations(ctx, sshPool, dbHost, pg, password, []string{dbName}, "postdeploy", rel.Version)
			if err != nil {
				return fmt.Errorf("[gate] check postdeploy at %s: %w", rel.Version, err)
			}
			missingPriorPostdeploy = append(missingPriorPostdeploy, missing...)
		}
		if total := len(missingExpand) + len(missingPriorPostdeploy); total > 0 {
			return fmt.Errorf("[gate] migrations required before deploying %s %s:\n%s\n\nrun: frameworks cluster migrate --phase expand --to-version %s",
				serviceName, targetPlatformVersion,
				formatMissingMigrations(missingExpand, missingPriorPostdeploy),
				targetPlatformVersion)
		}
	} else {
		fmt.Fprintln(cmd.OutOrStderr(), "[gate] WARNING: --skip-migration-check active; pre-deploy schema gate bypassed.")
	}

	// (c) prior required data migrations.
	if !skipDataMigrationCheck {
		catalog := releases.Catalog()
		if len(catalog) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "[gate] release catalog is empty; no prior data migrations to evaluate.")
		} else {
			reqs := preflight.CatalogRequirements(catalog, targetPlatformVersion)
			if len(reqs) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "[gate] no required data migrations declared up to %s.\n", targetPlatformVersion)
			} else {
				src := preflight.SSHStateSource(sshPool, manifestHostFor(manifest), manifestRuntimeFor(manifest))
				blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, currentPlatformVersion, targetPlatformVersion, releases.CompareSemver)
				if err != nil {
					return fmt.Errorf("[gate] check prior data migrations: %w", err)
				}
				if len(blockers) > 0 {
					return fmt.Errorf("[gate] prior data migrations required before deploying %s %s:\n%s\n\nrun: frameworks cluster data-migrate run <id>",
						serviceName, targetPlatformVersion, formatBlockers(blockers))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "[gate] %d prior required data migration(s) completed.\n", len(reqs))
			}
		}
	} else {
		fmt.Fprintln(cmd.OutOrStderr(), "[gate] WARNING: --skip-data-migration-check active; prior data migration gate bypassed.")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "[gate] OK — proceeding with deploy.")
	return nil
}

// enforceCatalogPath refuses direct upgrades that the catalog declares
// invalid. Empty catalog ⇒ no-op (honest "no catalog declared" signal).
func enforceCatalogPath(cmd *cobra.Command, current, target string) error {
	if len(releases.Catalog()) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "[gate] release catalog is empty; no catalog-path constraints to enforce.")
		return nil
	}
	if strings.TrimSpace(current) == "" {
		return fmt.Errorf("[gate] cannot determine current platform version for a DB-backed upgrade; update the running service or cluster metadata before upgrading to %s", target)
	}
	tgt := releases.Lookup(target)
	if tgt == nil {
		return fmt.Errorf("[gate] target release %s is not declared in the embedded release catalog; update the CLI/catalog before upgrading a DB-backed service", target)
	}
	if err := enforceMinCLIVersion(tgt.MinCLIVersion); err != nil {
		return err
	}
	if _, err := releases.Path(current, target); err != nil {
		return fmt.Errorf("[gate] %w", err)
	}
	return nil
}

func enforceMinCLIVersion(required string) error {
	required = strings.TrimSpace(required)
	if required == "" {
		return nil
	}
	current := strings.TrimSpace(fwv.Version)
	if current == "" {
		return fmt.Errorf("[gate] release requires CLI >= %s, but this CLI has no embedded version", required)
	}
	if current == "dev" {
		return nil
	}
	if !concreteVersionPattern.MatchString(current) {
		return fmt.Errorf("[gate] release requires CLI >= %s, but this CLI reports non-concrete version %q", required, current)
	}
	if releases.CompareSemver(current, required) < 0 {
		return fmt.Errorf("[gate] release requires CLI >= %s; current CLI is %s", required, current)
	}
	return nil
}

// resolvePGHost returns the host running the postgres / yugabyte primary,
// matching the resolution used by `cluster migrate`.
func resolvePGHost(manifest *inventory.Manifest, pg *inventory.PostgresConfig) (inventory.Host, bool) {
	if pg.IsYugabyte() && len(pg.Nodes) > 0 {
		return manifest.GetHost(pg.Nodes[0].Host)
	}
	return manifest.GetHost(pg.Host)
}

// sharedEnvForGate returns the shared env map if it's already loaded; the
// gate cannot tolerate a SOPS failure mid-flight — runUpgrade has already
// required a successful load before reaching the gate.
func sharedEnvForGate(rc *resolvedCluster) map[string]string {
	env, _ := rc.SharedEnv() //nolint:errcheck // already validated upstream in runUpgrade
	return env
}

// manifestHostFor returns a HostResolver for the cluster CLI: services
// listed in manifest.Services / Interfaces resolve to their declared host.
func manifestHostFor(manifest *inventory.Manifest) preflight.HostResolver {
	return func(service string) (inventory.Host, bool) {
		if svc, ok := manifest.Services[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		if svc, ok := manifest.Interfaces[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		if svc, ok := manifest.Observability[service]; ok && svc.Enabled {
			return firstServiceHost(manifest, svc)
		}
		return inventory.Host{}, false
	}
}

func manifestRuntimeFor(manifest *inventory.Manifest) preflight.RuntimeResolver {
	return func(service string) string {
		if svc, ok := manifest.Services[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		if svc, ok := manifest.Interfaces[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		if svc, ok := manifest.Observability[service]; ok && svc.Enabled {
			return serviceRuntimeName(service, svc)
		}
		return service
	}
}

func firstServiceHost(manifest *inventory.Manifest, svc inventory.ServiceConfig) (inventory.Host, bool) {
	if svc.Host != "" {
		return manifest.GetHost(svc.Host)
	}
	if len(svc.Hosts) > 0 {
		return manifest.GetHost(svc.Hosts[0])
	}
	return inventory.Host{}, false
}

func serviceRuntimeName(service string, svc inventory.ServiceConfig) string {
	if strings.TrimSpace(svc.Deploy) != "" {
		return strings.TrimSpace(svc.Deploy)
	}
	return service
}

func formatMissingMigrations(expand, postdeploy []provisioner.MigrationKey) string {
	var b strings.Builder
	if len(expand) > 0 {
		b.WriteString("  expand:\n")
		for _, m := range expand {
			fmt.Fprintf(&b, "    - %s\n", m.String())
		}
	}
	if len(postdeploy) > 0 {
		b.WriteString("  prior postdeploy:\n")
		for _, m := range postdeploy {
			fmt.Fprintf(&b, "    - %s\n", m.String())
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatBlockers(blockers []datamigrate.Blocker) string {
	var b strings.Builder
	for _, blk := range blockers {
		fmt.Fprintf(&b, "  - %s/%s (introduced %s): %s\n",
			blk.Requirement.Service, blk.Requirement.ID, blk.Requirement.IntroducedIn, blk.Reason)
	}
	return strings.TrimRight(b.String(), "\n")
}
