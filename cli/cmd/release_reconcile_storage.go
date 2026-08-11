package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func init() {
	registerReleaseTransition(storageDescriptorAdoption{})
}

// storageDescriptorAdoption converges the invariant "Quartermaster holds the immutable S3 descriptor every live
// Foghorn replica in a media cell is running with". It runs AFTER Quartermaster is upgraded (so the adopt RPC +
// s3_prefix column exist) and BEFORE Foghorn/Chandler upgrade (so the authority is in place before the components that
// read it move). Completion is read from natural authoritative state — the descriptor on the cluster row — so a rerun
// after a later failure sees Complete.
type storageDescriptorAdoption struct{}

func (storageDescriptorAdoption) ID() string           { return "storage-descriptor-adoption" }
func (storageDescriptorAdoption) Title() string        { return "Storage descriptor adoption" }
func (storageDescriptorAdoption) IntroducedIn() string { return "v0.2.97" }
func (storageDescriptorAdoption) Irreversible() bool   { return true } // safe: the descriptor stays valid on rollback
func (storageDescriptorAdoption) AfterServices() []string {
	return []string{"quartermaster"}
}
func (storageDescriptorAdoption) BeforeServices() []string {
	return []string{"foghorn", "chandler"}
}

// ProvisionDisposition is ProvisionEstablishedByBootstrap: on a CLEAN provision, Quartermaster's desired-state
// bootstrap establishes the storage descriptor (prefix included) on the cluster row BEFORE the in-cell Foghorn/Chandler
// deploy (the planner orders Quartermaster first), so the adoption invariant already holds — there is nothing for the
// transition to converge at install time. The establishing service is Quartermaster (this transition's AfterServices),
// which provision confirms is part of the manifest. On an EXISTING cluster the release-apply path runs
// Check→Apply→Verify instead.
func (storageDescriptorAdoption) ProvisionDisposition() ProvisionDisposition {
	return ProvisionEstablishedByBootstrap
}

// Scopes: one per media cluster (a cluster that runs an enabled Foghorn).
func (storageDescriptorAdoption) Scopes(env *reconcileEnv) ([]ReconcileScope, error) {
	seen := map[string]bool{}
	var scopes []ReconcileScope
	for name, svc := range env.rc.Manifest.Services {
		if !svc.Enabled {
			continue
		}
		dn, err := resolveDeployName(name, svc)
		if err != nil || dn != "foghorn" {
			continue
		}
		cid := serviceUpgradeCluster(env.rc.Manifest, svc)
		if cid == "" || seen[cid] {
			continue
		}
		seen[cid] = true
		scopes = append(scopes, ReconcileScope{ClusterID: cid, Label: cid})
	}
	return scopes, nil
}

func (storageDescriptorAdoption) Check(ctx context.Context, env *reconcileEnv, scope ReconcileScope) ReconcileCheck {
	cr, err := env.qm.GetCluster(ctx, scope.ClusterID)
	if err != nil {
		return ReconcileCheck{ReconcileBlocked, "quartermaster unreachable: " + err.Error()}
	}
	cl := cr.GetCluster()
	if cl == nil {
		return ReconcileCheck{ReconcileBlocked, "quartermaster returned no cluster row for " + scope.ClusterID}
	}
	qmSet := strings.TrimSpace(cl.GetS3Bucket()) != ""

	hosts := foghornHostsInCluster(env.rc.Manifest, scope.ClusterID)
	if len(hosts) == 0 {
		return ReconcileCheck{ReconcileNotApplicable, "no foghorn replicas in cluster"}
	}

	consensus, cerr := deployedDescriptorConsensus(ctx, env.sshPool, hosts)
	switch {
	case cerr == nil:
		if !qmSet {
			return ReconcileCheck{ReconcilePending, fmt.Sprintf("quartermaster empty; %d replica(s) agree on bucket=%q prefix=%q", len(hosts), consensus.bucket, consensus.prefix)}
		}
		// The frozen part (bucket/endpoint/region) must match — a difference is an immutable-backend repoint.
		if cl.GetS3Bucket() != consensus.bucket || cl.GetS3Endpoint() != consensus.endpoint ||
			effectiveS3Region(cl.GetS3Region()) != effectiveS3Region(consensus.region) {
			return ReconcileCheck{ReconcileBlocked, fmt.Sprintf("quartermaster descriptor (bucket=%q endpoint=%q region=%q) differs from the deployed replicas (bucket=%q endpoint=%q region=%q) — an immutable-backend repoint; resolve by hand", cl.GetS3Bucket(), cl.GetS3Endpoint(), cl.GetS3Region(), consensus.bucket, consensus.endpoint, consensus.region)}
		}
		// Prefix: a NULL (not-present) prefix on an established row is the pre-migration "unadopted" state — a one-time
		// fill is due (Pending), NOT a repoint. Only once the prefix is present is it frozen and compared exactly.
		if !cl.GetS3PrefixPresent() {
			return ReconcileCheck{ReconcilePending, fmt.Sprintf("quartermaster bucket established but s3_prefix is unadopted (NULL); one-time fill to %q", consensus.prefix)}
		}
		if cl.GetS3Prefix() != consensus.prefix {
			return ReconcileCheck{ReconcileBlocked, fmt.Sprintf("quartermaster adopted prefix=%q differs from the deployed replicas prefix=%q — an immutable-prefix repoint; resolve by hand", cl.GetS3Prefix(), consensus.prefix)}
		}
		return ReconcileCheck{ReconcileComplete, "quartermaster matches the deployed descriptor"}
	case errors.Is(cerr, errNoDeployedS3Backend):
		if qmSet {
			return ReconcileCheck{ReconcileBlocked, "quartermaster declares a descriptor but the deployed foghorn runs no S3 backend"}
		}
		return ReconcileCheck{ReconcileNotApplicable, "S3 not configured on this cell"}
	case errors.Is(cerr, errReplicaDescriptorDisagreement):
		return ReconcileCheck{ReconcileBlocked, cerr.Error()}
	case errors.Is(cerr, errFoghornNotDeployed):
		// POSITIVE not-deployed signal: no Foghorn is running to contradict — or to adopt from. Greenfield Complete
		// requires a COMPLETE descriptor (prefix present): with no live Foghorn there is no safe source to fill a
		// missing prefix, and the Foghorn that eventually boots would fail closed on the NULL prefix anyway.
		switch {
		case qmSet && cl.GetS3PrefixPresent():
			return ReconcileCheck{ReconcileComplete, "quartermaster descriptor established (complete) and no Foghorn is deployed yet (greenfield)"}
		case qmSet:
			return ReconcileCheck{ReconcileBlocked, "quartermaster has a bucket but s3_prefix is unadopted (NULL) and no Foghorn is deployed to adopt from — establish the complete descriptor via desired-state bootstrap first"}
		default:
			return ReconcileCheck{ReconcileNotApplicable, "no Foghorn deployed and no quartermaster descriptor — nothing to adopt yet"}
		}
	default:
		// The Foghorn IS deployed but could not be read (transport/unreachable). This is NOT proof of greenfield or of
		// backend agreement — fail closed rather than assume.
		return ReconcileCheck{ReconcileBlocked, "deployed Foghorn replicas could not be read to confirm backend agreement: " + cerr.Error()}
	}
}

func (storageDescriptorAdoption) Apply(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error {
	hosts := foghornHostsInCluster(env.rc.Manifest, scope.ClusterID)
	consensus, err := deployedDescriptorConsensus(ctx, env.sshPool, hosts)
	if err != nil {
		return fmt.Errorf("re-read deployed descriptor: %w", err)
	}
	if strings.TrimSpace(env.operatorJWT) == "" {
		return fmt.Errorf("storage adoption requires a platform-operator login: no operator JWT is available")
	}
	callCtx := context.WithValue(ctx, ctxkeys.KeyJWTToken, env.operatorJWT)
	// adoptDeployedDescriptor reconciles into Quartermaster and verifies the immediate read-back.
	if err := adoptDeployedDescriptor(callCtx, env.cmd, env.qm, scope.ClusterID, consensus); err != nil {
		return err
	}
	// Keep the same release run's in-memory manifest consistent so the manifest-based Foghorn pre-deploy gate agrees.
	syncClusterDescriptorToManifest(env.rc, scope.ClusterID, consensus)
	return nil
}

// ConvergeSideState keeps the manifest cluster descriptor in step with the AUTHORITATIVE Quartermaster row. It runs on
// Complete scopes so a resumed release (where adoption already happened, perhaps under a discarded temporary manifest)
// repairs the manifest before the manifest-based Foghorn pre-deploy gate reads it. Sourced from Quartermaster (the
// authority), not the deployed replicas.
func (storageDescriptorAdoption) ConvergeSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error {
	cr, err := env.qm.GetCluster(ctx, scope.ClusterID)
	if err != nil {
		return fmt.Errorf("get cluster: %w", err)
	}
	cl := cr.GetCluster()
	if cl == nil || strings.TrimSpace(cl.GetS3Bucket()) == "" {
		return nil // no authoritative descriptor to reflect
	}
	syncClusterDescriptorToManifest(env.rc, scope.ClusterID, deployedDescriptor{
		bucket:   cl.GetS3Bucket(),
		endpoint: cl.GetS3Endpoint(),
		region:   cl.GetS3Region(),
		prefix:   cl.GetS3Prefix(),
	})
	return nil
}

// PreviewSideState overlays the descriptor a Pending Apply WOULD adopt (the deployed replica consensus) onto the
// in-memory manifest, never persisting. In a dry-run this lets the downstream (manifest-based) Foghorn gate see the
// would-be descriptor instead of the stale empty row.
func (storageDescriptorAdoption) PreviewSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error {
	hosts := foghornHostsInCluster(env.rc.Manifest, scope.ClusterID)
	consensus, err := deployedDescriptorConsensus(ctx, env.sshPool, hosts)
	if err != nil {
		return fmt.Errorf("preview: re-read deployed descriptor: %w", err)
	}
	syncClusterDescriptorToManifest(env.rc, scope.ClusterID, consensus) // in-memory only
	return nil
}

func (storageDescriptorAdoption) Verify(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error {
	cr, err := env.qm.GetCluster(ctx, scope.ClusterID)
	if err != nil {
		return fmt.Errorf("get cluster: %w", err)
	}
	hosts := foghornHostsInCluster(env.rc.Manifest, scope.ClusterID)
	consensus, cerr := deployedDescriptorConsensus(ctx, env.sshPool, hosts)
	if cerr != nil {
		return fmt.Errorf("re-read deployed descriptor: %w", cerr)
	}
	if !qmMatchesDeployed(cr.GetCluster(), consensus) {
		return fmt.Errorf("quartermaster descriptor does not match the deployed replicas after adopt")
	}
	return nil
}

// qmMatchesDeployed reports whether a Quartermaster cluster row is FULLY adopted and consistent with a deployed
// descriptor: bucket/endpoint/prefix exact, region on its effective value, AND the prefix PRESENT (non-NULL) — a NULL
// prefix means the descriptor is not yet fully adopted even if the COALESCE'd value happens to equal the deployed one.
// Used by Verify to confirm the post-adopt state.
func qmMatchesDeployed(cl *quartermasterpb.InfrastructureCluster, d deployedDescriptor) bool {
	if cl == nil {
		return false
	}
	return cl.GetS3Bucket() == d.bucket &&
		cl.GetS3Endpoint() == d.endpoint &&
		effectiveS3Region(cl.GetS3Region()) == effectiveS3Region(d.region) &&
		cl.GetS3PrefixPresent() &&
		cl.GetS3Prefix() == d.prefix
}
