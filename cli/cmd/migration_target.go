package cmd

import (
	"fmt"
	"regexp"

	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
)

var concreteVersionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+([.\-+].*)?$`)

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
		if !concreteVersionPattern.MatchString(explicit) {
			return "", fmt.Errorf("invalid target version %q: expected concrete vX.Y.Z (channel names like \"stable\" are not accepted here)", explicit)
		}
		return explicit, nil
	}

	channel := manifest.ResolvedChannel()
	resolvedChannel, version := gitops.ResolveVersion(channel)
	if !concreteVersionPattern.MatchString(version) {
		gm, err := gitops.FetchFromRepositories(gitops.FetchOptions{}, releaseRepos, resolvedChannel, version)
		if err != nil {
			return "", fmt.Errorf("cannot resolve target version from cluster channel %q: %w; specify --to-version vX.Y.Z explicitly", channel, err)
		}
		version = gm.PlatformVersion
	}
	if !concreteVersionPattern.MatchString(version) {
		return "", fmt.Errorf("cannot resolve target version from cluster channel %q (got %q); specify --to-version vX.Y.Z explicitly", channel, version)
	}
	return version, nil
}
