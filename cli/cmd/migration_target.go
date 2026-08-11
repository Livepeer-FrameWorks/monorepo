package cmd

import (
	"fmt"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

// isConcreteVersion reports whether v is a concrete, well-formed vX.Y.Z release version (rejecting channel names like
// "stable" and malformed spellings such as v1.0.0- or v1.0.0-.). It routes through releases.ValidateVersion — the
// SINGLE shared version validator — so migration targets, release selectors, CLI-version checks, catalog entries, and
// fetched metadata all apply identical rules before any CompareSemver.
func isConcreteVersion(v string) bool {
	return releases.ValidateVersion(v) == nil
}

// resolveMigrationTarget returns a concrete vX.Y.Z target version for any
// command that consumes embedded SQL migrations. If explicit is set, it must
// already be concrete (channel names like "stable" are rejected). If empty,
// the resolver fetches the cluster's selected GitOps release manifest and
// reads PlatformVersion. A channel that does not yield a concrete version is
// a hard failure — operators must pass --to-version vX.Y.Z explicitly.
func resolveMigrationTarget(rc *resolvedCluster, explicit string) (string, error) {
	return resolveMigrationTargetFromParts(rc.Manifest, rc.ReleaseRepos, explicit)
}

// resolveMigrationTargetFromParts is for callers that don't have a
// resolvedCluster handy (e.g. helpers invoked from executeProvision).
func resolveMigrationTargetFromParts(manifest *inventory.Manifest, releaseRepos []string, explicit string) (string, error) {
	if explicit != "" {
		if !isConcreteVersion(explicit) {
			return "", fmt.Errorf("invalid target version %q: expected concrete vX.Y.Z (channel names like \"stable\" are not accepted here)", explicit)
		}
		return explicit, nil
	}

	channel := manifest.ResolvedChannel()
	resolvedChannel, version := gitops.ResolveVersion(channel)
	if !isConcreteVersion(version) {
		gm, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, releaseRepos, resolvedChannel, version)
		if err != nil {
			return "", fmt.Errorf("cannot resolve target version from cluster channel %q: %w; specify --to-version vX.Y.Z explicitly", channel, err)
		}
		version = gm.PlatformVersion
	}
	if !isConcreteVersion(version) {
		return "", fmt.Errorf("cannot resolve target version from cluster channel %q (got %q); specify --to-version vX.Y.Z explicitly", channel, version)
	}
	return version, nil
}
