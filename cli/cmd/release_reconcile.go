package cmd

import (
	"context"

	"frameworks/cli/pkg/ssh"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

// ReconcileStatus is the outcome of a transition's Check. It is derived from the NATURAL authoritative state (there is
// deliberately no separate "done" ledger): a transition re-checks reality every run, so a rerun after a later failure
// correctly sees an already-applied transition as Complete.
type ReconcileStatus string

const (
	// ReconcileComplete: the invariant already holds — nothing to do.
	ReconcileComplete ReconcileStatus = "complete"
	// ReconcilePending: the invariant does not hold yet but Apply can establish it safely.
	ReconcilePending ReconcileStatus = "pending"
	// ReconcileBlocked: the invariant cannot be established without human intervention (conflicting state, an existing
	// value that differs, or the authority is unavailable). The release halts.
	ReconcileBlocked ReconcileStatus = "blocked"
	// ReconcileNotApplicable: the transition does not apply to this scope (e.g. S3 not configured for the cell).
	ReconcileNotApplicable ReconcileStatus = "not_applicable"
)

// ReconcileScope is one unit a transition converges independently — e.g. a single media cluster. A transition with no
// applicable scopes is a whole-transition no-op.
type ReconcileScope struct {
	ClusterID string
	Label     string
}

// ReconcileCheck is Check's verdict for one scope, with a human-readable reason for the plan/dry-run output.
type ReconcileCheck struct {
	Status ReconcileStatus
	Detail string
}

// reconcileQM is the narrow Quartermaster surface transitions use: a read (Check/Verify) plus the operator-authorized
// descriptor adopt (Apply). *qmclient.GRPCClient satisfies it; tests inject a fake.
type reconcileQM interface {
	storageAdoptQM
	GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
}

// reconcileEnv carries everything a transition needs to observe and mutate cluster state. The QM client and operator
// JWT are built once by the release executor and shared across transitions.
type reconcileEnv struct {
	cmd         *cobra.Command
	rc          *resolvedCluster
	sshPool     *ssh.Pool
	qm          reconcileQM
	operatorJWT string
	// dryRun: side-state convergence updates in-memory state but does NOT persist (so dry-run mirrors a real run's
	// in-process view without mutating disk).
	dryRun bool
}

// ReleaseTransition is a constrained, resumable Check→Apply→Verify node in the release DAG. Implementations are
// registered by a COMPILED handler ID (never a shell command); an unknown id or a transition introduced after this
// CLI's version fails the release closed rather than silently skipping a required convergence step.
type ReleaseTransition interface {
	// ID is the stable compiled handler identifier (e.g. "storage-descriptor-adoption").
	ID() string
	// Title is a short human label for plan output.
	Title() string
	// IntroducedIn is the platform version at which this transition became required.
	IntroducedIn() string
	// Irreversible reports whether Apply commits state that a later service-upgrade rollback does NOT undo. Storage
	// adoption is irreversible-but-safe: the descriptor stays valid even if the deployment rolls back.
	Irreversible() bool
	// AfterServices are deploy names that MUST be upgraded before this transition runs.
	AfterServices() []string
	// BeforeServices are deploy names that MUST NOT upgrade until this transition is Complete.
	BeforeServices() []string
	// Scopes enumerates the independent units to converge (e.g. one per media cluster).
	Scopes(env *reconcileEnv) ([]ReconcileScope, error)
	// Check classifies one scope from natural authoritative state. It never returns an error: an unreachable
	// authority or conflicting state is a Blocked verdict, so the release halts with a clear reason.
	Check(ctx context.Context, env *reconcileEnv, scope ReconcileScope) ReconcileCheck
	// Apply establishes the invariant for a Pending scope. Must be idempotent.
	Apply(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
	// Verify re-reads authoritative state and confirms the invariant now holds.
	Verify(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
	// ProvisionDisposition classifies how a CLEAN `cluster provision` relates to this transition (see
	// ProvisionDisposition). Naming the two cases lets provision hold a ProvisionEstablishedByBootstrap claim to its
	// stated mechanism — the establishing AfterServices must be SCHEDULED in this provision's plan — rather than
	// trusting it blindly.
	ProvisionDisposition() ProvisionDisposition
}

// ProvisionDisposition names how a CLEAN `cluster provision` (which does NOT run the Check→Apply→Verify DAG) relates to
// a reconciliation transition. It is a typed replacement for a self-attested boolean: provision does not merely trust
// the value, it holds a ProvisionEstablishedByBootstrap claim to its stated mechanism.
type ProvisionDisposition int

const (
	// ProvisionMustExecute: the transition converges EXISTING cluster state and can only be established by running the
	// reconciler. A clean provision cannot bootstrap it, so provision FAILS CLOSED and routes the operator to
	// `cluster release apply` rather than silently omitting a required convergence step. This is the SAFE DEFAULT — the
	// zero value — so a new transition that forgets to classify itself is refused on provision, never silently skipped.
	ProvisionMustExecute ProvisionDisposition = iota
	// ProvisionEstablishedByBootstrap: a clean provision's desired-state bootstrap establishes this transition's
	// invariant as part of normal install, via the services named in AfterServices (e.g. Quartermaster's desired-state
	// bootstrap writes the storage descriptor before the in-cell Foghorn/Chandler deploy). The invariant is therefore
	// already in place at install time with nothing to converge. Provision does not take this on faith: it verifies the
	// establishing AfterServices are actually enabled in the provision manifest, so a claim whose bootstrap mechanism
	// is not part of this provision fails closed.
	ProvisionEstablishedByBootstrap
)

// releaseSideStateConverger is an OPTIONAL transition capability: converge derived side-state that must stay
// consistent with the authority even when the invariant itself is already Complete (so a rerun repairs it). Storage
// adoption uses it to keep the manifest descriptor in step with the authoritative Quartermaster row — otherwise a
// resumed release, seeing Complete, would leave a stale manifest that the manifest-based Foghorn gate then blocks on.
// ConvergeSideState runs on Complete scopes (in dry-run too, but in-memory only via env.dryRun); PreviewSideState runs
// on Pending scopes in dry-run; both are implied by Apply on a real Pending run.
type releaseSideStateConverger interface {
	ConvergeSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
	// PreviewSideState overlays the state a Pending Apply WOULD produce, IN-MEMORY ONLY (never persisted). It lets a
	// --dry-run represent earlier planned transitions to downstream Checks/gates without mutating anything, so a
	// dry-run matches the release it validates instead of inspecting stale pre-transition state.
	PreviewSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
}

// releaseTransitionRegistry maps compiled handler id → transition. Populated by init() in each transition file.
var releaseTransitionRegistry = map[string]ReleaseTransition{}

func registerReleaseTransition(t ReleaseTransition) {
	if _, dup := releaseTransitionRegistry[t.ID()]; dup {
		panic("duplicate release transition id: " + t.ID())
	}
	releaseTransitionRegistry[t.ID()] = t
}

// buildReconcileQM constructs an authenticated Quartermaster client for the release executor and returns the operator
// JWT so transition Applies can authorize the operator-only adopt RPC.
func buildReconcileQM(ctx context.Context) (*qmclient.GRPCClient, string, func(), error) {
	return clusterStorageQMClient(ctx)
}
