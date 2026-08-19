package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/controlplane"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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

// reconcileQM is the narrow Quartermaster read surface available to release transitions.
// *qmclient.GRPCClient satisfies it; tests inject a fake.
type reconcileQM interface {
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
	// ID is the stable compiled handler identifier.
	ID() string
	// Title is a short human label for plan output.
	Title() string
	// IntroducedIn is the platform version at which this transition became required.
	IntroducedIn() string
	// Irreversible reports whether Apply commits state that a later service-upgrade rollback does NOT undo.
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
	// invariant as part of normal install, via the services named in AfterServices. The invariant is therefore
	// already in place at install time with nothing to converge. Provision does not take this on faith: it verifies the
	// establishing AfterServices are actually enabled in the provision manifest, so a claim whose bootstrap mechanism
	// is not part of this provision fails closed.
	ProvisionEstablishedByBootstrap
)

// releaseSideStateConverger is an OPTIONAL transition capability: converge derived side-state that must stay
// consistent with the authority even when the invariant itself is already Complete (so a rerun repairs it).
// ConvergeSideState runs on Complete scopes (in dry-run too, but in-memory only via env.dryRun); PreviewSideState runs
// on Pending scopes in dry-run; both are implied by Apply on a real Pending run.
type releaseSideStateConverger interface {
	ConvergeSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
	// PreviewSideState overlays the state a Pending Apply WOULD produce, IN-MEMORY ONLY (never persisted). It lets a
	// --dry-run represent earlier planned transitions to downstream Checks/gates without mutating anything, so a
	// dry-run matches the release it validates instead of inspecting stale pre-transition state.
	PreviewSideState(ctx context.Context, env *reconcileEnv, scope ReconcileScope) error
}

// releaseTransitionRegistry maps compiled handler id → transition. A release
// that introduces a transition adds its implementation to this map alongside
// the catalog entry; v0.3 starts with no retained one-off handlers.
var releaseTransitionRegistry = map[string]ReleaseTransition{}

// ResolveSystemTenantID returns the stable UUID Quartermaster owns for the
// canonical system-tenant alias. A context-sourced invocation may reuse the
// identity that a prior reconciliation persisted; an explicit manifest always
// resolves its own authority through Quartermaster so an unrelated active
// context or static env value cannot leak across clusters. The result is cached
// for this invocation so a multi-service release never re-dials Quartermaster
// per replica.
func (rc *resolvedCluster) ResolveSystemTenantID(ctx context.Context) (string, error) {
	rc.systemTenantOnce.Do(func() {
		// An explicit manifest source may target a different cluster than the
		// active context. Only trust the remembered identity when that context
		// also supplied the manifest; explicit inputs resolve their own authority.
		if rc.Source == inventory.SourceContext || rc.Source == inventory.SourceContextLastManifest {
			if id := strings.TrimSpace(rc.ContextSystemTenantID); id != "" {
				rc.systemTenantID = id
				return
			}
		}
		client, _, cleanup, err := buildReconcileQM(ctx, rc)
		if err != nil {
			rc.systemTenantErr = err
			return
		}
		defer cleanup()
		resp, err := client.ResolveTenantAliases(ctx, []string{bootstrap.SystemTenantAlias})
		if err != nil {
			rc.systemTenantErr = fmt.Errorf("ResolveTenantAliases: %w", err)
			return
		}
		if len(resp.GetUnknown()) > 0 {
			rc.systemTenantErr = fmt.Errorf("system tenant alias %q is not established in Quartermaster", bootstrap.SystemTenantAlias)
			return
		}
		id := strings.TrimSpace(resp.GetMapping()[bootstrap.SystemTenantAlias])
		if id == "" {
			rc.systemTenantErr = fmt.Errorf("system tenant alias %q resolved to an empty UUID", bootstrap.SystemTenantAlias)
			return
		}
		rc.systemTenantID = id
	})
	return rc.systemTenantID, rc.systemTenantErr
}

// buildReconcileQM constructs an authenticated Quartermaster client for the release executor and returns the operator
// JWT for privileged transition Applies.
func buildReconcileQM(ctx context.Context, rc *resolvedCluster) (*qmclient.GRPCClient, string, func(), error) {
	ctxCfg, err := reconcileContextForResolved(ctx, rc)
	if err != nil {
		return nil, "", nil, err
	}
	resolver := controlplane.NewResolverWithManifest(ctxCfg, rc.Manifest, rc.ManifestPath, rc.AgeKey)
	ep, err := resolver.ResolveGRPC(ctx, "quartermaster")
	if err != nil {
		resolver.Close()
		return nil, "", nil, err
	}
	qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      ep.Address,
		Timeout:       15 * time.Second,
		Logger:        logging.NewLogger(),
		ServiceToken:  ctxCfg.Auth.ServiceToken,
		AllowInsecure: ep.AllowInsecure,
		CACertFile:    ep.CACertFile,
		CACertPEM:     ep.CACertPEM,
		ServerName:    ep.ServerName,
	})
	if err != nil {
		resolver.Close()
		return nil, "", nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	return qc, ctxCfg.Auth.JWT, resolver.Close, nil
}

func reconcileContextForResolved(ctx context.Context, rc *resolvedCluster) (fwcfg.Context, error) {
	if rc == nil || rc.Manifest == nil {
		return fwcfg.Context{}, fmt.Errorf("release reconciliation requires a resolved cluster manifest")
	}
	cfg, err := fwcfg.Load()
	if err != nil {
		return fwcfg.Context{}, err
	}
	ctxCfg, err := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
	if err != nil {
		return fwcfg.Context{}, err
	}
	if ctxCfg.Persona != fwcfg.PersonaPlatform {
		ctxCfg = fwcfg.Context{
			Name:       "manifest-invocation",
			Persona:    fwcfg.PersonaPlatform,
			AccessMode: fwcfg.AccessModeSSH,
			Endpoints:  fwcfg.DefaultEndpoints(),
		}
		if isDevProfile(rc.Manifest) {
			ctxCfg.AccessMode = fwcfg.AccessModeLocal
		}
	}
	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return fwcfg.Context{}, fmt.Errorf("load manifest env_files for reconciliation: %w", err)
	}
	ctxCfg.Auth.ServiceToken = strings.TrimSpace(sharedEnv["SERVICE_TOKEN"])
	if ctxCfg.Auth.ServiceToken == "" {
		return fwcfg.Context{}, fmt.Errorf("SERVICE_TOKEN missing from manifest env_files (%s)", rc.ManifestPath)
	}
	jwt, err := fwcredentials.ResolveUserAuth(fwcfg.OSEnv{}, fwcredentials.DefaultStore())
	if err != nil {
		return fwcfg.Context{}, err
	}
	ctxCfg.Auth.JWT = jwt
	return ctxCfg, nil
}
