package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// runningInstance is one platform service replica and the release its binary reports.
type runningInstance struct {
	Service string
	Host    string
	Version string
}

// serviceDetector reads what one host runs for a deploy name.
type serviceDetector interface {
	Detect(ctx context.Context, serviceName string) (*detect.ServiceState, error)
}

// Source-floor seams. Production reads the embedded catalog and detects over SSH; tests substitute a fixture catalog
// and a scripted fleet.
var (
	sourceFloorsDeclaredFn  = releases.SourceFloorsDeclared
	sourceFloorForFn        = releases.MinSourceVersionFor
	detectRunningVersionsFn = detectRunningVersions
	newServiceDetectorFn    = func(pool *ssh.Pool, host inventory.Host) serviceDetector {
		return detect.NewDetector(pool, host)
	}
)

// enforceSourceFloor refuses a move to `target` while any platform service replica runs a release outside the floor
// the catalog declares for that move. The running versions come from host detection, which is the observed-version
// authority for control-plane hosts; nothing is recorded. When no catalog release declares min_source_version the
// check reads nothing. A cluster verified for a target is not re-read for the next service in the same run.
func enforceSourceFloor(ctx context.Context, out io.Writer, rc *resolvedCluster, sshPool *ssh.Pool, target string) error {
	if !sourceFloorsDeclaredFn() {
		return nil
	}
	if rc.sourceFloorVerifiedFor == target {
		return nil
	}
	instances, err := detectRunningVersionsFn(ctx, sshPool, rc.Manifest)
	if err != nil {
		return fmt.Errorf("[source floor] read running release versions: %w", err)
	}
	if err := sourceFloorRefusal(instances, target, sourceFloorForFn); err != nil {
		return err
	}
	floor, _ := sourceFloorForFn(target)
	if floor == "" {
		floor = "none"
	}
	fmt.Fprintf(out, "[source floor] %d running instance(s) are within the release floor for %s (min_source_version: %s).\n", len(instances), target, floor)
	rc.sourceFloorVerifiedFor = target
	return nil
}

// describeSourceFloor renders the source floor of a target for `upgrade plan`.
func describeSourceFloor(target string) string {
	if target == "" {
		return "unknown (target version unresolved)"
	}
	floor, known := sourceFloorForFn(target)
	switch {
	case !known:
		return fmt.Sprintf("unknown (%s is not in this CLI's release catalog)", target)
	case floor == "":
		return fmt.Sprintf("none; any declared release may move to %s", target)
	default:
		return fmt.Sprintf("every running replica must be on %s or later before moving to %s", floor, target)
	}
}

// sourceFloorRefusal checks every running instance against the move to target. Moving up from S to T requires
// S >= floor(T); moving down from S to T requires T >= floor(S), because the components of S assume what floor(S)
// guarantees and a rollback below it would break them. Versions are compared by release base, after stripping a
// `git describe` suffix. An instance whose version cannot be read as a release, or whose release is newer than this
// CLI's catalog, is refused because its floor cannot be evaluated.
func sourceFloorRefusal(instances []runningInstance, target string, floorFor func(string) (string, bool)) error {
	targetBase := releases.BaseVersion(target)
	targetFloor, known := floorFor(targetBase)
	if !known {
		return fmt.Errorf("[source floor] target %s is not declared in this CLI's release catalog; upgrade the frameworks CLI", target)
	}
	var problems []string
	for _, inst := range instances {
		where := fmt.Sprintf("%s on %s", inst.Service, inst.Host)
		base, ok := runningReleaseBase(inst.Version)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s reports %q, which is not a release version; redeploy it from a release before moving to %s", where, inst.Version, target))
			continue
		}
		switch cmp := releases.CompareSemver(base, targetBase); {
		case cmp < 0:
			if targetFloor != "" && releases.CompareSemver(base, targetFloor) < 0 {
				problems = append(problems, fmt.Sprintf("%s runs %s, below min_source_version %s of %s; apply %s first", where, base, targetFloor, target, targetFloor))
			}
		case cmp > 0:
			runningFloor, runningKnown := floorFor(base)
			if !runningKnown {
				problems = append(problems, fmt.Sprintf("%s runs %s, which this CLI's release catalog does not declare; upgrade the frameworks CLI", where, base))
				continue
			}
			if runningFloor != "" && releases.CompareSemver(targetBase, runningFloor) < 0 {
				problems = append(problems, fmt.Sprintf("%s runs %s, whose min_source_version is %s; moving back to %s is not supported, roll back no further than %s", where, base, runningFloor, target, runningFloor))
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("[source floor] refusing to move the cluster to %s:\n  - %s", target, strings.Join(problems, "\n  - "))
}

// runningReleaseBase reads a detected version as a release: a `git describe` suffix is stripped, a missing leading v is
// added, and a prerelease counts as its base release (v0.3.11-rc1 is v0.3.11). Empty, "dev", channel names, and
// digests are not releases.
func runningReleaseBase(version string) (string, bool) {
	v := releases.StripGitDescribeSuffix(strings.TrimSpace(version))
	if v != "" && !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if releases.ValidateVersion(v) != nil {
		return "", false
	}
	return releases.BaseVersion(v), true
}

// detectRunningVersions reads the release every platform-artifact replica in the manifest runs. Managed dependencies
// and host infrastructure are versioned independently and are skipped. A replica that is not installed contributes
// nothing; a detection error fails the whole read so the floor check fails closed.
func detectRunningVersions(ctx context.Context, sshPool *ssh.Pool, manifest *inventory.Manifest) ([]runningInstance, error) {
	var ids []string
	for id, svc := range manifest.Services {
		if svc.Enabled {
			ids = append(ids, id)
		}
	}
	for id, svc := range manifest.Interfaces {
		if svc.Enabled {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	var instances []runningInstance
	for _, id := range ids {
		deployName, err := classificationDeployName(manifest, id)
		if err != nil {
			return nil, fmt.Errorf("resolve deploy name for %s: %w", id, err)
		}
		if servicedefs.DeliveryClassFor(deployName) != servicedefs.DeliveryPlatformArtifact {
			continue
		}
		hosts, _ := resolveUpgradeHosts(manifest, id)
		for _, host := range hosts {
			state, err := newServiceDetectorFn(sshPool, host).Detect(ctx, deployName)
			if err != nil {
				return nil, fmt.Errorf("detect %s on %s: %w", deployName, host.Name, err)
			}
			if state == nil || !state.Exists {
				continue
			}
			instances = append(instances, runningInstance{Service: deployName, Host: firstNonEmpty(host.Name, host.ExternalIP), Version: state.Version})
		}
	}
	return instances, nil
}
