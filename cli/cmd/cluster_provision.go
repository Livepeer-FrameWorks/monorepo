package cmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	fwcfg "frameworks/cli/internal/config"
	meshutil "frameworks/cli/internal/mesh"
	"frameworks/cli/internal/readiness"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/credentials"
	"frameworks/cli/pkg/detect"
	fwsops "frameworks/cli/pkg/sops"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/remoteaccess"
	"frameworks/cli/pkg/ssh"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newClusterProvisionCmd() *cobra.Command {
	var only string
	var dryRun bool
	var force bool
	var ignoreValidation bool

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision cluster infrastructure and services",
		Args:  cobra.NoArgs,
		Long: `Provision cluster infrastructure and services from manifest:

Phase Options (--only):
  infrastructure  - Provision Postgres, Redis, Kafka, Zookeeper, ClickHouse
  applications    - Provision FrameWorks services
  interfaces      - Provision Nginx/Caddy, Chartroom, Foredeck, Logbook
  all             - Provision everything (default)

Provisioning is idempotent - safe to run multiple times.
Existing services will be detected and skipped unless --force is used.

For application/all phases, provisioning also initializes databases and
applies static SQL-owned reference seeds. Service-owned bootstrap state
such as tenants, clusters, billing tiers, and operator users is reconciled
during the service bootstrap step. Demo data is never loaded by provision;
use 'frameworks cluster seed --demo' explicitly for local development.

The manifest source (single file, local gitops repo, or GitHub repo) is
chosen by the persistent cluster-group flags. Run 'frameworks setup' to
save a default, or pass them explicitly.`,
		Example: `  # Provision and make the platform usable in one shot
  frameworks cluster provision --bootstrap-admin-email you@co --bootstrap-admin-password-env PW

  # Dry-run against a local manifest
  frameworks cluster provision --manifest ./cluster.yaml --dry-run

  # Provision from a GitHub repo (requires github-app-id/installation-id/private-key)
  frameworks cluster provision --github-repo org/infra-repo --cluster production

  # Force re-provision even if services exist
  frameworks cluster provision --force`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rc, err := resolveClusterManifest(cmd)
			if err != nil {
				return err
			}
			defer rc.Cleanup()
			if err := requirePlatformIfImplicitManifest(rc, cmd.OutOrStdout()); err != nil {
				return err
			}
			return runProvision(cmd, rc, only, dryRun, force, ignoreValidation)
		},
	}

	cmd.Flags().StringVar(&only, "only", "all", "Phase to provision (infrastructure|applications|interfaces|all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show plan without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force re-provision even if exists")
	cmd.Flags().BoolVar(&ignoreValidation, "ignore-validation", false, "Continue even if health validation fails (DANGEROUS)")

	cmd.Flags().String("bootstrap-admin-email", "", "Create an initial operator user with this email")
	cmd.Flags().String("bootstrap-admin-password", "", "Plaintext password for bootstrap admin (prefer --bootstrap-admin-password-env or --bootstrap-admin-password-file)")
	cmd.Flags().String("bootstrap-admin-password-env", "", "Read bootstrap admin password from this environment variable")
	cmd.Flags().String("bootstrap-admin-password-file", "", "Read bootstrap admin password from this file")
	cmd.Flags().String("bootstrap-admin-first-name", "FrameWorks", "First name for bootstrap admin")
	cmd.Flags().String("bootstrap-admin-last-name", "Operator", "Last name for bootstrap admin")
	cmd.Flags().Bool("bootstrap-reset-credentials", false, "Allow bootstrap account entries with reset_credentials=true to update existing password hashes")

	cmd.Flags().Bool("strict-control-plane", false, "Fail (exit 1) if post-provision control-plane validation has warnings")

	return cmd
}

func runProvision(cmd *cobra.Command, rc *resolvedCluster, only string, dryRun, force, ignoreValidation bool) error {
	manifest := rc.Manifest
	manifestPath := rc.ManifestPath
	out := cmd.OutOrStdout()

	ux.Heading(out, fmt.Sprintf("Provisioning cluster from manifest: %s", manifestPath))
	fmt.Fprintf(out, "Cluster type: %s, Profile: %s\n", manifest.Type, manifest.Profile)
	fmt.Fprintf(out, "Phase: %s\n\n", only)

	if dryRun {
		fmt.Fprintln(out, "[DRY-RUN MODE - No changes will be made]")
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	var phase orchestrator.Phase
	switch only {
	case "infrastructure":
		phase = orchestrator.PhaseInfrastructure
	case "applications":
		phase = orchestrator.PhaseApplications
	case "interfaces":
		phase = orchestrator.PhaseInterfaces
	case "all":
		phase = orchestrator.PhaseAll
	default:
		return fmt.Errorf("invalid phase: %s (must be infrastructure, applications, interfaces, or all)", only)
	}

	if err := validateProvisionMeshIdentity(manifest, meshIdentityRemediation(rc)); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}

	if err := validateIngressBundleIDs(manifest); err != nil {
		return fmt.Errorf("invalid manifest: %w", err)
	}

	if phaseRequiresGatewayMeshValidation(phase) {
		if err := validateGatewayMeshCoverage(manifest); err != nil {
			return fmt.Errorf("invalid manifest: %w", err)
		}
		if err := validateInternalGRPCTLSCoverage(manifest); err != nil {
			return fmt.Errorf("invalid manifest: %w", err)
		}
	}

	// Load and validate shared env_files up front. SERVICE_TOKEN and other
	// shared platform secrets live in gitops (SOPS-encrypted); this is the
	// single source of truth for the entire provision run — bootstrap auth,
	// infrastructure credentials, and per-service env merges all read from
	// here. Running before the dry-run exit also catches missing age keys
	// and missing secrets before the operator commits to a live run.
	manifestDir := filepath.Dir(manifestPath)
	sharedEnv, err := rc.SharedEnv()
	if err != nil {
		return fmt.Errorf("load manifest env_files: %w", err)
	}
	clusterEnvs, err := rc.ClusterEnvs()
	if err != nil {
		return fmt.Errorf("load cluster env_files: %w", err)
	}
	if isDevProfile(manifest) {
		if _, genErr := credentials.GenerateIfMissing(sharedEnv); genErr != nil {
			return fmt.Errorf("auto-generate dev secrets: %w", genErr)
		}
	} else if valErr := credentials.ValidateShared(sharedEnv); valErr != nil {
		return valErr
	}

	planner := orchestrator.NewPlanner(manifest)
	plan, err := planner.Plan(ctx, orchestrator.ProvisionOptions{
		Phase:    phase,
		DryRun:   dryRun,
		Force:    force,
		Parallel: true,
	})
	if err != nil {
		return fmt.Errorf("failed to create execution plan: %w", err)
	}

	// Show plan. In dry-run mode, annotate each task with a desired-vs-observed
	// config diff summary so operators see the real change surface before applying.
	annotateTask := func(task *orchestrator.Task) string { return "" }
	if dryRun {
		compareFn, cleanup := buildDryRunTaskCompare(ctx, cmd, rc, manifest, manifestDir, sharedEnv)
		if cleanup != nil {
			defer cleanup()
		}
		annotateTask = compareFn
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Execution Plan:")
	for i, batch := range plan.Batches {
		fmt.Fprintf(cmd.OutOrStdout(), "  Batch %d (parallel):\n", i+1)
		for _, task := range batch {
			suffix := annotateTask(task)
			if task.ClusterID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s) on %s [cluster: %s]%s\n", task.Name, task.Type, task.Host, task.ClusterID, suffix)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s) on %s%s\n", task.Name, task.Type, task.Host, suffix)
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal tasks: %d\n\n", len(plan.AllTasks))

	if dryRun {
		printDryRunRemovedServicePlacementPlan(ctx, cmd, manifest, phase, sharedEnv)
		fmt.Fprintln(cmd.OutOrStdout(), "Dry-run complete. Use without --dry-run to execute.")
		return nil
	}

	if err := executeProvision(ctx, cmd, manifest, plan, phase, force, ignoreValidation, manifestDir, sharedEnv, clusterEnvs, rc.ReleaseRepos); err != nil {
		return fmt.Errorf("provisioning failed: %w", err)
	}

	if rc.Source == inventory.SourceManifestFlag {
		rememberLastManifest(cmd, rc.ManifestPath)
	}

	initRan, seedsRan := false, false
	if phaseRunsPostProvisionInit(phase) {
		ux.Heading(out, "Reconciling platform data")
		if err := runInit(cmd, rc, "all"); err != nil {
			return fmt.Errorf("cluster init: %w", err)
		}
		initRan = true
		if err := runSeed(cmd, rc, false, true); err != nil {
			return fmt.Errorf("cluster seed: %w", err)
		}
		seedsRan = true
	}

	if phaseSyncsEdgeReleaseTarget(phase) {
		ux.Heading(out, "Syncing edge release target")
		if err := syncClusterEdgeReleaseTargetFromGitOps(cmd, rc, manifest.ResolvedChannel(), sharedEnv); err != nil {
			return fmt.Errorf("edge release target sync: %w", err)
		}
	}

	renderProvisionSummary(ctx, cmd, manifest, only, initRan, seedsRan)
	return nil
}

func phaseRunsPostProvisionInit(phase orchestrator.Phase) bool {
	switch phase {
	case orchestrator.PhaseApplications, orchestrator.PhaseAll:
		return true
	default:
		return false
	}
}

func phaseSyncsEdgeReleaseTarget(phase orchestrator.Phase) bool {
	switch phase {
	case orchestrator.PhaseApplications, orchestrator.PhaseAll:
		return true
	default:
		return false
	}
}

// validateIngressBundleIDs rejects manifests with unsafe TLS bundle ids.
// Must run before tasks: the post-task ingress registration hook downgrades
// errors to warnings, which would silently half-apply a poisoned id.
func validateIngressBundleIDs(manifest *inventory.Manifest) error {
	if manifest == nil {
		return nil
	}
	for bundleID := range manifest.TLSBundles {
		if !ingress.IsValidBundleID(bundleID) {
			return fmt.Errorf("tls_bundles[%q]: bundle id must match lowercase alphanumeric+hyphen (max 128, no leading hyphen)", bundleID)
		}
	}
	for siteID, cfg := range manifest.IngressSites {
		if cfg.TLSBundleID == "" {
			continue
		}
		if !ingress.IsValidBundleID(cfg.TLSBundleID) {
			return fmt.Errorf("ingress_sites[%q].tls_bundle_id %q is not a valid bundle id", siteID, cfg.TLSBundleID)
		}
	}
	return nil
}

func validateProvisionMeshIdentity(manifest *inventory.Manifest, remediation string) error {
	if manifest == nil {
		return fmt.Errorf("manifest is required")
	}
	svc, ok := manifest.Services["privateer"]
	if !ok || !svc.Enabled {
		return nil
	}
	hosts := orchestrator.EffectivePrivateerHosts(svc, manifest.Hosts)
	if err := meshutil.ValidateIdentity(manifest, hosts); err != nil {
		return fmt.Errorf("%w\n\n%s\nThen commit cluster.yaml and hosts inventory changes before provisioning", err, remediation)
	}
	return nil
}

func meshIdentityRemediation(rc *resolvedCluster) string {
	if rc == nil {
		return "Run: frameworks mesh wg generate --manifest <cluster.yaml>"
	}
	switch rc.Source {
	case inventory.SourceGithubRepoFlag, inventory.SourceGithubRepoEnv:
		cluster := rc.Cluster
		if cluster == "" {
			cluster = "<cluster>"
		}
		return fmt.Sprintf("Run against a local checkout: frameworks mesh wg generate --gitops-dir <checkout> --cluster %s", cluster)
	default:
		return fmt.Sprintf("Run: frameworks mesh wg generate --manifest %s", rc.ManifestPath)
	}
}

// renderProvisionSummary prints the multi-line Result block and the Next:
// block for a successful provision. Both degrade cleanly in CI / JSON modes
// via the ux helpers.
func renderProvisionSummary(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, only string, initRan, seedsRan bool) {
	out := cmd.OutOrStdout()

	// Build a fresh readiness report after post-provision init/seed work.
	// The earlier validateControlPlane call inside postProvisionFinalize may
	// have seen pre-init state.
	adminBootstrapped, _ := cmd.Flags().GetString("bootstrap-admin-email") //nolint:errcheck // flag always exists
	report := buildControlPlaneReport(ctx, manifest, collectRuntimeForReadinessOnly(cmd, manifest), nil)

	// If the readiness check couldn't run (no service token), we don't know
	// whether an admin account exists — report state honestly as "unknown"
	// rather than inferring from a no-warnings-means-healthy heuristic.
	adminField := ux.ResultField{
		Key: "operator account",
	}
	switch {
	case !report.Checked && adminBootstrapped == "":
		adminField.OK = false
		adminField.Detail = "unknown (no service token to re-check)"
	case adminBootstrapped != "":
		adminField.OK = true
		adminField.Detail = adminBootstrapped
	default:
		// Report was checked: derive existence from the operator-account warning.
		adminExists := true
		for _, w := range report.Warnings {
			if w.Subject == "control-plane.operator-account" {
				adminExists = false
			}
		}
		adminField.OK = adminExists
		adminField.Detail = adminDetail(adminExists, "")
	}

	fields := []ux.ResultField{
		{Key: "infrastructure", OK: true, Detail: "all batches succeeded"},
		{Key: "control-plane", OK: report.OK(), Detail: controlPlaneDetail(report)},
		adminField,
	}
	if initRan || seedsRan {
		fields = append(fields,
			ux.ResultField{Key: "init", OK: initRan, Detail: "postgres/kafka/clickhouse"},
			ux.ResultField{Key: "seeds", OK: seedsRan, Detail: "static SQL-owned data"},
		)
	}
	fields = append(fields, ux.ResultField{
		Key:    "phase",
		OK:     true,
		Detail: only,
	})
	ux.Result(out, fields)

	// Compose next-steps from readiness remediations + workflow defaults.
	var steps []ux.NextStep
	for _, w := range report.Warnings {
		if w.Remediation.Cmd == "" && w.Remediation.Why == "" {
			continue
		}
		steps = append(steps, ux.NextStep{Cmd: w.Remediation.Cmd, Why: w.Remediation.Why})
	}
	// Point at cluster doctor either way — after success to verify, or
	// after a no-check run so the operator can re-verify with SOPS access.
	switch {
	case report.OK():
		steps = append(steps, ux.NextStep{
			Cmd: "frameworks cluster doctor",
			Why: "Verify the control plane and run a final health check.",
		})
	case !report.Checked:
		steps = append(steps, ux.NextStep{
			Cmd: "frameworks cluster doctor",
			Why: "The post-run summary couldn't re-verify the control plane — doctor can, given SOPS access to the manifest env_files.",
		})
	}
	ux.PrintNextSteps(out, steps)
}

func collectRuntimeForReadinessOnly(_ *cobra.Command, manifest *inventory.Manifest) map[string]any {
	// Re-resolve what we need from manifest + shared env just for the final
	// readiness recheck. Keep this separate from the provision runtimeData
	// map so changes to one don't leak into the other.
	data := map[string]any{}
	if qmAddr, err := resolveServiceGRPCAddr(manifest, "quartermaster", defaultGRPCPort("quartermaster")); err == nil {
		data["quartermaster_grpc_addr"] = qmAddr
	}
	cfg, err := fwcfg.Load()
	if err == nil {
		active, mErr := fwcfg.MaybeActiveContext(fwcfg.GetRuntimeOverrides(), fwcfg.OSEnv{}, cfg)
		if mErr == nil && active.SystemTenantID != "" {
			data["system_tenant_id"] = active.SystemTenantID
		}
	}
	// service_token comes from manifest shared env; we can't read that here
	// without triggering SOPS decryption. If it's missing, readiness
	// downgrades to no-check, which is fine for the summary case.
	return data
}

func controlPlaneDetail(r readiness.Report) string {
	if !r.Checked {
		return "not re-verified (no service token available to post-run summary)"
	}
	if len(r.Warnings) == 0 {
		return "healthy"
	}
	if len(r.Warnings) == 1 {
		return "1 warning — see Next"
	}
	return fmt.Sprintf("%d warnings — see Next", len(r.Warnings))
}

func defaultPort(serviceID string) int {
	port, ok := servicedefs.DefaultPort(serviceID)
	if !ok {
		panic(fmt.Sprintf("missing default port for service %q", serviceID))
	}
	return port
}

func defaultGRPCPort(serviceID string) int {
	port, ok := servicedefs.DefaultGRPCPort(serviceID)
	if !ok {
		panic(fmt.Sprintf("missing default gRPC port for service %q", serviceID))
	}
	return port
}

func redisURLWithOptionalPassword(addr string, password string) string {
	if password == "" {
		return fmt.Sprintf("redis://%s", addr)
	}
	return fmt.Sprintf("redis://:%s@%s", url.QueryEscape(password), addr)
}

func adminDetail(exists bool, bootstrapEmail string) string {
	if exists {
		if bootstrapEmail != "" {
			return bootstrapEmail
		}
		return "present"
	}
	return "missing"
}

// provisionedTask tracks a successfully provisioned task for rollback
type provisionedTask struct {
	task   *orchestrator.Task
	host   inventory.Host
	config provisioner.ServiceConfig
}

type taskProvisionOutcome struct {
	config            provisioner.ServiceConfig
	previouslyRunning bool
	running           bool
	deferred          bool
}

// executeProvision runs the provisioning tasks
func executeProvision(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, phase orchestrator.Phase, force, ignoreValidation bool, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, releaseRepos []string) error {
	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Track successfully provisioned tasks for rollback
	var completed []provisionedTask

	// Execute each batch sequentially
	runtimeData := make(map[string]any)

	// Seed service_token from the preloaded shared env. All downstream callers
	// (bootstrap auth, service env builders, QM self-seed) read this key.
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	}

	if err := ensureNodeBaseline(ctx, cmd, manifest, plan, sshPool); err != nil {
		return err
	}

	if err := ensureNodeTuning(ctx, cmd, manifest, plan, sshPool); err != nil {
		return err
	}

	// Bootstrap and finalization helpers dial Quartermaster / Purser /
	// Commodore from the operator host. When the operator is off-mesh those
	// gRPC endpoints are unreachable directly, so route every operator-
	// originated call through SSH local-forwards. The session is closed when
	// executeProvision returns, releasing all tunnels. It is passed
	// explicitly to the helpers that need it — keeping it out of runtimeData
	// avoids leaking a live control object into provisioner metadata.
	raSession, raErr := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(manifest),
	})
	if raErr != nil {
		return fmt.Errorf("open remote-access session: %w", raErr)
	}
	defer raSession.Close()

	if err := ensureProvisionGeoIP(ctx, cmd.OutOrStdout(), manifest, manifestDir, sharedEnv, sshPool); err != nil {
		return err
	}

	// Pre-generate edge telemetry keypair so all foghorn/vmauth tasks in
	// parallel batches share the same key material.
	if err := ensureEdgeTelemetryJWTKeypair(runtimeData); err != nil {
		return fmt.Errorf("pre-generate edge telemetry keypair: %w", err)
	}
	if pkiRequired := internalPKIBootstrapRequired(manifest); pkiRequired {
		pki, err := loadInternalPKIBootstrap(sharedEnv, manifestDir)
		if err != nil {
			return fmt.Errorf("load internal PKI bootstrap material: %w", err)
		}
		runtimeData["internal_pki_bootstrap"] = pki
	}

	for batchNum, batch := range plan.Batches {
		ux.Subheading(cmd.OutOrStdout(), fmt.Sprintf("Executing Batch %d/%d (%d task(s))", batchNum+1, len(plan.Batches), len(batch)))

		type batchResult struct {
			task        *orchestrator.Task
			host        inventory.Host
			outcome     *taskProvisionOutcome
			runtimeData map[string]any // per-task copy; new keys merged back after batch
		}

		var (
			mu      sync.Mutex
			results []batchResult
		)

		g, gCtx := errgroup.WithContext(ctx)
		for _, task := range batch {
			task := task
			host, ok := manifest.GetHost(task.Host)
			if !ok {
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("host %s not found in manifest", task.Host)
			}

			// Snapshot runtimeData so each goroutine has its own copy.
			// New keys written by provisioning helpers (enrollment tokens,
			// telemetry keys) are collected per-task and merged back sequentially.
			taskRD := make(map[string]any, len(runtimeData))
			for k, v := range runtimeData {
				taskRD[k] = v
			}

			g.Go(func() error {
				fmt.Fprintf(cmd.OutOrStdout(), "  Provisioning %s on %s...\n", task.Name, task.Host)
				stopProgress := startTaskProgressLogger(cmd, task, 15*time.Second)
				outcome, err := provisionTask(gCtx, task, host, sshPool, manifest, force, ignoreValidation, taskRD, manifestDir, sharedEnv, clusterEnvs, releaseRepos)
				stopProgress()
				if err != nil {
					if task.Type == "privateer" {
						diagCtx, diagCancel := context.WithTimeout(ctx, 20*time.Second)
						capturePrivateerDiagnostics(diagCtx, cmd.OutOrStdout(), host, sshPool)
						diagCancel()
					}
					return fmt.Errorf("failed to provision %s: %w", task.Name, err)
				}

				mu.Lock()
				results = append(results, batchResult{task: task, host: host, outcome: outcome, runtimeData: taskRD})
				if !outcome.previouslyRunning {
					completed = append(completed, provisionedTask{task: task, host: host, config: outcome.config})
				}
				mu.Unlock()

				if task.Type != "quartermaster" {
					ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s provisioned", task.Name))
				}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Batch failed: %v", err))
			fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
			rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
			return err
		}

		// Merge per-task runtimeData back into the shared map.
		// Map-valued entries need deep merging so parallel tasks do not
		// clobber each other's discoveries.
		for _, r := range results {
			for k, v := range r.runtimeData {
				if newMap, ok := v.(map[string]string); ok {
					if existing, exists := runtimeData[k].(map[string]string); exists {
						for mk, mv := range newMap {
							existing[mk] = mv
						}
						continue
					}
				}
				runtimeData[k] = v
			}
		}

		// Post-batch side effects run sequentially after all tasks complete.
		// QM bootstrap runs once after the QM batch and reconciles
		// tenants/clusters/nodes/ingress/service_registry from the rendered
		// desired-state file. Per-task service-registry / ingress registration
		// no longer happens here — that work is in the rendered file too.
		for _, r := range results {
			if r.task.Type != "quartermaster" {
				continue
			}
			fmt.Fprintln(cmd.OutOrStdout(), "  Running Cluster Bootstrap (System Tenant)...")
			bootstrapCtx, bootstrapCancel := context.WithTimeout(ctx, provisionInitializeTimeout)

			bootstrapYAML, renderErr := renderBootstrapYAML(cmd, manifest, manifestDir, sharedEnv)
			if renderErr != nil {
				bootstrapCancel()
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Bootstrap render failed: %v", renderErr))
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("bootstrap render failed: %w", renderErr)
			}
			runtimeData["bootstrap_desired_state"] = bootstrapYAML

			if err := runServiceBootstrap(bootstrapCtx, cmd, manifest, sshPool, "quartermaster", bootstrapYAML, nil); err != nil {
				bootstrapCancel()
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Bootstrap failed: %v", err))
				diagCtx, diagCancel := context.WithTimeout(ctx, 12*time.Second)
				captureQuartermasterDiagnostics(diagCtx, cmd.OutOrStdout(), manifest, sshPool)
				diagCancel()
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("bootstrap failed: %w", err)
			}
			bootstrapCancel()

			// Pull system_tenant_id from QM via gRPC; the readiness report and
			// downstream bootstrap-admin user creation need it. Alias→UUID is
			// QM-owned data and never read directly from the CLI.
			resolveCtx, resolveCancel := context.WithTimeout(ctx, provisionInitializeTimeout)
			systemTenantID, idErr := resolveSystemTenantIDViaQM(resolveCtx, manifest, runtimeData, raSession)
			if idErr != nil {
				resolveCancel()
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Resolve system tenant: %v", idErr))
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("resolve system tenant: %w", idErr)
			}
			runtimeData["system_tenant_id"] = systemTenantID
			if qmAddr, addrErr := resolveServiceGRPCAddr(manifest, "quartermaster", defaultGRPCPort("quartermaster")); addrErr == nil {
				runtimeData["quartermaster_grpc_addr"] = qmAddr
			}
			if err := verifyQuartermasterMeshReachability(ctx, cmd, manifest, sshPool); err != nil {
				resolveCancel()
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Quartermaster mesh reachability failed: %v", err))
				fmt.Fprintln(cmd.OutOrStdout(), "  Services depend on quartermaster.internal for bootstrap and runtime discovery.")
				fmt.Fprintln(cmd.OutOrStdout(), "  Fix mesh reachability and re-run provisioning.")
				fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("quartermaster mesh reachability failed: %w", err)
			}
			// Pre-resolve every cluster's owner_tenant alias to its UUID so the
			// per-cluster gateway env injection can populate
			// FRAMEWORKS_CLUSTER_OWNER_TENANT_ID for non-platform clusters too
			// (private/customer/marketplace cluster gateways need correct
			// tenant attribution for telemetry, not just the system tenant).
			//
			// Hard-fail when any cluster runs livepeer-gateway: an empty
			// FRAMEWORKS_CLUSTER_OWNER_TENANT_ID disables gateway telemetry
			// entirely (Decklog rejects events with no tenant), so a silent
			// warning would turn a configuration error into invisible data
			// loss. Clusters without livepeer-gateway can degrade gracefully.
			ownerMap, ownerErr := resolveClusterOwnerTenantIDs(resolveCtx, manifest, runtimeData, raSession)
			resolveCancel()
			if ownerErr != nil {
				if anyClusterRunsLivepeerGateway(manifest) {
					ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Resolve cluster owner tenants: %v", ownerErr))
					fmt.Fprintln(cmd.OutOrStdout(), "\n  Cluster runs livepeer-gateway — owner tenant must resolve to UUID for telemetry attribution.")
					fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("resolve cluster owner tenants for livepeer-gateway clusters: %w", ownerErr)
				}
				ux.Warn(cmd.OutOrStdout(), fmt.Sprintf("Resolve cluster owner tenants: %v (no livepeer-gateway services — continuing)", ownerErr))
			} else {
				runtimeData["owner_tenant_ids_by_alias"] = ownerMap
			}

			ux.Success(cmd.OutOrStdout(), "System Tenant bootstrapped")
			ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s provisioned", r.task.Name))
		}

		if err := maybeReconcileBatchServiceClusterAssignments(ctx, cmd, batch, manifest, runtimeData, raSession); err != nil {
			ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Service-cluster reconciliation failed: %v", err))
			fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
			rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
			return fmt.Errorf("service-cluster reconciliation failed: %w", err)
		}

		if batchContainsService(batch, "yugabyte") && !remainingBatchesContainService(plan.Batches[batchNum+1:], "yugabyte") {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			if err := verifyYugabyteCluster(ctx, cmd, manifest, sshPool); err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Yugabyte cluster verification failed: %v", err))
				if !ignoreValidation {
					fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("yugabyte cluster verification failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  Warning: continuing despite Yugabyte verification issues (--ignore-validation)")
			}
			ybTarget, ybErr := resolveMigrationTargetFromParts(manifest, releaseRepos, "")
			if ybErr != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("resolve migration target: %v", ybErr))
				fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("resolve migration target: %w", ybErr)
			}
			if err := initializeDeferredYugabyte(ctx, cmd, manifest, sshPool, sharedEnv, clusterEnvs, ybTarget); err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Yugabyte initialization failed: %v", err))
				fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("yugabyte initialization failed: %w", err)
			}
		}

		if batchContainsService(batch, "kafka-controller") && !remainingBatchesContainService(plan.Batches[batchNum+1:], "kafka-controller") {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			if err := verifyKafkaControllerMesh(ctx, cmd, manifest, sshPool); err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Kafka controller mesh verification failed: %v", err))
				if !ignoreValidation {
					fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("kafka controller mesh verification failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  Warning: continuing despite Kafka controller mesh issues (--ignore-validation)")
			}
		}

		if batchContainsService(batch, "kafka") && !remainingBatchesContainService(plan.Batches[batchNum+1:], "kafka") {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			if err := initializeDeferredKafka(ctx, cmd, manifest, sshPool, releaseRepos); err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Kafka topic initialization failed: %v", err))
				fmt.Fprintln(cmd.OutOrStdout(), "  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("kafka topic initialization failed: %w", err)
			}
		}

		// Mesh preflight gate: after a batch containing Privateer tasks,
		// verify mesh health before proceeding to application services.
		if batchContainsPrivateer(batch) && batchNum+1 < len(plan.Batches) {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			privateerSvc := manifest.Services["privateer"]
			meshHosts := orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts)
			if err := verifyMeshHealth(ctx, cmd, manifest, sshPool, meshHosts); err != nil {
				ux.Fail(cmd.OutOrStdout(), fmt.Sprintf("Mesh verification failed: %v", err))
				fmt.Fprintln(cmd.OutOrStdout(), "  Services depend on mesh DNS for discovery.")
				fmt.Fprintln(cmd.OutOrStdout(), "  Fix mesh issues and re-run provisioning, or use --ignore-validation to skip.")
				if !ignoreValidation {
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("mesh health verification failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "  Warning: continuing despite mesh issues (--ignore-validation)")
			}
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	if err := reconcileRemovedServicePlacements(ctx, cmd, manifest, phase, runtimeData, raSession, sshPool); err != nil {
		return fmt.Errorf("removed service placement reconciliation failed: %w", err)
	}

	// Post-provision: bootstrap Purser cluster pricing, admin user, control-plane validation
	if err := postProvisionFinalize(ctx, cmd, manifest, runtimeData, raSession); err != nil {
		return err
	}

	return nil
}

// postProvisionFinalize handles Purser pricing bootstrap, optional admin user creation,
// and control-plane validation after all service batches are complete.
func postProvisionFinalize(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) error {
	systemTenantID, ok := runtimeData["system_tenant_id"].(string)
	serviceToken, stOK := runtimeData["service_token"].(string)

	if !ok || !stOK || systemTenantID == "" || serviceToken == "" {
		// Bootstrap didn't run (e.g. --only=interfaces), skip finalization
		return nil
	}

	rememberSystemTenantID(cmd, systemTenantID)

	bootstrapYAML, ok := runtimeData["bootstrap_desired_state"].([]byte)
	if !ok || len(bootstrapYAML) == 0 {
		// Should not happen — QM bootstrap stashes it. Defensive guard.
		return validateControlPlane(ctx, cmd, manifest, runtimeData, sess)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "  Control-plane finalization...")

	sshKey := stringFlag(cmd, "ssh-key").Value
	sshPool := ssh.NewPool(30*time.Second, sshKey)
	defer sshPool.Close()

	// Purser bootstrap reconciles the embedded tier catalog, cluster pricing,
	// and customer billing. Idempotent — always safe to run; the no-pricing
	// case is just an empty cluster_pricing reconcile. Failure is fatal: a
	// successful provision must include a complete billing/entitlement state.
	if err := runServiceBootstrap(ctx, cmd, manifest, sshPool, "purser", bootstrapYAML, nil); err != nil {
		return fmt.Errorf("purser bootstrap: %w", err)
	}

	// Purser cross-service invariant check: every QM platform-official cluster
	// has a matching purser.cluster_pricing row. Skipped clusters here mean the
	// deposit monitor / tenant entitlement code goes blind, so this also fails
	// the provision rather than warns.
	if err := runServiceBootstrapValidate(ctx, cmd, manifest, sshPool, "purser"); err != nil {
		return fmt.Errorf("purser bootstrap validate: %w", err)
	}

	// Commodore bootstrap creates user(s) under tenants in the rendered
	// accounts: section. With no --bootstrap-admin-email the section is
	// empty and the subcommand is a parse-and-exit no-op. Failure is fatal.
	if err := runServiceBootstrap(ctx, cmd, manifest, sshPool, "commodore", bootstrapYAML, commodoreBootstrapExtraArgs(cmd)); err != nil {
		return fmt.Errorf("commodore bootstrap: %w", err)
	}

	return validateControlPlane(ctx, cmd, manifest, runtimeData, sess)
}

func validateControlPlane(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) error {
	report := buildControlPlaneReport(ctx, manifest, runtimeData, sess)
	runtimeData["control_plane_report"] = report

	if len(report.Warnings) == 0 {
		ux.Success(cmd.OutOrStdout(), "Control-plane validation passed")
		return nil
	}

	ux.Subheading(cmd.OutOrStdout(), "Control-plane validation warnings:")
	for _, w := range report.Warnings {
		ux.Warn(cmd.OutOrStdout(), w.Detail)
	}
	strict, _ := cmd.Flags().GetBool("strict-control-plane") //nolint:errcheck // flag always exists
	switch {
	case strict:
		return fmt.Errorf("control-plane validation failed with %d warning(s) (--strict-control-plane is set)", len(report.Warnings))
	case !isDevProfile(manifest):
		return fmt.Errorf("control-plane validation failed with %d warning(s); non-dev profiles fail on warnings by default — pass --ignore-validation to override if you know what you're doing", len(report.Warnings))
	}
	return nil
}

// buildControlPlaneReport assembles the ControlPlaneInputs from manifest +
// runtime data and delegates to readiness.ControlPlaneReadiness. Callers
// that only need the report (without printing / policy) can use this
// directly — cluster doctor and status do.
func buildControlPlaneReport(ctx context.Context, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) readiness.Report {
	systemTenantID, _ := runtimeData["system_tenant_id"].(string) //nolint:errcheck // zero value on missing key is the intent
	serviceToken, _ := runtimeData["service_token"].(string)      //nolint:errcheck // zero value on missing key is the intent

	var pricings []readiness.ClusterPricing
	for clusterID, cc := range manifest.Clusters {
		if cc.Pricing != nil {
			pricings = append(pricings, readiness.ClusterPricing{ClusterID: clusterID})
		}
	}

	caPEM := internalCAFromRuntime(runtimeData)

	// Resolve every endpoint up front so a tunnel/dial-resolution failure
	// surfaces as a warning rather than degrading silently to Checked=false.
	// validateControlPlane treats len(Warnings)==0 as success, so silent
	// resolution failures must not be possible during provisioning.
	qm, qmErr := serviceEndpointFor(ctx, manifest, sess, "quartermaster", defaultGRPCPort("quartermaster"), caPEM)
	commodore, commodoreErr := serviceEndpointFor(ctx, manifest, sess, "commodore", defaultGRPCPort("commodore"), caPEM)
	purser, purserErr := serviceEndpointFor(ctx, manifest, sess, "purser", defaultGRPCPort("purser"), caPEM)

	report := readiness.ControlPlaneReadiness(ctx, readiness.ControlPlaneInputs{
		SystemTenantID:   systemTenantID,
		ServiceToken:     serviceToken,
		Quartermaster:    qm,
		Commodore:        commodore,
		Purser:           purser,
		DeclaredPricings: pricings,
	})

	resolutionWarnings := endpointResolutionWarnings(sess, qmErr, commodoreErr, purserErr)
	if len(resolutionWarnings) > 0 {
		// Endpoint resolution failed but we attempted; surface as warnings
		// and force Checked=true so the policy gate cannot read this as
		// "everything is fine, no warnings".
		report.Warnings = append(resolutionWarnings, report.Warnings...)
		report.Checked = true
	}
	return report
}

// endpointResolutionWarnings turns endpoint resolution errors into readiness
// warnings. When sess is non-nil (provisioning), every error is a warning —
// it almost certainly indicates an SSH tunnel failure or a manifest gap that
// will block real calls. When sess is nil (doctor / status), Quartermaster
// is the only required endpoint; missing Commodore/Purser entries in the
// manifest are normal and silenced.
func endpointResolutionWarnings(sess *remoteaccess.Session, qmErr, commodoreErr, purserErr error) []readiness.Warning {
	var ws []readiness.Warning
	add := func(subject, name string, err error) {
		if err == nil {
			return
		}
		ws = append(ws, readiness.Warning{
			Subject: subject,
			Detail:  fmt.Sprintf("Could not resolve %s endpoint: %v", name, err),
		})
	}
	add("control-plane.quartermaster", "Quartermaster", qmErr)
	if sess != nil {
		add("control-plane.commodore", "Commodore", commodoreErr)
		add("control-plane.purser", "Purser", purserErr)
	}
	return ws
}

// serviceEndpointFor builds a readiness.ServiceEndpoint by routing through
// resolveServiceDial and returns the underlying error so callers can decide
// how to surface it. An empty endpoint accompanies the error so a caller
// that chooses to ignore it (read-only commands skipping optional services)
// gets a value the readiness check treats as "skip".
func serviceEndpointFor(ctx context.Context, manifest *inventory.Manifest, sess *remoteaccess.Session, name string, defaultPort int, caPEM string) (readiness.ServiceEndpoint, error) {
	addr, serverName, insecure, err := resolveServiceDial(ctx, manifest, sess, name, defaultPort)
	if err != nil {
		return readiness.ServiceEndpoint{}, err
	}
	return readiness.ServiceEndpoint{
		GRPCAddr:      addr,
		ServerName:    serverName,
		AllowInsecure: insecure,
		CACertPEM:     caPEM,
	}, nil
}

// resolveServiceGRPCAddr resolves a service's gRPC address from the manifest.
// Prefer the WireGuard address when present because internal gRPC traffic is
// mesh-scoped during provisioning; fall back to the public address for hosts
// that are not on the mesh.
func resolveServiceGRPCAddr(manifest *inventory.Manifest, serviceName string, defaultGRPCPort int) (string, error) {
	svc, ok := manifest.Services[serviceName]
	if !ok {
		return "", fmt.Errorf("%s service not found in manifest", serviceName)
	}

	hostKey := svc.Host
	if hostKey == "" && len(svc.Hosts) > 0 {
		hostKey = svc.Hosts[0]
	}

	host, ok := manifest.GetHost(hostKey)
	if !ok {
		return "", fmt.Errorf("%s host %q not found in manifest", serviceName, hostKey)
	}

	grpcPort := defaultGRPCPort
	if svc.GRPCPort != 0 {
		grpcPort = svc.GRPCPort
	}

	addr := manifest.MeshAddress(hostKey)
	if addr == "" || addr == hostKey {
		addr = host.ExternalIP
	}
	return fmt.Sprintf("%s:%d", addr, grpcPort), nil
}

func maybeReconcileBatchServiceClusterAssignments(ctx context.Context, cmd *cobra.Command, batch []*orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) error {
	// Run after any pool-assigned service has been deployed in this batch so
	// the service_instances rows exist before we wire assignment FKs.
	any := false
	for _, name := range pkgdns.PoolAssignedServiceTypes() {
		if batchContainsService(batch, name) {
			any = true
			break
		}
	}
	if !any {
		return nil
	}

	return reconcileServiceClusterAssignments(ctx, cmd, manifest, runtimeData, sess)
}

// batchContainsPrivateer returns true if any task in the batch is a Privateer deployment.
func batchContainsPrivateer(batch []*orchestrator.Task) bool {
	return batchContainsService(batch, "privateer")
}

func batchContainsService(batch []*orchestrator.Task, serviceName string) bool {
	for _, task := range batch {
		if task.ServiceID == serviceName || task.Type == serviceName {
			return true
		}
	}
	return false
}

func remainingBatchesContainService(batches [][]*orchestrator.Task, serviceName string) bool {
	for _, batch := range batches {
		if batchContainsService(batch, serviceName) {
			return true
		}
	}
	return false
}

func verifyYugabyteCluster(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() || len(pg.Nodes) == 0 {
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Verifying Yugabyte cluster on %d node(s)...\n", len(pg.Nodes))
	base := provisioner.NewBaseProvisioner("yugabyte-verify", pool)
	var failures []string

	for _, node := range pg.Nodes {
		hostInfo, ok := manifest.Hosts[node.Host]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: host not found in manifest", node.Host))
			continue
		}

		fmt.Fprintf(cmd.OutOrStdout(), "    Checking %s (%s)...\n", node.Host, hostInfo.ExternalIP)

		result, err := base.RunCommand(ctx, hostInfo, "systemctl is-active yb-master yb-tserver 2>/dev/null")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: systemd check failed: %v", node.Host, err))
			continue
		}
		activeLines := strings.Fields(strings.TrimSpace(result.Stdout))
		if len(activeLines) < 2 || activeLines[0] != "active" || activeLines[1] != "active" {
			failures = append(failures, fmt.Sprintf("%s: services not active (output=%q)", node.Host, strings.TrimSpace(result.Stdout)))
			continue
		}

		result, err = base.RunCommand(ctx, hostInfo, `
for i in $(seq 1 30); do
  if ss -ltn '( sport = :5433 )' 2>/dev/null | grep -q LISTEN; then
    echo READY
    exit 0
  fi
  sleep 2
done
echo NOT_READY
exit 0`)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: YSQL wait failed: %v", node.Host, err))
			continue
		}
		if !strings.Contains(result.Stdout, "READY") {
			statusResult, statusErr := base.RunCommand(ctx, hostInfo, "systemctl status yb-master yb-tserver --no-pager --full 2>/dev/null || true")
			if statusErr != nil {
				statusResult.Stdout = fmt.Sprintf("(status probe failed: %v)", statusErr)
			}
			logResult, logErr := base.RunCommand(ctx, hostInfo, `
set -e
for file in \
  /var/lib/yugabyte/data/yb-data/tserver/logs/yb-tserver.INFO \
  "$(ls -1t /var/lib/yugabyte/data/yb-data/tserver/logs/yb-tserver*.INFO* 2>/dev/null | head -n 1)" \
  "$(ls -1t /var/lib/yugabyte/data/yb-data/tserver/logs/postgresql* 2>/dev/null | head -n 1)"; do
  if [ -n "$file" ] && [ -f "$file" ]; then
    echo "===== $file ====="
    tail -n 80 "$file"
  fi
done
`)
			if logErr != nil {
				logResult.Stdout = fmt.Sprintf("(log probe failed: %v)", logErr)
			}
			failures = append(failures, fmt.Sprintf("%s: YSQL not listening on 5433 after cluster assembly\nsystemctl:\n%s\nlogs:\n%s", node.Host, strings.TrimSpace(statusResult.Stdout), strings.TrimSpace(logResult.Stdout)))
			continue
		}

		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("%s: yb-master/yb-tserver active, YSQL listening on :5433", node.Host))
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d node(s) failed Yugabyte verification:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}

	ux.Success(cmd.OutOrStdout(), "Yugabyte cluster verified")
	return nil
}

func initializeDeferredYugabyte(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, targetVersion string) error {
	pg := manifest.Infrastructure.Postgres
	if pg == nil || !pg.Enabled || !pg.IsYugabyte() || len(pg.Nodes) == 0 {
		return nil
	}

	host, ok := manifest.GetHost(pg.Nodes[0].Host)
	if !ok {
		return fmt.Errorf("yugabyte node host %s not found", pg.Nodes[0].Host)
	}

	password, err := resolveYugabytePassword(pg, sharedEnv)
	if err != nil {
		return err
	}
	yugabyteDatabases := expandedYugabyteDatabaseConfigs(pg.Databases, manifest)
	databases := yugabyteDatabaseConfigsToMetadata(yugabyteDatabases, manifest, sharedEnv, clusterEnvs, password)
	schemaDatabases := yugabyteSchemaDatabases(pg.Databases, manifest)

	config := provisioner.ServiceConfig{
		Version: pg.Version,
		Port:    pg.EffectivePort(),
		Metadata: map[string]any{
			"platform_channel":  manifest.ResolvedChannel(),
			"databases":         databases,
			"postgres_password": password,
		},
	}

	prov, err := provisioner.GetProvisioner("yugabyte", pool)
	if err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout(), "  Initializing YugabyteDB databases...")
	if err := prov.Initialize(ctx, host, config); err != nil {
		return err
	}
	if err := applyPostgresSchemasAndMigrations(ctx, cmd.OutOrStdout(), "yugabyte", host, config, prov, schemaDatabases, targetVersion); err != nil {
		return err
	}
	ux.Success(cmd.OutOrStdout(), "YugabyteDB initialized")
	return nil
}

func initializeDeferredKafka(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool, releaseRepos []string) error {
	clusters := allKafkaClusters(manifest)
	if len(clusters) == 0 {
		return nil
	}

	prov, err := provisioner.GetProvisioner("kafka", pool)
	if err != nil {
		return err
	}

	for i := range clusters {
		cluster := clusters[i]
		if len(cluster.Topics) == 0 {
			continue
		}
		if len(cluster.Brokers) == 0 {
			return fmt.Errorf("kafka cluster %s has topics but no brokers", kafkaClusterAlias(manifest, &cluster))
		}

		broker := cluster.Brokers[0]
		host, ok := manifest.GetHost(broker.Host)
		if !ok {
			return fmt.Errorf("kafka broker host %s not found", broker.Host)
		}

		task := &orchestrator.Task{
			Name:       fmt.Sprintf("kafka-topic-init-%s", kafkaClusterAlias(manifest, &cluster)),
			Type:       "kafka",
			ServiceID:  "kafka",
			InstanceID: strconv.Itoa(broker.ID),
			Host:       broker.Host,
			Phase:      orchestrator.PhaseInfrastructure,
			ClusterID:  cluster.RegionID,
		}
		config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, releaseRepos)
		if err != nil {
			return err
		}

		fmt.Fprintf(cmd.OutOrStdout(), "  Initializing Kafka topics for %s on %s...\n", kafkaClusterAlias(manifest, &cluster), broker.Host)
		if err := runProvisionPhase(ctx, provisionInitializeTimeout, "initialize", func(phaseCtx context.Context) error {
			return initializeKafkaTopicsWithRetry(phaseCtx, cmd, prov, host, config)
		}); err != nil {
			return err
		}
		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Kafka topics initialized for %s", kafkaClusterAlias(manifest, &cluster)))
	}

	return nil
}

func initializeKafkaTopicsWithRetry(ctx context.Context, cmd *cobra.Command, prov provisioner.Provisioner, host inventory.Host, config provisioner.ServiceConfig) error {
	var lastErr error
	for attempt := 1; ; attempt++ {
		err := prov.Initialize(ctx, host, config)
		if err == nil {
			return nil
		}
		if !isKafkaBrokerRegistrationLag(err) {
			return err
		}
		lastErr = err
		fmt.Fprintf(cmd.OutOrStdout(), "    Kafka brokers still registering; retrying topic init (attempt %d)...\n", attempt)

		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("broker registration did not reach topic replication factor before timeout: %w", lastErr)
		case <-timer.C:
		}
	}
}

func isKafkaBrokerRegistrationLag(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "InvalidReplicationFactorException") &&
		strings.Contains(msg, "broker(s) are registered")
}

func meshHostname(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return ""
	}
	return fmt.Sprintf("%s.internal", trimmed)
}

func manifestMeshHostname(manifest *inventory.Manifest, hostName string) string {
	if manifest == nil {
		return ""
	}
	hostName = strings.TrimSpace(hostName)
	if hostName == "" {
		return ""
	}
	if _, ok := manifest.GetHost(hostName); !ok {
		return ""
	}
	return meshHostname(hostName)
}

func usesInternalGRPCLeaf(serviceName string) bool {
	switch serviceName {
	case "commodore", "quartermaster", "purser", "periscope-query", "decklog", "foghorn", "signalman", "deckhand", "skipper", "navigator":
		return true
	default:
		return false
	}
}

func internalGRPCLeafServiceName(task *orchestrator.Task) string {
	if task == nil {
		return ""
	}
	if usesInternalGRPCLeaf(task.Type) {
		return task.Type
	}
	if usesInternalGRPCLeaf(task.ServiceID) {
		return task.ServiceID
	}
	return ""
}

func phaseRequiresGatewayMeshValidation(phase orchestrator.Phase) bool {
	return phase == orchestrator.PhaseApplications || phase == orchestrator.PhaseAll
}

func serviceRunning(state *detect.ServiceState) bool {
	return state != nil && state.Exists && state.Running
}

type serviceClusterAssignmentClient interface {
	AssignServiceToCluster(ctx context.Context, req *pb.AssignServiceToClusterRequest) error
	DrainServiceInstance(ctx context.Context, req *pb.DrainServiceInstanceRequest) (*pb.DrainServiceInstanceResponse, error)
	ListServices(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListServicesResponse, error)
	ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error)
}

func resolveQuartermasterRuntimeData(manifest *inventory.Manifest, runtimeData map[string]any) (string, string, error) {
	var serviceToken string
	if v, ok := runtimeData["service_token"].(string); ok {
		serviceToken = strings.TrimSpace(v)
	}
	if serviceToken == "" {
		return "", "", fmt.Errorf("SERVICE_TOKEN missing from manifest env_files — add it to your gitops secrets")
	}

	var qmHost string
	var qmSvc inventory.ServiceConfig
	for name, svc := range manifest.Services {
		if name != "quartermaster" {
			continue
		}
		qmHost = svc.Host
		qmSvc = svc
		if qmHost == "" && len(svc.Hosts) > 0 {
			qmHost = svc.Hosts[0]
		}
		break
	}
	host, ok := manifest.GetHost(qmHost)
	if !ok {
		return "", "", fmt.Errorf("quartermaster host not found in manifest")
	}

	grpcPort := defaultGRPCPort("quartermaster")
	if qmSvc.GRPCPort != 0 {
		grpcPort = qmSvc.GRPCPort
	}

	return serviceToken, fmt.Sprintf("%s:%d", host.ExternalIP, grpcPort), nil
}

func ensureEdgeTelemetryJWTKeypair(runtimeData map[string]any) error {
	if runtimeData == nil {
		return fmt.Errorf("runtime data is nil")
	}
	priv, privOK := runtimeData["edge_telemetry_jwt_private_key_pem_b64"].(string)
	pub, pubOK := runtimeData["edge_telemetry_jwt_public_key_pem_b64"].(string)
	if privOK && pubOK && strings.TrimSpace(priv) != "" && strings.TrimSpace(pub) != "" {
		return nil
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return fmt.Errorf("generate edge telemetry signing key: %w", err)
	}
	privateDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("marshal edge telemetry private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal edge telemetry public key: %w", err)
	}

	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	runtimeData["edge_telemetry_jwt_private_key_pem_b64"] = base64.StdEncoding.EncodeToString(privatePEM)
	runtimeData["edge_telemetry_jwt_public_key_pem_b64"] = base64.StdEncoding.EncodeToString(publicPEM)
	return nil
}

type internalPKIBootstrap struct {
	CABundlePEM      string
	intermediateCert *x509.Certificate
	intermediateKey  *ecdsa.PrivateKey
}

func internalPKIBootstrapRequired(manifest *inventory.Manifest) bool {
	if manifest == nil || isDevProfile(manifest) {
		return false
	}
	if svc, ok := manifest.Services["navigator"]; ok && svc.Enabled {
		return true
	}
	for name, svc := range manifest.Services {
		if svc.Enabled && usesInternalGRPCLeaf(name) {
			return true
		}
	}
	return false
}

func loadInternalPKIBootstrap(sharedEnv map[string]string, manifestDir string) (*internalPKIBootstrap, error) {
	rootPEM, intermediatePEM, intermediateKeyPEM, err := loadInternalCAMaterial(sharedEnv, manifestDir)
	if err != nil {
		return nil, err
	}
	rootCert, err := parseCertificatePEM(rootPEM)
	if err != nil {
		return nil, fmt.Errorf("parse root ca cert: %w", err)
	}
	intermediateCert, err := parseCertificatePEM(intermediatePEM)
	if err != nil {
		return nil, fmt.Errorf("parse intermediate ca cert: %w", err)
	}
	intermediateKey, err := parseECPrivateKeyPEM(intermediateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse intermediate ca key: %w", err)
	}
	if err := validateInternalCA(rootCert, intermediateCert, intermediateKey); err != nil {
		return nil, err
	}
	return &internalPKIBootstrap{
		CABundlePEM:      strings.TrimSpace(rootPEM) + "\n" + strings.TrimSpace(intermediatePEM) + "\n",
		intermediateCert: intermediateCert,
		intermediateKey:  intermediateKey,
	}, nil
}

func loadInternalCAMaterial(env map[string]string, manifestDir string) (string, string, string, error) {
	rootB64 := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64"])
	intermediateB64 := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64"])
	keyB64 := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64"])
	if rootB64 != "" || intermediateB64 != "" || keyB64 != "" {
		if rootB64 == "" || intermediateB64 == "" || keyB64 == "" {
			return "", "", "", fmt.Errorf("internal CA base64 env requires root cert, intermediate cert, and intermediate key")
		}
		rootPEM, err := decodeB64PEM(rootB64, "root ca cert")
		if err != nil {
			return "", "", "", err
		}
		intermediatePEM, err := decodeB64PEM(intermediateB64, "intermediate ca cert")
		if err != nil {
			return "", "", "", err
		}
		keyPEM, err := decodeB64PEM(keyB64, "intermediate ca key")
		if err != nil {
			return "", "", "", err
		}
		return rootPEM, intermediatePEM, keyPEM, nil
	}

	rootFile := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE"])
	intermediateFile := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE"])
	keyFile := strings.TrimSpace(env["NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE"])
	if rootFile == "" && intermediateFile == "" && keyFile == "" {
		return "", "", "", fmt.Errorf("internal CA material is required for non-dev internal gRPC TLS")
	}
	if rootFile == "" || intermediateFile == "" || keyFile == "" {
		return "", "", "", fmt.Errorf("internal CA file env requires root cert, intermediate cert, and intermediate key")
	}
	rootPEM, err := readPEMFile(resolveEnvFilePath(rootFile, manifestDir), "root ca cert")
	if err != nil {
		return "", "", "", err
	}
	intermediatePEM, err := readPEMFile(resolveEnvFilePath(intermediateFile, manifestDir), "intermediate ca cert")
	if err != nil {
		return "", "", "", err
	}
	keyPEM, err := readPEMFile(resolveEnvFilePath(keyFile, manifestDir), "intermediate ca key")
	if err != nil {
		return "", "", "", err
	}
	return rootPEM, intermediatePEM, keyPEM, nil
}

func decodeB64PEM(value, label string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("decode %s base64 env: %w", label, err)
	}
	return string(decoded), nil
}

func resolveEnvFilePath(path, manifestDir string) string {
	if filepath.IsAbs(path) || manifestDir == "" {
		return path
	}
	return filepath.Join(manifestDir, path)
}

func readPEMFile(path, label string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s %q: %w", label, path, err)
	}
	return string(data), nil
}

func parseCertificatePEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("missing pem block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECPrivateKeyPEM(keyPEM string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("missing pem block")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err == nil {
		return key, nil
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecKey, ok := raw.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not ECDSA")
	}
	return ecKey, nil
}

func validateInternalCA(rootCert, intermediateCert *x509.Certificate, intermediateKey *ecdsa.PrivateKey) error {
	now := time.Now()
	if !rootCert.IsCA {
		return fmt.Errorf("root ca cert must be a CA certificate")
	}
	if !intermediateCert.IsCA {
		return fmt.Errorf("intermediate ca cert must be a CA certificate")
	}
	if now.Before(rootCert.NotBefore) || now.After(rootCert.NotAfter) {
		return fmt.Errorf("root ca cert is not currently valid")
	}
	if now.Before(intermediateCert.NotBefore) || now.After(intermediateCert.NotAfter) {
		return fmt.Errorf("intermediate ca cert is not currently valid")
	}
	if intermediateCert.PublicKeyAlgorithm != x509.ECDSA {
		return fmt.Errorf("intermediate ca cert public key is not ECDSA")
	}
	certPubDER, err := x509.MarshalPKIXPublicKey(intermediateCert.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal intermediate ca cert public key: %w", err)
	}
	keyPubDER, err := x509.MarshalPKIXPublicKey(&intermediateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal intermediate ca key public key: %w", err)
	}
	if !bytes.Equal(certPubDER, keyPubDER) {
		return fmt.Errorf("intermediate ca key does not match intermediate certificate")
	}
	if err := intermediateCert.CheckSignatureFrom(rootCert); err != nil {
		return fmt.Errorf("intermediate ca cert is not signed by root ca cert: %w", err)
	}
	return nil
}

func (p *internalPKIBootstrap) issueLeaf(serviceName, clusterID, rootDomain string, host inventory.Host) (string, string, error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("generate key: %w", err)
	}
	serial, err := crand.Int(crand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", fmt.Errorf("generate serial: %w", err)
	}
	dnsNames, ipAddresses := bootstrapInternalCertSANs(serviceName, clusterID, rootDomain, host)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   serviceName,
			Organization: []string{"FrameWorks Internal"},
		},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(72 * time.Hour),
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(crand.Reader, template, p.intermediateCert, &leafKey.PublicKey, p.intermediateKey)
	if err != nil {
		return "", "", fmt.Errorf("sign certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return "", "", fmt.Errorf("marshal key: %w", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

func bootstrapInternalCertSANs(serviceName, clusterID, rootDomain string, host inventory.Host) ([]string, []net.IP) {
	dnsSeen := map[string]struct{}{}
	var dnsNames []string
	addDNS := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := dnsSeen[value]; ok {
			return
		}
		dnsSeen[value] = struct{}{}
		dnsNames = append(dnsNames, value)
	}
	addDNS(serviceName)
	addDNS(serviceName + ".internal")
	addDNS("localhost")
	if host.Name != "" {
		addDNS(host.Name + ".internal")
	}
	if clusterID != "" && rootDomain != "" {
		addDNS(fmt.Sprintf("%s.%s.%s", serviceName, clusterID, rootDomain))
	}

	ipSeen := map[string]struct{}{}
	var ipAddresses []net.IP
	addIP := func(value string) {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return
		}
		key := ip.String()
		if _, ok := ipSeen[key]; ok {
			return
		}
		ipSeen[key] = struct{}{}
		ipAddresses = append(ipAddresses, ip)
	}
	addIP(host.WireguardIP)
	addIP(host.ExternalIP)
	return dnsNames, ipAddresses
}

// Ingress / public-service derivation helpers live in cli/pkg/clusterderive so
// they are shared with the bootstrap-desired-state renderer. Aliases below keep
// existing call sites readable.
var (
	publicServiceRootDomain = clusterderive.PublicServiceRootDomain
	autoIngressDomains      = clusterderive.AutoIngressDomains
)

func reconcileServiceClusterAssignments(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) error {
	token, ok := runtimeData["service_token"].(string)
	if !ok || token == "" {
		return fmt.Errorf("missing Quartermaster connection info for service-cluster reconciliation: service_token not set")
	}

	client, err := newQuartermasterClient(ctx, manifest, runtimeData, sess)
	if err != nil {
		return fmt.Errorf("connect Quartermaster for service-cluster reconciliation: %w", err)
	}
	defer client.Close()

	var lastErr error
	for attempt := 1; attempt <= 6; attempt++ {
		lastErr = reconcileServiceClusterAssignmentsWithClient(ctx, cmd.OutOrStdout(), manifest, client)
		if lastErr == nil {
			return nil
		}
		if attempt == 6 {
			break
		}

		fmt.Fprintf(cmd.OutOrStdout(), "    Retry %d/5 after reconciliation failure: %v\n", attempt, lastErr)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}

	return lastErr
}

func reconcileServiceClusterAssignmentsWithClient(ctx context.Context, out io.Writer, manifest *inventory.Manifest, client serviceClusterAssignmentClient) error {
	fmt.Fprintln(out, "  Reconciling service-cluster assignments...")

	serviceIDs, err := serviceIDsByType(ctx, client)
	if err != nil {
		return err
	}

	poolAssignedServices := pkgdns.PoolAssignedServiceTypes()
	plans := make([]serviceAssignmentPlan, 0, len(poolAssignedServices))
	for _, serviceName := range poolAssignedServices {
		serviceID := serviceIDs[serviceName]
		if serviceID == "" {
			if len(enabledPoolAssignedManifestServices(manifest, serviceName)) > 0 {
				return fmt.Errorf("%s service is enabled but missing from Quartermaster service catalog", serviceName)
			}
			continue
		}

		instances, err := serviceInstancesForService(ctx, client, serviceID)
		if err != nil {
			return fmt.Errorf("list %s instances: %w", serviceName, err)
		}
		instances = serviceInstancesOnManifestHosts(manifest, instances)

		configs := enabledPoolAssignedManifestServices(manifest, serviceName)
		if len(configs) == 0 {
			plans = append(plans, serviceAssignmentPlan{serviceName: serviceName, instances: instances})
			continue
		}
		plan := serviceAssignmentPlan{
			serviceName: serviceName,
			instances:   instances,
		}
		for _, cfg := range configs {
			targets := clusterderive.LogicalServiceClusterIDs(cfg.name, cfg.svc, manifest)
			if len(targets) == 0 {
				return fmt.Errorf("%s has no logical media-cluster assignment", cfg.name)
			}
			instanceIDs, err := desiredServiceInstanceIDs(cfg.name, cfg.svc, instances)
			if err != nil {
				return err
			}
			for _, target := range targets {
				plan.assignments = append(plan.assignments, serviceAssignmentTarget{
					clusterID:   target,
					instanceIDs: instanceIDs,
					manifestKey: cfg.name,
				})
			}
		}
		plans = append(plans, plan)
	}

	for _, plan := range plans {
		if len(plan.instances) > 0 {
			if err := drainServiceAssignments(ctx, client, plan.serviceName, plan.instances); err != nil {
				return err
			}
		}
		for _, assignment := range plan.assignments {
			assignCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := client.AssignServiceToCluster(assignCtx, &pb.AssignServiceToClusterRequest{
				ClusterId:   assignment.clusterID,
				InstanceIds: assignment.instanceIDs,
				ServiceType: plan.serviceName,
			})
			cancel()
			if err != nil {
				return fmt.Errorf("assign %s to cluster %s: %w", assignment.manifestKey, assignment.clusterID, err)
			}

			ux.Success(out, fmt.Sprintf("%s assigned %d instance(s) to cluster %s", assignment.manifestKey, len(assignment.instanceIDs), assignment.clusterID))
		}
	}

	return nil
}

type serviceAssignmentPlan struct {
	serviceName string
	instances   []*pb.ServiceInstance
	assignments []serviceAssignmentTarget
}

type serviceAssignmentTarget struct {
	clusterID   string
	instanceIDs []string
	manifestKey string
}

type namedServiceConfig struct {
	name string
	svc  inventory.ServiceConfig
}

func enabledPoolAssignedManifestServices(manifest *inventory.Manifest, serviceType string) []namedServiceConfig {
	if manifest == nil {
		return nil
	}
	var configs []namedServiceConfig
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		resolvedType, ok := clusterderive.ManifestServiceType(name, svc)
		if !ok || resolvedType != serviceType || !pkgdns.IsPoolAssignedServiceType(resolvedType) {
			continue
		}
		configs = append(configs, namedServiceConfig{name: name, svc: svc})
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].name < configs[j].name })
	return configs
}

func serviceIDsByType(ctx context.Context, client serviceClusterAssignmentClient) (map[string]string, error) {
	resp, err := client.ListServices(ctx, &pb.CursorPaginationRequest{First: 1000})
	if err != nil {
		return nil, fmt.Errorf("list Quartermaster services: %w", err)
	}
	out := make(map[string]string, len(resp.GetServices()))
	for _, svc := range resp.GetServices() {
		serviceType := strings.TrimSpace(svc.GetType())
		if serviceType == "" {
			serviceType = strings.TrimSpace(svc.GetServiceId())
		}
		if serviceType == "" || svc.GetServiceId() == "" {
			continue
		}
		if _, exists := out[serviceType]; !exists || svc.GetServiceId() == serviceType {
			out[serviceType] = svc.GetServiceId()
		}
	}
	return out, nil
}

func serviceInstancesForService(ctx context.Context, client serviceClusterAssignmentClient, serviceID string) ([]*pb.ServiceInstance, error) {
	resp, err := client.ListServiceInstances(ctx, "", serviceID, "", &pb.CursorPaginationRequest{First: 1000})
	if err != nil {
		return nil, err
	}
	return resp.GetInstances(), nil
}

func serviceInstancesOnManifestHosts(manifest *inventory.Manifest, instances []*pb.ServiceInstance) []*pb.ServiceInstance {
	if manifest == nil || len(manifest.Hosts) == 0 || len(instances) == 0 {
		return nil
	}
	filtered := make([]*pb.ServiceInstance, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		nodeID := strings.TrimSpace(inst.GetNodeId())
		if nodeID == "" {
			continue
		}
		if _, ok := manifest.GetHost(nodeID); ok {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

func drainServiceAssignments(ctx context.Context, client serviceClusterAssignmentClient, serviceName string, instances []*pb.ServiceInstance) error {
	for _, inst := range instances {
		if inst.GetId() == "" {
			continue
		}
		drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := client.DrainServiceInstance(drainCtx, &pb.DrainServiceInstanceRequest{
			InstanceId:  inst.GetId(),
			ServiceType: serviceName,
		})
		cancel()
		if err == nil || status.Code(err) == codes.NotFound {
			continue
		}
		return fmt.Errorf("clear existing %s assignments for instance %s: %w", serviceName, inst.GetId(), err)
	}
	return nil
}

func desiredServiceInstanceIDs(serviceName string, svc inventory.ServiceConfig, instances []*pb.ServiceInstance) ([]string, error) {
	hosts := serviceHosts(svc)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%s needs host or hosts before service-cluster assignments can be reconciled", serviceName)
	}

	byHost := make(map[string][]string)
	for _, inst := range instances {
		if inst.GetStatus() != "running" {
			continue
		}
		nodeID := strings.TrimSpace(inst.GetNodeId())
		if nodeID == "" || inst.GetId() == "" {
			continue
		}
		byHost[nodeID] = append(byHost[nodeID], inst.GetId())
	}

	var ids []string
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		hostIDs := byHost[host]
		if len(hostIDs) == 0 {
			return nil, fmt.Errorf("%s has no running service instance on manifest host %s", serviceName, host)
		}
		ids = append(ids, hostIDs...)
	}
	sort.Strings(ids)
	return ids, nil
}

type removedServicePlacement struct {
	serviceName  string
	deployName   string
	serviceID    string
	svc          inventory.ServiceConfig
	instance     *pb.ServiceInstance
	nodeID       string
	cleanupModes []string
}

type removedPlacementCleanupFunc func(context.Context, removedServicePlacement) error

func reconcileRemovedServicePlacements(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, phase orchestrator.Phase, runtimeData map[string]any, sess *remoteaccess.Session, sshPool *ssh.Pool) error {
	if phase == orchestrator.PhaseInfrastructure {
		return nil
	}

	client, err := newQuartermasterClient(ctx, manifest, runtimeData, sess)
	if err != nil {
		return fmt.Errorf("connect Quartermaster for removed-placement reconciliation: %w", err)
	}
	defer client.Close()

	cleanup := func(cleanupCtx context.Context, placement removedServicePlacement) error {
		host, ok := manifest.GetHost(placement.nodeID)
		if !ok {
			return fmt.Errorf("%s stale instance %s is on unknown host %q", placement.serviceName, placement.instance.GetId(), placement.nodeID)
		}
		prov, provErr := provisioner.GetProvisioner(placement.deployName, sshPool)
		if provErr != nil {
			return provErr
		}
		for _, mode := range placement.cleanupModes {
			mode = strings.TrimSpace(mode)
			if mode == "" {
				continue
			}
			config := provisioner.ServiceConfig{
				Mode:       mode,
				DeployName: placement.deployName,
				Port:       placement.svc.Port,
				Metadata:   map[string]any{"_cleanup_only": true},
			}
			if err := prov.Cleanup(cleanupCtx, host, config); err != nil {
				return err
			}
		}
		return nil
	}

	return reconcileRemovedServicePlacementsWithClient(ctx, cmd.OutOrStdout(), manifest, phase, client, cleanup)
}

func printDryRunRemovedServicePlacementPlan(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, phase orchestrator.Phase, sharedEnv map[string]string) {
	if phase == orchestrator.PhaseInfrastructure {
		return
	}

	runtimeData := map[string]any{}
	if token := strings.TrimSpace(sharedEnv["SERVICE_TOKEN"]); token != "" {
		runtimeData["service_token"] = token
	}

	sshKey := stringFlag(cmd, "ssh-key").Value
	sess, err := remoteaccess.OpenSession(remoteaccess.Options{
		Manifest:      manifest,
		SSHKeyPath:    sshKey,
		AllowInsecure: isDevProfile(manifest),
	})
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed service cleanup plan: inconclusive: %v\n\n", err)
		return
	}
	defer sess.Close()

	client, err := newQuartermasterClient(ctx, manifest, runtimeData, sess)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed service cleanup plan: inconclusive: %v\n\n", err)
		return
	}
	defer client.Close()

	placements, err := removedServicePlacements(ctx, manifest, phase, client)
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed service cleanup plan: inconclusive: %v\n\n", err)
		return
	}
	writeRemovedServicePlacementDryRunPlan(cmd.OutOrStdout(), placements)
}

func writeRemovedServicePlacementDryRunPlan(out io.Writer, placements []removedServicePlacement) {
	fmt.Fprintln(out, "Removed service cleanup plan:")
	if len(placements) == 0 {
		fmt.Fprintln(out, "  - no stale managed service placements detected")
		fmt.Fprintln(out)
		return
	}
	for _, placement := range placements {
		action := "cleanup"
		if pkgdns.IsPoolAssignedServiceType(placement.serviceName) {
			action = "drain pool assignment and cleanup"
		}
		fmt.Fprintf(out, "  - %s on %s: would %s (%s)\n", placement.serviceName, placement.nodeID, action, strings.Join(placement.cleanupModes, "+"))
	}
	fmt.Fprintln(out)
}

func reconcileRemovedServicePlacementsWithClient(ctx context.Context, out io.Writer, manifest *inventory.Manifest, phase orchestrator.Phase, client serviceClusterAssignmentClient, cleanup removedPlacementCleanupFunc) error {
	placements, err := removedServicePlacements(ctx, manifest, phase, client)
	if err != nil {
		return err
	}
	if len(placements) == 0 {
		return nil
	}
	if cleanup == nil {
		return fmt.Errorf("removed-placement cleanup function is required")
	}

	fmt.Fprintln(out, "  Reconciling removed service placements...")
	for _, placement := range placements {
		if pkgdns.IsPoolAssignedServiceType(placement.serviceName) {
			drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, drainErr := client.DrainServiceInstance(drainCtx, &pb.DrainServiceInstanceRequest{
				InstanceId:  placement.instance.GetId(),
				ServiceType: placement.serviceName,
			})
			cancel()
			if drainErr != nil && status.Code(drainErr) != codes.NotFound {
				return fmt.Errorf("drain stale %s instance %s: %w", placement.serviceName, placement.instance.GetId(), drainErr)
			}
		}

		if err := cleanup(ctx, placement); err != nil {
			return fmt.Errorf("cleanup stale %s on %s: %w", placement.serviceName, placement.nodeID, err)
		}
		ux.Success(out, fmt.Sprintf("%s removed from %s", placement.serviceName, placement.nodeID))
	}
	return nil
}

func removedServicePlacements(ctx context.Context, manifest *inventory.Manifest, phase orchestrator.Phase, client serviceClusterAssignmentClient) ([]removedServicePlacement, error) {
	serviceIDs, err := serviceIDsByType(ctx, client)
	if err != nil {
		return nil, err
	}

	configs := serviceConfigsForPlacementPhase(manifest, phase, serviceIDs)
	var placements []removedServicePlacement
	for serviceName, svc := range configs {
		serviceID := serviceIDs[serviceName]
		if serviceID == "" {
			continue
		}
		deployName, ok := servicedefs.DeployName(serviceName, svc.Deploy)
		if !ok {
			continue
		}
		instances, err := serviceInstancesForService(ctx, client, serviceID)
		if err != nil {
			return nil, fmt.Errorf("list %s instances: %w", serviceName, err)
		}
		desired := desiredPlacementHosts(manifest, serviceName, svc)
		for _, inst := range instances {
			if !serviceInstanceShouldBePruned(inst, desired) {
				continue
			}
			nodeID := strings.TrimSpace(inst.GetNodeId())
			if _, ok := manifest.GetHost(nodeID); !ok {
				continue
			}
			cleanupModes := removedPlacementCleanupModes(svc, inst)
			if len(cleanupModes) == 0 {
				continue
			}
			placements = append(placements, removedServicePlacement{
				serviceName:  serviceName,
				deployName:   deployName,
				serviceID:    serviceID,
				svc:          svc,
				instance:     inst,
				nodeID:       nodeID,
				cleanupModes: cleanupModes,
			})
		}
	}

	sort.Slice(placements, func(i, j int) bool {
		if placements[i].serviceName != placements[j].serviceName {
			return placements[i].serviceName < placements[j].serviceName
		}
		return placements[i].nodeID < placements[j].nodeID
	})
	return placements, nil
}

func serviceConfigsForPlacementPhase(manifest *inventory.Manifest, phase orchestrator.Phase, serviceIDs map[string]string) map[string]inventory.ServiceConfig {
	out := map[string]inventory.ServiceConfig{}
	if manifest == nil {
		return out
	}
	if phase == orchestrator.PhaseApplications || phase == orchestrator.PhaseAll {
		for name, svc := range manifest.Services {
			out[name] = svc
		}
		for _, serviceType := range pkgdns.PoolAssignedServiceTypes() {
			configs := enabledPoolAssignedManifestServices(manifest, serviceType)
			if len(configs) == 0 {
				continue
			}
			out[serviceType] = aggregatePlacementServiceConfig(serviceType, configs)
		}
	}
	if phase == orchestrator.PhaseInterfaces || phase == orchestrator.PhaseAll {
		for name, svc := range manifest.Interfaces {
			out[name] = svc
		}
		for name, svc := range manifest.Observability {
			out[name] = svc
		}
	}
	for name := range serviceIDs {
		if _, ok := out[name]; ok || !phaseIncludesDeletedService(phase, name) {
			continue
		}
		out[name] = inventory.ServiceConfig{Enabled: false}
	}
	return out
}

func aggregatePlacementServiceConfig(serviceType string, configs []namedServiceConfig) inventory.ServiceConfig {
	merged := inventory.ServiceConfig{
		Enabled: true,
		Deploy:  serviceType,
	}
	seenHosts := map[string]struct{}{}
	var mode string
	modeSet := false
	for _, cfg := range configs {
		if !cfg.svc.Enabled {
			continue
		}
		for _, host := range serviceHosts(cfg.svc) {
			host = strings.TrimSpace(host)
			if host == "" {
				continue
			}
			if _, ok := seenHosts[host]; ok {
				continue
			}
			seenHosts[host] = struct{}{}
			merged.Hosts = append(merged.Hosts, host)
		}
		if cfg.svc.Port != 0 && merged.Port == 0 {
			merged.Port = cfg.svc.Port
		}
		cfgMode := strings.TrimSpace(cfg.svc.Mode)
		switch {
		case !modeSet:
			mode = cfgMode
			modeSet = true
		case mode != cfgMode:
			mode = ""
		}
	}
	sort.Strings(merged.Hosts)
	merged.Mode = mode
	return merged
}

func phaseIncludesDeletedService(phase orchestrator.Phase, serviceName string) bool {
	if phase == orchestrator.PhaseAll {
		return true
	}
	def, ok := servicedefs.Lookup(serviceName)
	if !ok {
		return false
	}
	switch phase {
	case orchestrator.PhaseApplications:
		return def.Role != "interface" && def.Role != "observability"
	case orchestrator.PhaseInterfaces:
		return def.Role == "interface" || def.Role == "observability"
	default:
		return false
	}
}

func removedPlacementCleanupModes(svc inventory.ServiceConfig, inst *pb.ServiceInstance) []string {
	mode := strings.TrimSpace(svc.Mode)
	if mode != "" {
		return []string{mode}
	}
	if strings.TrimSpace(inst.GetContainerId()) != "" {
		return []string{"docker"}
	}
	return []string{"native", "docker"}
}

func desiredPlacementHosts(manifest *inventory.Manifest, serviceName string, svc inventory.ServiceConfig) map[string]struct{} {
	desired := map[string]struct{}{}
	if !svc.Enabled {
		return desired
	}
	hosts := serviceHosts(svc)
	if serviceName == "privateer" && len(hosts) == 0 {
		hosts = orchestrator.EffectivePrivateerHosts(svc, manifest.Hosts)
	}
	if serviceName == "vmagent" && len(hosts) == 0 {
		hosts = orchestrator.EffectiveVMAgentHosts(svc, manifest.Hosts)
	}
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host != "" {
			desired[host] = struct{}{}
		}
	}
	return desired
}

func serviceInstanceShouldBePruned(inst *pb.ServiceInstance, desiredHosts map[string]struct{}) bool {
	if inst == nil || strings.TrimSpace(inst.GetId()) == "" {
		return false
	}
	nodeID := strings.TrimSpace(inst.GetNodeId())
	if nodeID == "" {
		return false
	}
	if _, ok := desiredHosts[nodeID]; ok {
		return false
	}
	switch inst.GetStatus() {
	case "running", "starting", "active", "unknown", "":
		return true
	default:
		return false
	}
}

// buildTaskConfig creates a ServiceConfig for a task.
func buildTaskConfig(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]any, force bool, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, releaseRepos []string) (provisioner.ServiceConfig, error) {
	config := provisioner.ServiceConfig{
		Mode:     "docker",
		Version:  "stable",
		Port:     provisioner.ServicePorts[task.Type],
		Metadata: make(map[string]any),
		Force:    force,
	}

	config.DeployName = task.Type

	// Inject cluster ID and node ID from resolved task
	if task.ClusterID != "" {
		config.Metadata["cluster_id"] = task.ClusterID
	}
	if task.Host != "" {
		config.Metadata["node_id"] = task.Host
	}
	if len(releaseRepos) > 0 {
		repos := make([]string, len(releaseRepos))
		copy(repos, releaseRepos)
		config.Metadata["gitops_repositories"] = repos
	}
	if manifest != nil {
		config.Metadata["platform_channel"] = manifest.ResolvedChannel()
	}

	// Copy runtime data
	for k, v := range runtimeData {
		config.Metadata[k] = v
	}

	// Use base service name for manifest lookups (handles "bridge@host" → "bridge")
	baseName := task.ServiceID
	config.Metadata["service_name"] = baseName

	if manifest != nil {
		// Service overrides
		if _, svc, ok := serviceConfigForTask(manifest.Services, task); ok {
			if svc.Mode != "" {
				config.Mode = svc.Mode
			}
			if svc.Version != "" {
				config.Version = svc.Version
			}
			if svc.Image != "" {
				config.Image = svc.Image
			}
			if svc.BinaryURL != "" {
				config.BinaryURL = svc.BinaryURL
			}
			if svc.EnvFile != "" {
				config.EnvFile = svc.EnvFile
			}
			for k, v := range svc.Config {
				config.Metadata[k] = v
			}
			if port, err := resolvePort(baseName, svc); err == nil && port != 0 {
				config.Port = port
			}
		}
		// Interface overrides
		if _, iface, ok := serviceConfigForTask(manifest.Interfaces, task); ok {
			if iface.Mode != "" {
				config.Mode = iface.Mode
			}
			if iface.Version != "" {
				config.Version = iface.Version
			}
			if iface.Image != "" {
				config.Image = iface.Image
			}
			if iface.BinaryURL != "" {
				config.BinaryURL = iface.BinaryURL
			}
			if iface.EnvFile != "" {
				config.EnvFile = iface.EnvFile
			}
			for k, v := range iface.Config {
				config.Metadata[k] = v
			}
			if port, err := resolvePort(baseName, iface); err == nil && port != 0 {
				config.Port = port
			}
		}
		// Observability overrides
		if _, obs, ok := serviceConfigForTask(manifest.Observability, task); ok {
			config.Metadata["component"] = baseName
			if obs.Mode != "" {
				config.Mode = obs.Mode
			}
			if obs.Version != "" {
				config.Version = obs.Version
			}
			if obs.Image != "" {
				config.Image = obs.Image
			}
			if obs.BinaryURL != "" {
				config.BinaryURL = obs.BinaryURL
			}
			if obs.EnvFile != "" {
				config.EnvFile = obs.EnvFile
			}
			for k, v := range obs.Config {
				config.Metadata[k] = v
			}
			if port, err := resolvePort(baseName, obs); err == nil && port != 0 {
				config.Port = port
			}
		}
		// Infrastructure overrides
		switch task.Type {
		case "postgres":
			if manifest.Infrastructure.Postgres != nil {
				if inst := resolvePostgresInstanceByID(task.InstanceID, manifest); inst != nil {
					config.Mode = "native"
					if inst.Mode != "" {
						config.Mode = inst.Mode
					}
					config.Version = manifest.Infrastructure.Postgres.Version
					if inst.Version != "" {
						config.Version = inst.Version
					}
					if config.Version == "" {
						config.Version = "16"
					}
					config.Port = postgresInstancePort(inst)
					config.Metadata["instance"] = inst.Name
					config.Metadata["instance_name"] = inst.Name
					instancePassword := postgresInstancePassword(inst, sharedEnv)
					if len(inst.Databases) > 0 {
						config.Metadata["databases"] = databaseConfigsToMetadata(inst.Databases, instancePassword)
					}
					if len(inst.Tuning) > 0 {
						config.Metadata["tuning"] = stringMapToAnyMap(inst.Tuning)
					}
					if instancePassword != "" {
						config.Metadata["postgres_password"] = instancePassword
					}
					for k, v := range inst.Config {
						config.Metadata["postgres_"+k] = v
						config.Metadata[k] = v
					}
				} else {
					if manifest.Infrastructure.Postgres.Mode != "" {
						config.Mode = manifest.Infrastructure.Postgres.Mode
					}
					if manifest.Infrastructure.Postgres.Version != "" {
						config.Version = manifest.Infrastructure.Postgres.Version
					}
					if manifest.Infrastructure.Postgres.Port != 0 {
						config.Port = manifest.Infrastructure.Postgres.Port
					}
					if len(manifest.Infrastructure.Postgres.Databases) > 0 {
						config.Metadata["databases"] = databaseConfigsToMetadata(manifest.Infrastructure.Postgres.Databases, "")
					}
				}
			}
		case "clickhouse":
			if manifest.Infrastructure.ClickHouse != nil {
				if manifest.Infrastructure.ClickHouse.Mode != "" {
					config.Mode = manifest.Infrastructure.ClickHouse.Mode
				}
				if manifest.Infrastructure.ClickHouse.Version != "" {
					config.Version = manifest.Infrastructure.ClickHouse.Version
				}
				if manifest.Infrastructure.ClickHouse.Port != 0 {
					config.Port = manifest.Infrastructure.ClickHouse.Port
				}
			}
		case "kafka":
			if manifest.Infrastructure.Kafka != nil {
				cluster := findKafkaClusterView(manifest, task.ClusterID)
				if cluster == nil {
					return config, fmt.Errorf("kafka task %s: no cluster view for region %q", task.Name, task.ClusterID)
				}
				if manifest.Infrastructure.Kafka.Mode != "" {
					config.Mode = manifest.Infrastructure.Kafka.Mode
				}
				if manifest.Infrastructure.Kafka.Version != "" {
					config.Version = manifest.Infrastructure.Kafka.Version
				}
				if task.InstanceID != "" {
					if brokerID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["node_id"] = brokerID
					}
				}
				config.Metadata["cluster_id"] = cluster.ClusterID
				if cluster.RegionID != "" {
					config.Metadata["region_id"] = cluster.RegionID
				}

				if len(cluster.Controllers) > 0 {
					// Dedicated mode: broker-only
					config.Metadata["role"] = "broker"
					config.Metadata["controllers"] = kafkaControllersToMetadata(manifest, cluster)
					config.Metadata["controller_quorum_voters"] = buildDedicatedControllerQuorum(manifest, cluster)
					config.Metadata["brokers"] = kafkaBrokersToMetadata(manifest, cluster)
					config.Metadata["bootstrap_servers"] = buildBootstrapServers(manifest, cluster)
				} else {
					// Combined mode
					config.Metadata["controller_quorum_voters"] = buildControllerQuorum(manifest, cluster)
					controllerPort := cluster.ControllerPort
					if controllerPort == 0 {
						controllerPort = 9093
					}
					config.Metadata["controller_port"] = controllerPort
				}

				if len(cluster.Topics) > 0 {
					config.Metadata["topics"] = kafkaTopicsToMetadata(cluster.Topics)
				}
				if brokerCount := len(cluster.Brokers); brokerCount > 0 {
					config.Metadata["broker_count"] = brokerCount
				}
				if cluster.DeleteTopicEnable != nil {
					config.Metadata["delete_topic_enable"] = *cluster.DeleteTopicEnable
				}
				if cluster.MinInSyncReplicas > 0 {
					config.Metadata["min_insync_replicas"] = cluster.MinInSyncReplicas
				}
				if cluster.OffsetsTopicReplicationFactor > 0 {
					config.Metadata["offsets_topic_replication_factor"] = cluster.OffsetsTopicReplicationFactor
				}
				if cluster.TransactionStateLogReplicationFactor > 0 {
					config.Metadata["transaction_state_log_replication_factor"] = cluster.TransactionStateLogReplicationFactor
				}
				if cluster.TransactionStateLogMinISR > 0 {
					config.Metadata["transaction_state_log_min_isr"] = cluster.TransactionStateLogMinISR
				}
			}
		case "kafka-mirrormaker":
			if manifest.Infrastructure.Kafka != nil && manifest.Infrastructure.Kafka.MirrorMaker != nil {
				mm := manifest.Infrastructure.Kafka.MirrorMaker
				if manifest.Infrastructure.Kafka.Version != "" {
					config.Version = manifest.Infrastructure.Kafka.Version
				}
				if mm.HeapOpts != "" {
					config.Metadata["heap_opts"] = mm.HeapOpts
				}
				if mm.Replicas > 0 {
					config.Metadata["replicas"] = mm.Replicas
				}
				if mm.TaskCount > 0 {
					config.Metadata["task_count"] = mm.TaskCount
				}
				views := allKafkaClusters(manifest)
				target := aggregatorKafkaClusterView(manifest)
				if target != nil {
					targetAlias := kafkaClusterAlias(manifest, target)
					config.Metadata["target"] = map[string]any{
						"alias":             targetAlias,
						"region_id":         target.RegionID,
						"bootstrap_servers": kafkaBrokersBootstrap(manifest, target),
						"replicas":          replicasForCluster(target, mm.Replicas),
					}
				}
				sources := make([]map[string]any, 0, len(views))
				for i := range views {
					v := views[i]
					if isAggregatorKafkaClusterView(manifest, &v) {
						continue
					}
					alias := kafkaClusterAlias(manifest, &v)
					sources = append(sources, map[string]any{
						"alias":             alias,
						"region_id":         v.RegionID,
						"bootstrap_servers": kafkaBrokersBootstrap(manifest, &v),
						"topics":            mirrorTopicsForRegional(manifest, v.RegionID),
						"replicas":          replicasForCluster(&v, 0),
					})
				}
				config.Metadata["sources"] = sources
				if local := serviceKafkaCluster(manifest, "", manifestTaskRegion(manifest, task)); local != nil {
					if alias := kafkaClusterAlias(manifest, local); alias != "" {
						config.Metadata["local_cluster_alias"] = alias
					}
				}
			}
		case "kafka-controller":
			if manifest.Infrastructure.Kafka != nil {
				cluster := findKafkaClusterView(manifest, task.ClusterID)
				if cluster == nil {
					return config, fmt.Errorf("kafka-controller task %s: no cluster view for region %q", task.Name, task.ClusterID)
				}
				if manifest.Infrastructure.Kafka.Mode != "" {
					config.Mode = manifest.Infrastructure.Kafka.Mode
				}
				if manifest.Infrastructure.Kafka.Version != "" {
					config.Version = manifest.Infrastructure.Kafka.Version
				}
				config.Metadata["role"] = "controller"
				config.Metadata["cluster_id"] = cluster.ClusterID
				if cluster.RegionID != "" {
					config.Metadata["region_id"] = cluster.RegionID
				}
				config.Metadata["controllers"] = kafkaControllersToMetadata(manifest, cluster)
				config.Metadata["controller_quorum_voters"] = buildDedicatedControllerQuorum(manifest, cluster)
				config.Metadata["brokers"] = kafkaBrokersToMetadata(manifest, cluster)
				config.Metadata["bootstrap_servers"] = buildBootstrapServers(manifest, cluster)
				config.Metadata["initial_controllers"] = buildInitialControllers(manifest, cluster)
				if task.InstanceID != "" {
					if ctrlID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["node_id"] = ctrlID
						for _, ctrl := range cluster.Controllers {
							if ctrl.ID == ctrlID {
								config.Metadata["bind_host"] = manifest.MeshAddress(ctrl.Host)
								if ctrl.Port != 0 {
									config.Port = ctrl.Port
								} else {
									config.Port = 9093
								}
								break
							}
						}
					}
				}
			}
		case "zookeeper":
			if manifest.Infrastructure.Zookeeper != nil {
				if manifest.Infrastructure.Zookeeper.Mode != "" {
					config.Mode = manifest.Infrastructure.Zookeeper.Mode
				}
				if manifest.Infrastructure.Zookeeper.Version != "" {
					config.Version = manifest.Infrastructure.Zookeeper.Version
				}
				if nodeConfig := resolveZookeeperNodeByID(task.InstanceID, manifest); nodeConfig != nil {
					if nodeConfig.Port != 0 {
						config.Port = nodeConfig.Port
					}
					if nodeConfig.ServerID != 0 {
						config.Metadata["server_id"] = nodeConfig.ServerID
					}
					if len(nodeConfig.Servers) > 0 {
						config.Metadata["servers"] = nodeConfig.Servers
					}
				}
			}
		case "redis":
			if manifest.Infrastructure.Redis != nil {
				if manifest.Infrastructure.Redis.Mode != "" {
					config.Mode = manifest.Infrastructure.Redis.Mode
				}
				if manifest.Infrastructure.Redis.Version != "" {
					config.Version = manifest.Infrastructure.Redis.Version
				}
				// Sentinel replica/sentinel tasks carry the canonical
				// instance name in task.Metadata["instance_label"]; primary
				// tasks key off task.InstanceID directly.
				lookupName := task.InstanceID
				if label, ok := task.Metadata["instance_label"].(string); ok && label != "" {
					lookupName = label
				}
				if inst := resolveRedisInstanceForTask(lookupName, task.ClusterID, task.Host, manifest); inst != nil {
					engine := manifest.Infrastructure.Redis.Engine
					if inst.Engine != "" {
						engine = inst.Engine
					}
					if engine != "" {
						config.Metadata["engine"] = engine
					}
					if inst.Port != 0 {
						config.Port = inst.Port
					}
					config.Metadata["instance"] = inst.Name
					config.Metadata["instance_name"] = inst.Name
					if inst.Password != "" {
						password, err := inventory.ResolveSharedEnvPlaceholder(inst.Password, sharedEnv)
						if err != nil {
							return config, fmt.Errorf("redis %s password: %w", inst.Name, err)
						}
						config.Metadata["password"] = password
					} else if password := strings.TrimSpace(sharedEnv["REDIS_"+envNameToken(inst.Name)+"_PASSWORD"]); password != "" {
						config.Metadata["password"] = password
					}
					for k, v := range inst.Config {
						config.Metadata["redis_"+k] = v
						config.Metadata[k] = v
					}
					if _, ok := config.Metadata["bind"]; !ok {
						if host, hostOK := manifest.GetHost(task.Host); hostOK && strings.TrimSpace(host.WireguardIP) != "" {
							config.Metadata["bind"] = fmt.Sprintf("127.0.0.1 %s", strings.TrimSpace(host.WireguardIP))
						}
					}
					// HA role wiring. Default is "primary"; replica/sentinel
					// tasks supply primary_host + (for sentinel) the
					// quorum/master_name needed by the role's templates.
					role := "primary"
					if r, ok := task.Metadata["redis_role"].(string); ok && r != "" {
						role = r
					}
					config.Metadata["redis_role"] = role
					if role != "primary" {
						if h, ok := task.Metadata["primary_host"].(string); ok && h != "" {
							config.Metadata["redis_primary_host"] = manifest.MeshAddress(h)
						}
						port := inst.Port
						if port == 0 {
							port = 6379
						}
						config.Metadata["redis_primary_port"] = port
					}
					if role == "sentinel" {
						master := strings.TrimSpace(inst.MasterName)
						if master == "" {
							master = inst.Name
						}
						config.Metadata["redis_master_name"] = master
						if sp, ok := task.Metadata["sentinel_port"].(int); ok && sp > 0 {
							config.Metadata["redis_sentinel_port"] = sp
						}
						// Quorum = floor(N/2) + 1 where N is the number of
						// sentinels declared. Defaults to 2 if absent.
						if n := len(inst.Sentinels); n > 0 {
							config.Metadata["redis_sentinel_quorum"] = (n / 2) + 1
						}
					}
				}
			}
		case "yugabyte":
			if pg := manifest.Infrastructure.Postgres; pg != nil {
				config.Port = pg.EffectivePort()
				config.Version = pg.Version
				config.Metadata["master_addresses"] = pg.MasterAddresses(manifest.MeshAddress)
				config.Metadata["replication_factor"] = pg.EffectiveReplicationFactor()
				if task.InstanceID != "" {
					if nodeID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["node_id"] = nodeID
					}
				}
				if len(pg.Databases) > 0 {
					config.Metadata["databases"] = yugabyteDatabaseConfigsToMetadata(expandedYugabyteDatabaseConfigs(pg.Databases, manifest), manifest, sharedEnv, clusterEnvs, "")
				}
			}
		}
	}

	switch task.Type {
	case "kafka", "kafka-controller", "yugabyte", "zookeeper", "clickhouse":
		if task.Host != "" {
			config.Metadata["advertised_host"] = manifest.MeshAddress(task.Host)
		}
	}

	// Override for infrastructure (Redis uses manifest mode, not forced native)
	if task.Phase == orchestrator.PhaseInfrastructure && task.Type != "zookeeper" && task.Type != "redis" {
		config.Mode = "native"
		// Keep manifest-specified version for infra with explicit native version
		// semantics; only fall back to "latest" for the remaining legacy cases.
		keepVersion := task.Type == "yugabyte" || task.Type == "postgres" || task.Type == "kafka" || task.Type == "kafka-controller" || task.Type == "clickhouse"
		if !keepVersion || config.Version == "" {
			config.Version = "latest"
		}
	}

	// Native override for Privateer + inject mesh node identity
	if task.Type == "privateer" {
		config.Mode = "native"
		if services := internalGRPCLeafServicesForHost(manifest, task.Host); len(services) > 0 {
			config.Metadata["expected_internal_grpc_services"] = services
		}

		if selfHost, ok := manifest.GetHost(task.Host); ok {
			config.Metadata["static_peers"] = buildPrivateerStaticPeers(manifest, task.Host)
			config.Metadata["static_dns"] = buildPrivateerSeedDNS(manifest, task.Host)
			if selfHost.WireguardIP != "" {
				config.Metadata["wireguard_ip"] = selfHost.WireguardIP
			}
			if selfHost.WireguardPrivateKey != "" {
				config.Metadata["wireguard_private_key"] = selfHost.WireguardPrivateKey
			}
			if selfHost.WireguardPort != 0 {
				config.Metadata["wireguard_port"] = selfHost.WireguardPort
			}
			// Adopted-local hosts opt out of SOPS key rendering so the
			// Ansible role preserves the on-disk private key.
			if selfHost.WireguardPrivateKeyManaged != nil {
				config.Metadata["wireguard_private_key_managed"] = *selfHost.WireguardPrivateKeyManaged
			}
			if selfHost.WireguardPrivateKeyFile != "" {
				config.Metadata["wireguard_private_key_file"] = selfHost.WireguardPrivateKeyFile
			}
		}
	}

	if task.Type == "vmagent" {
		config.Mode = "docker"
		if targets := buildVMAgentScrapeTargets(manifest, task.Host); len(targets) > 0 {
			config.Metadata["scrape_targets"] = targets
		}
	}

	// Reverse proxy metadata: inject root_domain and colocated services
	if task.Type == "caddy" || task.Type == "nginx" {
		if manifest.RootDomain != "" {
			config.Metadata["root_domain"] = manifest.RootDomain
		}
		config.Metadata["node_id"] = task.Host
		clusterID := manifest.HostCluster(task.Host)
		if clusterID == "" {
			clusterID = manifest.ResolveCluster(task.Type)
		}
		localSvcs := make(map[string]any)
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Services, task.Type)
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Interfaces, task.Type)
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Observability, task.Type)
		if len(localSvcs) > 0 {
			config.Metadata["local_services"] = localSvcs
		}
		if routes := buildExtraProxyRoutesForHost(manifest, task.Host, clusterID); len(routes) > 0 {
			config.Metadata["extra_proxy_routes"] = routes
		}
		if sites := buildProxySitesForHost(manifest, task.Host, clusterID, localSvcs, config.Metadata["extra_proxy_routes"]); len(sites) > 0 {
			config.Metadata["proxy_sites"] = sites
		}
		hasPKI := manifest.Services["navigator"].Enabled
		if grpcAddr, ok := runtimeData["quartermaster_grpc_addr"].(string); ok && grpcAddr != "" {
			if hasPKI {
				config.Metadata["quartermaster_http_url"] = "https://quartermaster.internal:18002"
				config.Metadata["quartermaster_http_ca_file"] = "/etc/frameworks/pki/ca.crt"
			} else {
				config.Metadata["quartermaster_http_url"] = quartermasterHTTPURL(grpcAddr)
			}
		}
		if hasPKI {
			config.Metadata["navigator_http_url"] = fmt.Sprintf("https://navigator.internal:%d", defaultPort("navigator"))
			config.Metadata["navigator_http_ca_file"] = "/etc/frameworks/pki/ca.crt"
		} else {
			config.Metadata["navigator_http_url"] = fmt.Sprintf("http://navigator:%d", defaultPort("navigator"))
		}
		if serviceToken, ok := runtimeData["service_token"].(string); ok && serviceToken != "" {
			config.Metadata["service_token"] = serviceToken
		}
	}

	// Generate merged env vars for application/interface services.
	// Infrastructure services (postgres, kafka, etc.) manage their own config.
	if task.Phase != orchestrator.PhaseInfrastructure && manifest != nil {
		envVars, err := buildServiceEnvVars(task, manifest, runtimeData, config.EnvFile, manifestDir, sharedEnv, clusterEnvs)
		if err != nil {
			return config, fmt.Errorf("service %s: %w", task.Name, err)
		}
		config.EnvVars = envVars
		if missing := missingRequiredGeneratedEnv(manifest, task.Type, config.EnvVars); len(missing) > 0 {
			return config, fmt.Errorf("service %s: missing generated env var(s): %s; check that required dependency services are enabled in the manifest", task.Name, strings.Join(missing, ", "))
		}
	}
	if pki, ok := runtimeData["internal_pki_bootstrap"].(*internalPKIBootstrap); ok && pki != nil {
		config.Metadata["internal_ca_bundle_pem"] = pki.CABundlePEM
		if leafServiceName := internalGRPCLeafServiceName(task); leafServiceName != "" {
			host, _ := manifest.GetHost(task.Host)
			certPEM, keyPEM, certErr := pki.issueLeaf(leafServiceName, task.ClusterID, manifest.RootDomain, host)
			if certErr != nil {
				return config, fmt.Errorf("service %s: issue bootstrap internal gRPC certificate: %w", task.Name, certErr)
			}
			config.Metadata["internal_tls_cert_pem"] = certPEM
			config.Metadata["internal_tls_key_pem"] = keyPEM
		}
	}

	// Privateer starts in PhaseMesh before Quartermaster service DNS resolves,
	// so inject QM's gRPC endpoint as a mesh-IP literal. The SyncMesh loop
	// will retry against this endpoint until QM becomes reachable.
	if task.Type == "privateer" {
		if addr := quartermasterMeshGRPCAddr(manifest); addr != "" {
			if config.EnvVars == nil {
				config.EnvVars = map[string]string{}
			}
			config.EnvVars["QUARTERMASTER_GRPC_ADDR"] = addr
		}
	}

	return config, nil
}

func ensureNodeBaseline(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, pool *ssh.Pool) error {
	hostNames := plannedProvisionHosts(plan)
	if len(hostNames) == 0 {
		return nil
	}
	prov, err := provisioner.NewNodeBaselineProvisioner(pool)
	if err != nil {
		return fmt.Errorf("node baseline: %w", err)
	}
	ux.Subheading(cmd.OutOrStdout(), fmt.Sprintf("Ensuring Node Baseline (%d host(s))", len(hostNames)))

	type baselineTarget struct {
		name string
		host inventory.Host
	}
	targets := make([]baselineTarget, 0, len(hostNames))
	for _, hostName := range hostNames {
		host, ok := manifest.GetHost(hostName)
		if !ok {
			return fmt.Errorf("node baseline: host %s not found in manifest", hostName)
		}
		targets = append(targets, baselineTarget{name: hostName, host: host})
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(nodeBaselineConcurrency)
	for _, target := range targets {
		target := target
		g.Go(func() error {
			hostName := target.name
			fmt.Fprintf(cmd.OutOrStdout(), "  Ensuring node baseline on %s...\n", hostName)
			if err := runProvisionPhase(gCtx, provisionApplyTimeout, "node baseline", func(phaseCtx context.Context) error {
				return prov.Provision(phaseCtx, target.host, provisioner.ServiceConfig{
					Mode:     "native",
					Metadata: map[string]any{},
				})
			}); err != nil {
				return fmt.Errorf("node baseline %s: %w", hostName, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	ux.Success(cmd.OutOrStdout(), "Node baseline ready")
	return nil
}

func ensureNodeTuning(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, pool *ssh.Pool) error {
	hostNames := plannedProvisionHosts(plan)
	if len(hostNames) == 0 {
		return nil
	}
	prov, err := provisioner.NewNodeTuningProvisioner(pool)
	if err != nil {
		return fmt.Errorf("node tuning: %w", err)
	}
	ux.Subheading(cmd.OutOrStdout(), fmt.Sprintf("Ensuring Node Tuning (%d host(s))", len(hostNames)))

	type tuningTarget struct {
		name    string
		host    inventory.Host
		profile string
	}
	targets := make([]tuningTarget, 0, len(hostNames))
	for _, hostName := range hostNames {
		host, ok := manifest.GetHost(hostName)
		if !ok {
			return fmt.Errorf("node tuning: host %s not found in manifest", hostName)
		}
		targets = append(targets, tuningTarget{
			name:    hostName,
			host:    host,
			profile: nodeTuningProfileForHost(manifest, host),
		})
	}

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(nodeBaselineConcurrency)
	for _, target := range targets {
		g.Go(func() error {
			hostName := target.name
			fmt.Fprintf(cmd.OutOrStdout(), "  Ensuring node tuning on %s (profile=%s)...\n", hostName, target.profile)
			if err := runProvisionPhase(gCtx, provisionApplyTimeout, "node tuning", func(phaseCtx context.Context) error {
				return prov.Provision(phaseCtx, target.host, provisioner.ServiceConfig{
					Mode:     "native",
					Metadata: map[string]any{"profile": target.profile},
				})
			}); err != nil {
				return fmt.Errorf("node tuning %s: %w", hostName, err)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	ux.Success(cmd.OutOrStdout(), "Node tuning ready")
	return nil
}

func nodeTuningProfileForHost(manifest *inventory.Manifest, host inventory.Host) string {
	if manifest != nil && manifest.Type == "edge" {
		return "edge"
	}
	if slices.Contains(host.Roles, "edge") {
		return "edge"
	}
	if manifest != nil && host.Cluster != "" {
		if cluster, ok := manifest.Clusters[host.Cluster]; ok && cluster.Type == "edge" {
			return "edge"
		}
	}
	return "core"
}

func plannedProvisionHosts(plan *orchestrator.ExecutionPlan) []string {
	if plan == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, task := range plan.AllTasks {
		if task == nil || task.Host == "" {
			continue
		}
		seen[task.Host] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for hostName := range seen {
		out = append(out, hostName)
	}
	sort.Strings(out)
	return out
}

// quartermasterMeshGRPCAddr returns "<mesh_ip>:<grpc_port>" for the host
// running the `quartermaster` service, or "" if the service is not defined
// or its host has no mesh IP. This is the address Privateer agents SyncMesh
// against; using the mesh IP avoids a DNS dependency at cold boot.
func quartermasterMeshGRPCAddr(manifest *inventory.Manifest) string {
	if manifest == nil {
		return ""
	}
	svc, ok := manifest.Services["quartermaster"]
	if !ok {
		return ""
	}
	host := svc.Host
	if host == "" && len(svc.Hosts) > 0 {
		host = svc.Hosts[0]
	}
	if host == "" {
		return ""
	}
	addr := manifest.MeshAddress(host)
	if addr == "" {
		return ""
	}
	port := svc.GRPCPort
	if port == 0 {
		port = defaultGRPCPort("quartermaster")
	}
	return fmt.Sprintf("%s:%d", addr, port)
}

func containsHost(hosts []string, target string) bool {
	for _, h := range hosts {
		if h == target {
			return true
		}
	}
	return false
}

func ensureProvisionGeoIP(ctx context.Context, out io.Writer, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string, pool *ssh.Pool) error {
	if manifest == nil || manifest.GeoIP == nil || !manifest.GeoIP.Enabled {
		return nil
	}

	services := effectiveGeoIPServices(manifest, nil)
	if len(services) == 0 {
		return nil
	}

	source := effectiveGeoIPSource(manifest, "")
	filePath := effectiveGeoIPFilePath(manifest, "", manifestDir)
	licenseKey := effectiveGeoIPLicenseKey(sharedEnv, "")
	remotePath := effectiveGeoIPRemotePath(manifest, "")

	mmdbPath, cleanup, err := resolveGeoIPMMDBPath(ctx, source, filePath, licenseKey)
	if err != nil {
		return fmt.Errorf("geoip provisioning failed: %w", err)
	}
	defer cleanup()

	if _, err := uploadGeoIPToHosts(ctx, manifest, pool, mmdbPath, remotePath, services, false, out); err != nil {
		return fmt.Errorf("geoip provisioning failed: %w", err)
	}

	return nil
}

func buildVMAgentScrapeTargets(manifest *inventory.Manifest, hostName string) []map[string]any {
	if manifest == nil || hostName == "" {
		return nil
	}
	metricsCapableServices := map[string]struct{}{
		"bridge":           {},
		"chandler":         {},
		"commodore":        {},
		"deckhand":         {},
		"decklog":          {},
		"foghorn":          {},
		"helmsman":         {},
		"livepeer-gateway": {},
		"livepeer-signer":  {},
		"navigator":        {},
		"periscope-ingest": {},
		"periscope-query":  {},
		"privateer":        {},
		"purser":           {},
		"quartermaster":    {},
		"signalman":        {},
		"skipper":          {},
		"steward":          {},
		"victoriametrics":  {},
		"vmagent":          {},
	}

	type target struct {
		name   string
		port   int
		path   string
		labels map[string]string
	}

	var targets []target
	addTarget := func(name string, svc inventory.ServiceConfig, source string) {
		if !svc.Enabled {
			return
		}
		if _, ok := metricsCapableServices[name]; !ok {
			return
		}
		if svc.Host != hostName && !containsHost(svc.Hosts, hostName) {
			return
		}
		port, err := resolvePort(name, svc)
		if err != nil || port == 0 {
			return
		}
		path := "/metrics"
		if name == "victoriametrics" {
			path = "/metrics"
		}
		targets = append(targets, target{
			name: name,
			port: port,
			path: path,
			labels: map[string]string{
				"frameworks_service": name,
				"frameworks_source":  source,
				"node_id":            hostName,
			},
		})
	}

	for name, svc := range manifest.Services {
		addTarget(name, svc, "services")
	}
	for name, svc := range manifest.Interfaces {
		addTarget(name, svc, "interfaces")
	}
	for name, svc := range manifest.Observability {
		if name == "grafana" {
			continue
		}
		addTarget(name, svc, "observability")
	}

	if len(targets) == 0 {
		return nil
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].name == targets[j].name {
			return targets[i].port < targets[j].port
		}
		return targets[i].name < targets[j].name
	})

	result := make([]map[string]any, 0, len(targets))
	for _, tgt := range targets {
		result = append(result, map[string]any{
			"job_name": tgt.name,
			"targets":  []string{fmt.Sprintf("127.0.0.1:%d", tgt.port)},
			"path":     tgt.path,
			"labels":   tgt.labels,
		})
	}

	return result
}

func defaultVictoriaMetricsHost(manifest *inventory.Manifest) (string, int) {
	if manifest == nil {
		return "", 0
	}
	obs, ok := manifest.Observability["victoriametrics"]
	if !ok || !obs.Enabled {
		return "", 0
	}
	hostName := obs.Host
	if hostName == "" && len(obs.Hosts) > 0 {
		hostName = obs.Hosts[0]
	}
	if hostName == "" {
		return "", 0
	}
	port, err := resolvePort("victoriametrics", obs)
	if err != nil || port == 0 {
		port = provisioner.ServicePorts["victoriametrics"]
	}
	return manifestMeshHostname(manifest, hostName), port
}

func defaultVictoriaMetricsWriteURL(manifest *inventory.Manifest) string {
	host, port := defaultVictoriaMetricsHost(manifest)
	if host == "" || port == 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/api/v1/write", host, port)
}

func defaultVictoriaMetricsDatasourceURL(manifest *inventory.Manifest) string {
	host, port := defaultVictoriaMetricsHost(manifest)
	if host == "" || port == 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/prometheus", host, port)
}

// buildPrivateerStaticPeers emits the static-peers.json payload for the
// given self host: same-cluster peers plus cross-cluster Privateer seed
// peers needed for service, infrastructure, and federation reachability.
// Hosts missing a mesh IP or public key are skipped — they can't participate
// until the operator reruns `frameworks mesh wg generate`.
func buildPrivateerStaticPeers(manifest *inventory.Manifest, selfHostName string) []map[string]any {
	if manifest == nil || selfHostName == "" {
		return nil
	}
	selfCluster := manifest.HostCluster(selfHostName)
	seedPeerHosts := privateerSeedPeerHosts(manifest, selfHostName)
	var peers []map[string]any
	names := make([]string, 0, len(manifest.Hosts))
	for name := range manifest.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == selfHostName {
			continue
		}
		h := manifest.Hosts[name]
		if h.WireguardIP == "" || h.WireguardPublicKey == "" {
			continue
		}
		if selfCluster != "" && manifest.HostCluster(name) != selfCluster {
			if _, ok := seedPeerHosts[name]; !ok {
				continue
			}
		}
		port := h.WireguardPort
		if port == 0 && manifest.WireGuard != nil {
			port = manifest.WireGuard.ListenPort
		}
		if port == 0 {
			port = 51820
		}
		peer := map[string]any{
			"name":        name,
			"public_key":  h.WireguardPublicKey,
			"allowed_ips": []string{h.WireguardIP + "/32"},
		}
		if h.ExternalIP != "" {
			peer["endpoint"] = fmt.Sprintf("%s:%d", h.ExternalIP, port)
		}
		peers = append(peers, peer)
	}
	return peers
}

func privateerSeedPeerHosts(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	hosts := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return hosts
	}
	for hostName := range privateerBootstrapPeerHosts(manifest) {
		hosts[hostName] = struct{}{}
	}
	for hostName := range privateerDependencyPeerHosts(manifest, selfHostName) {
		hosts[hostName] = struct{}{}
	}
	for hostName := range privateerReciprocalPeerHosts(manifest, selfHostName) {
		hosts[hostName] = struct{}{}
	}
	return hosts
}

func privateerBootstrapPeerHosts(manifest *inventory.Manifest) map[string]struct{} {
	hosts := map[string]struct{}{}
	if manifest == nil {
		return hosts
	}
	if svc, ok := manifest.Services["quartermaster"]; ok && svc.Enabled {
		for _, hostName := range serviceHosts(svc) {
			if hostName != "" {
				hosts[hostName] = struct{}{}
			}
		}
	}
	return hosts
}

func privateerDNSAliasesForHost(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	aliases := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return aliases
	}
	aliases["quartermaster"] = struct{}{}
	for _, serviceID := range privateerLocalServiceIDs(manifest, selfHostName) {
		for _, dep := range topology.DNSServiceDependencies(serviceID) {
			if manifestServiceEnabledForDeploy(manifest, dep) {
				aliases[dep] = struct{}{}
			}
		}
	}
	return aliases
}

func privateerDependencyPeerHosts(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	hosts := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return hosts
	}
	globalAliases := privateerGlobalDNSAliasesForHost(manifest, selfHostName)
	contextClusters := privateerDNSContextClusters(manifest, selfHostName)
	for alias := range privateerDNSAliasesForHost(manifest, selfHostName) {
		var providerHosts []string
		if _, ok := globalAliases[alias]; ok {
			providerHosts = serviceProviderHostsForAliasGlobal(manifest, alias)
		} else {
			providerHosts = serviceProviderHostsForAlias(manifest, alias, contextClusters)
		}
		for _, hostName := range providerHosts {
			hosts[hostName] = struct{}{}
		}
	}
	for _, hostName := range privateerInfraPeerHostsForHost(manifest, selfHostName) {
		hosts[hostName] = struct{}{}
	}
	for _, hostName := range privateerConcretePeerHostsForHost(manifest, selfHostName) {
		hosts[hostName] = struct{}{}
	}
	return hosts
}

func privateerGlobalDNSAliasesForHost(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	aliases := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return aliases
	}
	for _, serviceID := range privateerLocalServiceIDs(manifest, selfHostName) {
		for _, dep := range topology.GlobalDNSServiceDependencies(serviceID) {
			if manifestServiceEnabledForDeploy(manifest, dep) {
				aliases[dep] = struct{}{}
			}
		}
	}
	return aliases
}

func privateerReciprocalPeerHosts(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	hosts := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return hosts
	}
	for hostName, host := range manifest.Hosts {
		if hostName == selfHostName || host.WireguardIP == "" || host.WireguardPublicKey == "" {
			continue
		}
		if _, ok := privateerDependencyPeerHosts(manifest, hostName)[selfHostName]; ok {
			hosts[hostName] = struct{}{}
		}
	}
	return hosts
}

func privateerLocalServiceIDs(manifest *inventory.Manifest, selfHostName string) []string {
	seen := map[string]struct{}{}
	addServices := func(services map[string]inventory.ServiceConfig) {
		for name, svc := range services {
			if !svc.Enabled || !slices.Contains(serviceHosts(svc), selfHostName) {
				continue
			}
			serviceID := name
			if svc.Deploy != "" {
				serviceID = svc.Deploy
			}
			if serviceID != "" {
				seen[serviceID] = struct{}{}
			}
		}
	}
	addServices(manifest.Services)
	addServices(manifest.Interfaces)
	addServices(manifest.Observability)
	return sortedKeys(seen)
}

func serviceProviderHostsForAlias(manifest *inventory.Manifest, alias string, contextClusters map[string]struct{}) []string {
	if manifest == nil || alias == "" {
		return nil
	}
	var services []inventory.ServiceConfig
	collect := func(configs map[string]inventory.ServiceConfig) {
		for name, svc := range configs {
			if serviceDeployMatches(name, svc, alias) && svc.Enabled {
				services = append(services, svc)
			}
		}
	}
	collect(manifest.Services)
	collect(manifest.Interfaces)
	collect(manifest.Observability)
	if len(services) == 0 {
		return nil
	}
	if len(services) == 1 {
		return serviceDNSHostsForContext(manifest, services[0], contextClusters)
	}
	return serviceDNSHostsForGroup(manifest, services, contextClusters)
}

func serviceProviderHostsForAliasGlobal(manifest *inventory.Manifest, alias string) []string {
	if manifest == nil || alias == "" {
		return nil
	}
	var hosts []string
	collect := func(configs map[string]inventory.ServiceConfig) {
		for name, svc := range configs {
			if serviceDeployMatches(name, svc, alias) && svc.Enabled {
				hosts = append(hosts, serviceHosts(svc)...)
			}
		}
	}
	collect(manifest.Services)
	collect(manifest.Interfaces)
	collect(manifest.Observability)
	return sortedUniqueStrings(hosts)
}

func privateerInfraPeerHostsForHost(manifest *inventory.Manifest, selfHostName string) []string {
	if manifest == nil || selfHostName == "" {
		return nil
	}
	hosts := map[string]struct{}{}
	addHost := func(hostName string) {
		if strings.TrimSpace(hostName) != "" {
			hosts[hostName] = struct{}{}
		}
	}
	forEachLocalService(manifest, selfHostName, func(name string, svc inventory.ServiceConfig) {
		serviceID := name
		if svc.Deploy != "" {
			serviceID = svc.Deploy
		}
		for _, dep := range topology.InfraDependencies(serviceID) {
			switch dep.Kind {
			case topology.InfraDatabase:
				if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
					switch dep.Provider {
					case topology.InfraProviderPrimary, "":
						if pg.IsYugabyte() && len(pg.Nodes) > 0 {
							for _, node := range pg.Nodes {
								addHost(node.Host)
							}
						} else {
							addHost(pg.Host)
						}
					case topology.InfraProviderNamed:
						for _, inst := range pg.Instances {
							if inst.Name == dep.Name {
								addHost(inst.Host)
							}
						}
					}
				}
			case topology.InfraClickHouse:
				if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
					addHost(ch.Host)
				}
			case topology.InfraKafka:
				if manifest.Infrastructure.Kafka == nil || !manifest.Infrastructure.Kafka.Enabled {
					continue
				}
				var kafkaClusters []*kafkaClusterView
				if dep.Provider == topology.InfraProviderAggregator || isAggregatorPinnedService(serviceID) {
					kafkaClusters = append(kafkaClusters, aggregatorKafkaClusterView(manifest))
				} else {
					for _, clusterID := range serviceProviderClusterIDs(manifest, svc, selfHostName) {
						kafkaClusters = append(kafkaClusters, serviceKafkaCluster(manifest, clusterID, privateerHostRegion(manifest, selfHostName)))
					}
				}
				for _, kc := range kafkaClusters {
					if kc == nil {
						continue
					}
					for _, broker := range kc.Brokers {
						addHost(broker.Host)
					}
				}
			case topology.InfraRedis:
				if redis := manifest.Infrastructure.Redis; redis != nil && redis.Enabled {
					clusterSet := map[string]struct{}{}
					for _, clusterID := range serviceProviderClusterIDs(manifest, svc, selfHostName) {
						clusterSet[clusterID] = struct{}{}
					}
					for _, inst := range redis.Instances {
						if dep.Provider == topology.InfraProviderNamed && inst.Name != dep.Name {
							continue
						}
						if inst.Cluster != "" {
							if _, ok := clusterSet[inst.Cluster]; !ok {
								continue
							}
						}
						addHost(inst.Host)
						for _, replica := range inst.ReplicaHosts {
							addHost(replica)
						}
						for _, sentinel := range inst.Sentinels {
							addHost(sentinel.Host)
						}
					}
				}
			}
		}
	})
	return sortedKeys(hosts)
}

func privateerConcretePeerHostsForHost(manifest *inventory.Manifest, selfHostName string) []string {
	if manifest == nil || selfHostName == "" {
		return nil
	}
	hosts := map[string]struct{}{}
	addServiceHosts := func(svc inventory.ServiceConfig) {
		for _, hostName := range serviceHosts(svc) {
			if hostName != "" {
				hosts[hostName] = struct{}{}
			}
		}
	}
	forEachLocalService(manifest, selfHostName, func(name string, svc inventory.ServiceConfig) {
		serviceID := name
		if svc.Deploy != "" {
			serviceID = svc.Deploy
		}
		switch serviceID {
		case "bridge":
			if signalman, ok := manifest.Services["signalman"]; ok && signalman.Enabled {
				addServiceHosts(signalman)
			}
		case "foghorn":
			for _, clusterID := range serviceProviderClusterIDs(manifest, svc, selfHostName) {
				if chandler, ok := chandlerForCluster(manifest, clusterID); ok {
					addServiceHosts(chandler)
				}
			}
			for _, peer := range servicesByDeploy(manifest, "foghorn") {
				addServiceHosts(peer)
			}
		case "privateer":
			if navigator, ok := manifest.Services["navigator"]; ok && navigator.Enabled {
				addServiceHosts(navigator)
			}
		}
		for _, peerServiceID := range topology.FederationPeerServices(serviceID) {
			for _, peer := range servicesByDeploy(manifest, peerServiceID) {
				addServiceHosts(peer)
			}
		}
	})
	return sortedKeys(hosts)
}

func forEachLocalService(manifest *inventory.Manifest, selfHostName string, fn func(name string, svc inventory.ServiceConfig)) {
	if manifest == nil || selfHostName == "" || fn == nil {
		return
	}
	walk := func(services map[string]inventory.ServiceConfig) {
		for name, svc := range services {
			if svc.Enabled && slices.Contains(serviceHosts(svc), selfHostName) {
				fn(name, svc)
			}
		}
	}
	walk(manifest.Services)
	walk(manifest.Interfaces)
	walk(manifest.Observability)
}

func privateerHostRegion(manifest *inventory.Manifest, hostName string) string {
	if manifest == nil || hostName == "" {
		return ""
	}
	host, ok := manifest.Hosts[hostName]
	if !ok {
		return ""
	}
	if region := strings.TrimSpace(host.Labels["region"]); region != "" {
		return region
	}
	if clusterID := strings.TrimSpace(host.Cluster); clusterID != "" {
		if cluster, ok := manifest.Clusters[clusterID]; ok {
			return strings.TrimSpace(cluster.Region)
		}
	}
	return ""
}

func buildPrivateerSeedDNS(manifest *inventory.Manifest, selfHostName string) map[string][]string {
	if manifest == nil || selfHostName == "" {
		return nil
	}
	selfCluster := manifest.HostCluster(selfHostName)
	contextClusters := privateerDNSContextClusters(manifest, selfHostName)
	seedPeerHosts := privateerSeedPeerHosts(manifest, selfHostName)
	dnsAliases := privateerDNSAliasesForHost(manifest, selfHostName)
	globalDNSAliases := privateerGlobalDNSAliasesForHost(manifest, selfHostName)
	dns := map[string][]string{}
	addRecord := func(recordName, hostName string) {
		if recordName == "" || hostName == "" {
			return
		}
		h, ok := manifest.Hosts[hostName]
		if !ok || h.WireguardIP == "" {
			return
		}
		if !slices.Contains(dns[recordName], h.WireguardIP) {
			dns[recordName] = append(dns[recordName], h.WireguardIP)
		}
	}
	hostNames := make([]string, 0, len(manifest.Hosts))
	for name := range manifest.Hosts {
		hostNames = append(hostNames, name)
	}
	sort.Strings(hostNames)
	for _, hostName := range hostNames {
		if selfCluster != "" && manifest.HostCluster(hostName) != selfCluster {
			if _, ok := seedPeerHosts[hostName]; !ok {
				continue
			}
		}
		addRecord(hostName, hostName)
	}
	addServices := func(services map[string]inventory.ServiceConfig) {
		names := make([]string, 0, len(services))
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)
		deployGroups := map[string][]inventory.ServiceConfig{}
		for _, name := range names {
			svc := services[name]
			if !svc.Enabled {
				continue
			}
			if svc.Deploy != "" && svc.Deploy != name {
				deployGroups[svc.Deploy] = append(deployGroups[svc.Deploy], svc)
			}
			for _, hostName := range serviceDNSHostsForContext(manifest, svc, contextClusters) {
				addRecord(hostName, hostName)
				if _, ok := dnsAliases[name]; ok {
					addRecord(name, hostName)
				}
			}
		}
		deployNames := make([]string, 0, len(deployGroups))
		for deployName := range deployGroups {
			deployNames = append(deployNames, deployName)
		}
		sort.Strings(deployNames)
		for _, deployName := range deployNames {
			for _, hostName := range serviceDNSHostsForGroup(manifest, deployGroups[deployName], contextClusters) {
				addRecord(hostName, hostName)
				if _, ok := dnsAliases[deployName]; ok {
					addRecord(deployName, hostName)
				}
			}
		}
	}
	addServices(manifest.Services)
	addServices(manifest.Interfaces)
	addServices(manifest.Observability)
	if _, ok := dnsAliases["signalman"]; ok {
		for recordName, hosts := range signalmanRegionalDNSRecords(manifest) {
			for _, hostName := range hosts {
				addRecord(recordName, hostName)
			}
		}
	}
	for alias := range globalDNSAliases {
		for _, hostName := range serviceProviderHostsForAliasGlobal(manifest, alias) {
			addRecord(hostName, hostName)
			addRecord(alias, hostName)
		}
	}
	for _, ips := range dns {
		sort.Strings(ips)
	}
	return dns
}

func privateerDNSContextClusters(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	contexts := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return contexts
	}
	if clusterID := manifest.HostCluster(selfHostName); clusterID != "" {
		contexts[clusterID] = struct{}{}
	}
	addServiceContexts := func(services map[string]inventory.ServiceConfig) {
		for _, svc := range services {
			if !svc.Enabled || !slices.Contains(serviceHosts(svc), selfHostName) {
				continue
			}
			for _, clusterID := range serviceProviderClusterIDs(manifest, svc, selfHostName) {
				contexts[clusterID] = struct{}{}
			}
		}
	}
	addServiceContexts(manifest.Services)
	addServiceContexts(manifest.Interfaces)
	addServiceContexts(manifest.Observability)
	return contexts
}

func serviceDNSHostsForContext(manifest *inventory.Manifest, svc inventory.ServiceConfig, contextClusters map[string]struct{}) []string {
	if manifest == nil {
		return nil
	}
	hosts := serviceHosts(svc)
	if len(hosts) == 0 {
		return nil
	}
	var local []string
	providerClusters := map[string]struct{}{}
	for _, hostName := range hosts {
		clusters := serviceProviderClusterIDs(manifest, svc, hostName)
		for _, clusterID := range clusters {
			providerClusters[clusterID] = struct{}{}
			if _, ok := contextClusters[clusterID]; ok {
				local = append(local, hostName)
				break
			}
		}
	}
	if len(local) > 0 {
		return sortedUniqueStrings(local)
	}
	if len(providerClusters) == 1 {
		return sortedUniqueStrings(hosts)
	}
	return nil
}

func serviceDNSHostsForGroup(manifest *inventory.Manifest, services []inventory.ServiceConfig, contextClusters map[string]struct{}) []string {
	if manifest == nil {
		return nil
	}
	var local, allHosts []string
	providerClusters := map[string]struct{}{}
	for _, svc := range services {
		if !svc.Enabled {
			continue
		}
		for _, hostName := range serviceHosts(svc) {
			allHosts = append(allHosts, hostName)
			clusters := serviceProviderClusterIDs(manifest, svc, hostName)
			for _, clusterID := range clusters {
				providerClusters[clusterID] = struct{}{}
				if _, ok := contextClusters[clusterID]; ok {
					local = append(local, hostName)
					break
				}
			}
		}
	}
	if len(local) > 0 {
		return sortedUniqueStrings(local)
	}
	if len(providerClusters) == 1 {
		return sortedUniqueStrings(allHosts)
	}
	return nil
}

func serviceProviderClusterIDs(manifest *inventory.Manifest, svc inventory.ServiceConfig, hostName string) []string {
	var out []string
	if svc.Cluster != "" {
		out = append(out, svc.Cluster)
	}
	for _, clusterID := range svc.Clusters {
		if clusterID != "" {
			out = append(out, clusterID)
		}
	}
	if len(out) == 0 && manifest != nil && hostName != "" {
		if clusterID := manifest.HostCluster(hostName); clusterID != "" {
			out = append(out, clusterID)
		}
	}
	return sortedUniqueStrings(out)
}

func sortedUniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func internalGRPCLeafServicesForHost(manifest *inventory.Manifest, hostName string) []string {
	if manifest == nil || hostName == "" {
		return nil
	}

	var services []string
	for serviceName, svc := range manifest.Services {
		if !svc.Enabled || !usesInternalGRPCLeaf(serviceName) {
			continue
		}
		if svc.Host == hostName || containsHost(svc.Hosts, hostName) {
			services = append(services, serviceName)
		}
	}

	sort.Strings(services)
	return services
}

func quartermasterHTTPURL(grpcAddr string) string {
	host, _, err := net.SplitHostPort(grpcAddr)
	if err == nil && host != "" {
		return "http://" + host + ":18002"
	}
	if idx := strings.LastIndex(grpcAddr, ":"); idx > 0 {
		return "http://" + grpcAddr[:idx] + ":18002"
	}
	return "http://quartermaster:18002"
}

var proxyRouteServiceNames = map[string]struct{}{
	"bridge":           {},
	"chandler":         {},
	"chartroom":        {},
	"chatwoot":         {},
	"foghorn":          {},
	"foredeck":         {},
	"grafana":          {},
	"logbook":          {},
	"listmonk":         {},
	"livepeer-gateway": {},
	"metabase":         {},
	"steward":          {},
	"vmauth":           {},
}

func buildExtraProxyRoutesForHost(manifest *inventory.Manifest, hostName, clusterID string) []map[string]any {
	if manifest == nil || hostName == "" || clusterID == "" {
		return nil
	}
	vmauth, ok := manifest.Observability["vmauth"]
	if !ok || !vmauth.Enabled {
		return nil
	}
	if vmauth.Host != hostName && !containsHost(vmauth.Hosts, hostName) {
		return nil
	}
	rootDomain := publicServiceRootDomain("telemetry", manifest, clusterID)
	if rootDomain == "" {
		return nil
	}
	fqdn, ok := pkgdns.ServiceFQDN("telemetry", rootDomain)
	if !ok || fqdn == "" {
		return nil
	}
	port, err := resolvePort("vmauth", vmauth)
	if err != nil || port == 0 {
		port = provisioner.ServicePorts["vmauth"]
	}
	return []map[string]any{
		{
			"name":         "telemetry",
			"server_names": []string{fqdn},
			"upstream":     fmt.Sprintf("127.0.0.1:%d", port),
		},
	}
}

func buildProxySitesForHost(manifest *inventory.Manifest, hostName, clusterID string, localSvcs map[string]any, extraRoutes any) []map[string]any {
	if manifest == nil || hostName == "" || clusterID == "" {
		return nil
	}
	var sites []map[string]any
	seen := map[string]struct{}{}
	appendSite := func(site map[string]any) {
		key := proxySiteDedupeKey(site)
		if key != "" {
			if _, ok := seen[key]; ok {
				return
			}
			seen[key] = struct{}{}
		}
		sites = append(sites, site)
	}
	serviceNames := make([]string, 0, len(localSvcs))
	for name := range localSvcs {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, name := range serviceNames {
		rawPort := localSvcs[name]
		port, ok := rawPort.(int)
		if !ok || port == 0 {
			continue
		}
		domains, bundleID := autoIngressDomains(name, manifest, clusterID)
		if len(domains) == 0 {
			continue
		}
		site := map[string]any{
			"name":     name,
			"domains":  domains,
			"upstream": fmt.Sprintf("127.0.0.1:%d", port),
			"profile":  proxyRouteProfileForService(name),
		}
		if bundleID != "" {
			site["tls_bundle_id"] = bundleID
			applyProxySiteIngressTLSDefaults(site, bundleID)
			applyProxySiteTLSBundleMetadata(site, manifest, bundleID)
		}
		appendSite(site)
	}
	for _, route := range proxyRouteSliceFromAny(extraRoutes) {
		domains := stringSliceFromAny(route["server_names"])
		if len(domains) == 0 {
			continue
		}
		upstream, ok := route["upstream"].(string)
		if !ok {
			continue
		}
		if upstream == "" {
			continue
		}
		site := map[string]any{
			"domains":  domains,
			"upstream": upstream,
		}
		if name, ok := route["name"].(string); ok && name != "" {
			site["name"] = name
			site["profile"] = proxyRouteProfileForService(name)
		}
		copyProxySiteMetadata(site, stringMapFromAny(route["metadata"]))
		appendSite(site)
	}
	siteIDs := make([]string, 0, len(manifest.IngressSites))
	for siteID := range manifest.IngressSites {
		siteIDs = append(siteIDs, siteID)
	}
	sort.Strings(siteIDs)
	for _, siteID := range siteIDs {
		cfg := manifest.IngressSites[siteID]
		if cfg.Node != hostName {
			continue
		}
		siteClusterID := clusterID
		if cfg.Cluster != "" {
			siteClusterID = cfg.Cluster
		}
		if siteClusterID != clusterID {
			continue
		}
		if len(cfg.Domains) == 0 || cfg.Upstream == "" {
			continue
		}
		site := map[string]any{
			"name":     siteID,
			"domains":  append([]string{}, cfg.Domains...),
			"upstream": cfg.Upstream,
		}
		if cfg.Kind != "" {
			site["kind"] = cfg.Kind
			site["profile"] = proxyRouteProfileForKind(cfg.Kind)
		}
		if cfg.TLSBundleID != "" {
			site["tls_bundle_id"] = cfg.TLSBundleID
			applyProxySiteIngressTLSDefaults(site, cfg.TLSBundleID)
			applyProxySiteTLSBundleMetadata(site, manifest, cfg.TLSBundleID)
		}
		copyProxySiteMetadata(site, cfg.Metadata)
		appendSite(site)
	}
	return sites
}

func proxySiteDedupeKey(site map[string]any) string {
	domains := stringSliceFromAny(site["domains"])
	if len(domains) == 0 {
		return ""
	}
	domains = append([]string{}, domains...)
	sort.Strings(domains)
	upstream, _ := stringFromAny(site["upstream"])
	paths := stringSliceFromAny(site["path_prefixes"])
	if path, ok := stringFromAny(site["path_prefix"]); ok && path != "" {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return strings.Join(domains, ",") + "|" + strings.TrimSpace(upstream) + "|" + strings.Join(paths, ",")
}

// applyProxySiteIngressTLSDefaults sets the canonical cert paths and
// tls_mode=files on a proxy site keyed by bundle id. For Privateer-managed
// (bundle-id) sites these three keys are NOT overridable from manifest
// metadata — copyProxySiteMetadata enforces that — so nginx and Privateer
// cannot disagree on where a bundle's cert and key live. Manual TLS sites
// (no tls_bundle_id) keep full metadata override.
//
// Bundle ids that fail ingress.IsValidBundleID are not safe to use as path
// components; this helper silently skips them so a poisoned manifest cannot
// drive a path that escapes ingress.TLSRoot. Registration in
// registerIngressDesiredStateWithClient also rejects them up front so they
// never reach Quartermaster.
func applyProxySiteIngressTLSDefaults(site map[string]any, bundleID string) {
	if !ingress.IsValidBundleID(bundleID) {
		return
	}
	if _, ok := site["tls_mode"]; !ok {
		site["tls_mode"] = "files"
	}
	if _, ok := site["tls_cert_path"]; !ok {
		site["tls_cert_path"] = ingress.TLSCertPath(bundleID)
	}
	if _, ok := site["tls_key_path"]; !ok {
		site["tls_key_path"] = ingress.TLSKeyPath(bundleID)
	}
}

func applyProxySiteTLSBundleMetadata(site map[string]any, manifest *inventory.Manifest, bundleID string) {
	if manifest == nil || bundleID == "" {
		return
	}
	bundle, ok := manifest.TLSBundles[bundleID]
	if !ok {
		return
	}
	copyProxySiteMetadata(site, bundle.Metadata)
}

func copyProxySiteMetadata(site map[string]any, metadata map[string]string) {
	if len(metadata) == 0 {
		return
	}
	// When a site is keyed by a Navigator-managed tls_bundle_id, the on-disk
	// paths and tls_mode are canonical (set by applyProxySiteIngressTLSDefaults)
	// and must NOT be overridable from manifest metadata. Privateer always
	// writes to ingress.TLSCertPath(bundleID) / TLSKeyPath(bundleID); letting
	// metadata steer nginx to a different path would silently desync the two
	// — nginx serving placeholders/operator-supplied certs while Privateer
	// rotates real material somewhere else. Operators wanting fully-manual
	// TLS leave tls_bundle_id empty and supply their own paths.
	bundleID, _ := stringFromAny(site["tls_bundle_id"])
	managed := strings.TrimSpace(bundleID) != ""
	overridable := []string{
		"path_prefix",
		"profile",
		"client_max_body_size",
		"client_body_timeout",
		"send_timeout",
		"proxy_connect_timeout",
		"proxy_read_timeout",
		"proxy_send_timeout",
	}
	if !managed {
		overridable = append(overridable, "tls_mode", "tls_cert_path", "tls_key_path")
	}
	for _, key := range overridable {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			site[key] = value
		}
	}
	if raw := strings.TrimSpace(metadata["path_prefixes"]); raw != "" {
		site["path_prefixes"] = splitCSVStrings(raw)
	}
	if raw := strings.TrimSpace(metadata["extra_directives"]); raw != "" {
		site["extra_directives"] = splitCSVStrings(raw)
	}
	for _, key := range []string{"proxy_request_buffering", "proxy_buffering", "websocket"} {
		if value := strings.TrimSpace(metadata[key]); value != "" {
			site[key] = parseBoolLike(value)
		}
	}
}

func proxyRouteProfileForService(serviceName string) string {
	switch serviceName {
	case "livepeer-gateway":
		return "media_ingest"
	case "chartroom", "chatwoot", "foredeck", "grafana", "listmonk", "logbook", "metabase", "steward":
		return "web_ui"
	default:
		return "api"
	}
}

func proxyRouteProfileForKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "media_ingest", "media-ingest":
		return "media_ingest"
	case "media_delivery", "media-delivery", "http_delivery", "http-delivery":
		return "media_delivery"
	case "web", "web_ui", "web-ui", "ui":
		return "web_ui"
	default:
		return "api"
	}
}

func parseBoolLike(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func splitCSVStrings(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func stringFromAny(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func proxyRouteSliceFromAny(v any) []map[string]any {
	switch typed := v.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if route, ok := item.(map[string]any); ok {
				out = append(out, route)
			}
		}
		return out
	default:
		return nil
	}
}

func stringMapFromAny(v any) map[string]string {
	switch typed := v.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, value := range typed {
			if s, ok := value.(string); ok {
				out[key] = s
			}
		}
		return out
	default:
		return nil
	}
}

func stringSliceFromAny(v any) []string {
	switch typed := v.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func addLocalProxyRoutes(routes map[string]any, hostName string, services map[string]inventory.ServiceConfig, skipName string) {
	for name, svc := range services {
		if !svc.Enabled || name == skipName {
			continue
		}
		if _, ok := proxyRouteServiceNames[name]; !ok {
			continue
		}
		if svc.Host != hostName && !containsHost(svc.Hosts, hostName) {
			continue
		}
		port, err := resolvePort(name, svc)
		if err != nil || port == 0 {
			continue
		}
		routes[name] = port
	}
}

type zookeeperNodeConfig struct {
	ServerID int
	Port     int
	Servers  []string
}

func resolveZookeeperNodeByID(instanceID string, manifest *inventory.Manifest) *zookeeperNodeConfig {
	if manifest.Infrastructure.Zookeeper == nil || instanceID == "" {
		return nil
	}

	id, err := strconv.Atoi(instanceID)
	if err != nil {
		return nil
	}

	var targetNode *inventory.ZookeeperNode
	for i := range manifest.Infrastructure.Zookeeper.Ensemble {
		node := &manifest.Infrastructure.Zookeeper.Ensemble[i]
		if node.ID == id {
			targetNode = node
			break
		}
	}
	if targetNode == nil {
		return nil
	}

	servers := []string{}
	for _, node := range manifest.Infrastructure.Zookeeper.Ensemble {
		servers = append(servers, fmt.Sprintf("server.%d=%s:2888:3888", node.ID, manifest.MeshAddress(node.Host)))
	}

	return &zookeeperNodeConfig{
		ServerID: targetNode.ID,
		Port:     targetNode.Port,
		Servers:  servers,
	}
}

func resolveRedisInstanceForTask(instanceID, clusterID, hostName string, manifest *inventory.Manifest) *inventory.RedisInstance {
	if manifest.Infrastructure.Redis == nil || instanceID == "" {
		return nil
	}
	var first *inventory.RedisInstance
	for i := range manifest.Infrastructure.Redis.Instances {
		inst := &manifest.Infrastructure.Redis.Instances[i]
		if inst.Name != instanceID {
			continue
		}
		if first == nil {
			first = inst
		}
		if clusterID != "" && inst.Cluster == clusterID {
			return inst
		}
		if clusterID == "" && hostName != "" && inst.Host == hostName {
			return inst
		}
	}
	return first
}

func resolvePostgresInstanceByID(instanceID string, manifest *inventory.Manifest) *inventory.PostgresInstance {
	if manifest == nil || manifest.Infrastructure.Postgres == nil || instanceID == "" {
		return nil
	}
	for i := range manifest.Infrastructure.Postgres.Instances {
		if manifest.Infrastructure.Postgres.Instances[i].Name == instanceID {
			return &manifest.Infrastructure.Postgres.Instances[i]
		}
	}
	return nil
}

func postgresInstancePort(inst *inventory.PostgresInstance) int {
	if inst == nil || inst.Port == 0 {
		return 5432
	}
	return inst.Port
}

func databaseConfigsToMetadata(databases []inventory.DatabaseConfig, password string) []map[string]string {
	items := make([]map[string]string, 0, len(databases))
	for _, db := range databases {
		item := map[string]string{
			"name":  db.Name,
			"owner": db.Owner,
		}
		if password != "" {
			item["password"] = password
		}
		items = append(items, item)
	}
	return items
}

func postgresInstancePassword(inst *inventory.PostgresInstance, sharedEnv map[string]string) string {
	if inst == nil {
		return ""
	}
	if inst.Password != "" {
		return inst.Password
	}
	prefix := "POSTGRES_" + envNameToken(inst.Name)
	if password := strings.TrimSpace(sharedEnv[prefix+"_PASSWORD"]); password != "" {
		return password
	}
	return strings.TrimSpace(sharedEnv["DATABASE_PASSWORD"])
}

func expandedYugabyteDatabaseConfigs(databases []inventory.DatabaseConfig, manifest *inventory.Manifest) []inventory.DatabaseConfig {
	if manifest == nil || len(databases) == 0 {
		return databases
	}
	items := make([]inventory.DatabaseConfig, 0, len(databases))
	seen := map[string]struct{}{}
	for _, db := range databases {
		expanded := clusterScopedDatabaseAliases(db, manifest)
		if len(expanded) == 0 {
			key := databaseConfigKey(db)
			if _, ok := seen[key]; !ok {
				items = append(items, db)
				seen[key] = struct{}{}
			}
			continue
		}
		for _, item := range expanded {
			key := databaseConfigKey(item)
			if _, ok := seen[key]; ok {
				continue
			}
			items = append(items, item)
			seen[key] = struct{}{}
		}
	}
	return items
}

func yugabyteSchemaDatabases(databases []inventory.DatabaseConfig, manifest *inventory.Manifest) []provisioner.SchemaDatabase {
	if manifest == nil || len(databases) == 0 {
		return schemaDatabasesFromConfigs(databases)
	}
	items := make([]provisioner.SchemaDatabase, 0, len(databases))
	seen := map[string]struct{}{}
	for _, db := range databases {
		logicalName := strings.TrimSpace(db.Name)
		expanded := clusterScopedDatabaseAliases(db, manifest)
		if len(expanded) == 0 {
			addSchemaDatabase(&items, seen, db.Name, db.Owner, "", "")
			continue
		}
		for _, item := range expanded {
			addSchemaDatabase(&items, seen, item.Name, item.Owner, logicalName, logicalName)
		}
	}
	return items
}

func schemaDatabasesFromConfigs(databases []inventory.DatabaseConfig) []provisioner.SchemaDatabase {
	items := make([]provisioner.SchemaDatabase, 0, len(databases))
	seen := map[string]struct{}{}
	for _, db := range databases {
		addSchemaDatabase(&items, seen, db.Name, db.Owner, "", "")
	}
	return items
}

func addSchemaDatabase(items *[]provisioner.SchemaDatabase, seen map[string]struct{}, name, owner, sourceName, schema string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		owner = name
	}
	key := name + "\x00" + owner + "\x00" + strings.TrimSpace(sourceName) + "\x00" + strings.TrimSpace(schema)
	if _, ok := seen[key]; ok {
		return
	}
	*items = append(*items, provisioner.SchemaDatabase{
		Name:       name,
		Owner:      owner,
		SourceName: strings.TrimSpace(sourceName),
		Schema:     strings.TrimSpace(schema),
	})
	seen[key] = struct{}{}
}

func clusterScopedDatabaseAliases(db inventory.DatabaseConfig, manifest *inventory.Manifest) []inventory.DatabaseConfig {
	logicalName := strings.TrimSpace(db.Name)
	if logicalName == "" {
		return nil
	}
	serviceIDs := make([]string, 0, len(manifest.Services))
	for serviceID, svc := range manifest.Services {
		if !svc.Enabled || strings.TrimSpace(svc.Cluster) == "" {
			continue
		}
		deploy := strings.TrimSpace(svc.Deploy)
		if deploy == "" {
			deploy = serviceID
		}
		if deploy != logicalName {
			continue
		}
		alias := strings.ReplaceAll(serviceID, "-", "_")
		if alias == logicalName {
			continue
		}
		serviceIDs = append(serviceIDs, serviceID)
	}
	sort.Strings(serviceIDs)
	items := make([]inventory.DatabaseConfig, 0, len(serviceIDs))
	for _, serviceID := range serviceIDs {
		alias := strings.ReplaceAll(serviceID, "-", "_")
		item := db
		item.Name = alias
		item.Owner = alias
		items = append(items, item)
	}
	return items
}

func databaseConfigKey(db inventory.DatabaseConfig) string {
	return strings.TrimSpace(db.Name) + "\x00" + strings.TrimSpace(db.Owner)
}

func yugabyteDatabaseConfigsToMetadata(databases []inventory.DatabaseConfig, manifest *inventory.Manifest, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, fallbackPassword string) []map[string]string {
	items := make([]map[string]string, 0, len(databases))
	for _, db := range databases {
		item := map[string]string{
			"name":  db.Name,
			"owner": db.Owner,
		}
		if password := yugabyteDatabasePassword(db, manifest, sharedEnv, clusterEnvs, fallbackPassword); password != "" {
			item["password"] = password
		}
		items = append(items, item)
	}
	return items
}

func yugabyteDatabasePassword(db inventory.DatabaseConfig, manifest *inventory.Manifest, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, fallbackPassword string) string {
	names := map[string]struct{}{}
	for _, value := range []string{db.Name, db.Owner} {
		value = strings.TrimSpace(value)
		if value != "" {
			names[value] = struct{}{}
		}
	}
	if manifest != nil {
		for serviceID, svc := range manifest.Services {
			serviceDBName := strings.ReplaceAll(serviceID, "-", "_")
			if _, ok := names[serviceDBName]; !ok {
				continue
			}
			clusterID := strings.TrimSpace(svc.Cluster)
			if clusterID == "" {
				continue
			}
			if clusterEnv := clusterEnvs[clusterID]; clusterEnv != nil {
				if password := strings.TrimSpace(clusterEnv["DATABASE_PASSWORD"]); password != "" {
					return password
				}
			}
		}
	}
	if fallbackPassword != "" {
		return fallbackPassword
	}
	return strings.TrimSpace(sharedEnv["DATABASE_PASSWORD"])
}

func stringMapToAnyMap(values map[string]string) map[string]any {
	out := make(map[string]any, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

// kafkaClusterView is the flattened shape that planner + provisioner work
// against. Both the primary KafkaConfig and each RegionalKafkaCluster project
// into one view — the rest of the kafka plumbing reads only this view and is
// unaware whether it's looking at the primary or a regional entry.
type kafkaClusterView struct {
	RegionID                             string // explicit region_id from manifest; empty falls back to inference from broker host labels
	Role                                 string // "aggregator" | "regional"; resolved (never empty after allKafkaClusters)
	ClusterID                            string
	ControllerPort                       int
	Controllers                          []inventory.KafkaController
	Brokers                              []inventory.KafkaBroker
	Topics                               []inventory.KafkaTopic
	DeleteTopicEnable                    *bool
	MinInSyncReplicas                    int
	OffsetsTopicReplicationFactor        int
	TransactionStateLogReplicationFactor int
	TransactionStateLogMinISR            int
}

// isAggregatorPinnedService reports whether the named service must always
// bind the aggregator Kafka cluster, regardless of which region its host
// happens to be in. Central writers/consumers belong here:
//
//   - periscope-ingest: writes central ClickHouse; every replica consumes the
//     same aggregator Kafka topic set in one consumer group.
//   - purser / periscope-query: central billing producer and consumer.
//   - commodore: central control-plane Kafka producer.
//
// Region-local services (decklog, signalman) and pool-assigned media services
// (foghorn, chandler, livepeer-gateway) pick Kafka via serviceKafkaCluster's
// region resolution. See docs/architecture/service-events.md.
func isAggregatorPinnedService(serviceType string) bool {
	switch serviceType {
	case "periscope-ingest", "purser", "periscope-query", "commodore":
		return true
	default:
		return false
	}
}

func serviceKafkaCluster(manifest *inventory.Manifest, mediaClusterID, taskRegion string) *kafkaClusterView {
	views := allKafkaClusters(manifest)
	if len(views) == 0 {
		return &kafkaClusterView{}
	}
	primary := views[0]
	region := strings.TrimSpace(taskRegion)
	if mediaClusterID != "" {
		if cluster, ok := manifest.Clusters[mediaClusterID]; ok && strings.TrimSpace(cluster.Region) != "" {
			region = strings.TrimSpace(cluster.Region)
		}
	}
	if region == "" {
		return &primary
	}
	for i := range views {
		if views[i].RegionID == region || (views[i].RegionID == "" && singleKafkaBrokerRegion(manifest, &views[i]) == region) {
			return &views[i]
		}
	}
	return &primary
}

// allKafkaClusters returns one kafkaClusterView per declared Kafka cluster.
// The primary KafkaConfig is always index 0 (with RegionID = ""); each
// RegionalKafkaCluster follows. Returns nil when Kafka is disabled or
// unconfigured.
func allKafkaClusters(manifest *inventory.Manifest) []kafkaClusterView {
	if manifest == nil || manifest.Infrastructure.Kafka == nil || !manifest.Infrastructure.Kafka.Enabled {
		return nil
	}
	k := manifest.Infrastructure.Kafka
	views := make([]kafkaClusterView, 0, 1+len(k.Regional))
	if k.ClusterID != "" || len(k.Brokers) > 0 || len(k.Controllers) > 0 {
		topRole := k.Role
		if topRole == "" {
			topRole = "aggregator"
		}
		views = append(views, kafkaClusterView{
			RegionID:                             k.RegionID,
			Role:                                 topRole,
			ClusterID:                            k.ClusterID,
			ControllerPort:                       k.ControllerPort,
			Controllers:                          k.Controllers,
			Brokers:                              k.Brokers,
			Topics:                               k.Topics,
			DeleteTopicEnable:                    k.DeleteTopicEnable,
			MinInSyncReplicas:                    k.MinInSyncReplicas,
			OffsetsTopicReplicationFactor:        k.OffsetsTopicReplicationFactor,
			TransactionStateLogReplicationFactor: k.TransactionStateLogReplicationFactor,
			TransactionStateLogMinISR:            k.TransactionStateLogMinISR,
		})
	}
	for _, rc := range k.Regional {
		role := rc.Role
		if role == "" {
			role = "regional"
		}
		views = append(views, kafkaClusterView{
			RegionID:                             rc.RegionID,
			Role:                                 role,
			ClusterID:                            rc.ClusterID,
			ControllerPort:                       rc.ControllerPort,
			Controllers:                          rc.Controllers,
			Brokers:                              rc.Brokers,
			Topics:                               rc.Topics,
			DeleteTopicEnable:                    rc.DeleteTopicEnable,
			MinInSyncReplicas:                    rc.MinInSyncReplicas,
			OffsetsTopicReplicationFactor:        rc.OffsetsTopicReplicationFactor,
			TransactionStateLogReplicationFactor: rc.TransactionStateLogReplicationFactor,
			TransactionStateLogMinISR:            rc.TransactionStateLogMinISR,
		})
	}
	return views
}

// findKafkaClusterView returns the view matching task.ClusterID. Primary
// cluster matches the empty-string region; regional entries match by their
// RegionID. Returns nil when no kafka is configured or the region is unknown.
func findKafkaClusterView(manifest *inventory.Manifest, regionID string) *kafkaClusterView {
	for _, v := range allKafkaClusters(manifest) {
		if v.RegionID == regionID {
			return &v
		}
	}
	return nil
}

func buildControllerQuorum(manifest *inventory.Manifest, c *kafkaClusterView) string {
	if c == nil {
		return ""
	}
	port := c.ControllerPort
	if port == 0 {
		port = 9093
	}
	voters := make([]string, 0, len(c.Brokers))
	for _, b := range c.Brokers {
		voters = append(voters, fmt.Sprintf("%d@%s:%d", b.ID, manifest.MeshAddress(b.Host), port))
	}
	return strings.Join(voters, ",")
}

func buildBootstrapServers(manifest *inventory.Manifest, c *kafkaClusterView) string {
	if c == nil {
		return ""
	}
	servers := make([]string, 0, len(c.Controllers))
	for _, ctrl := range c.Controllers {
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		servers = append(servers, fmt.Sprintf("%s:%d", manifest.MeshAddress(ctrl.Host), port))
	}
	return strings.Join(servers, ",")
}

func buildDedicatedControllerQuorum(manifest *inventory.Manifest, c *kafkaClusterView) string {
	if c == nil {
		return ""
	}
	voters := make([]string, 0, len(c.Controllers))
	for _, ctrl := range c.Controllers {
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		voters = append(voters, fmt.Sprintf("%d@%s:%d", ctrl.ID, manifest.MeshAddress(ctrl.Host), port))
	}
	return strings.Join(voters, ",")
}

func kafkaControllersToMetadata(manifest *inventory.Manifest, c *kafkaClusterView) []map[string]any {
	if c == nil {
		return nil
	}
	controllers := make([]map[string]any, 0, len(c.Controllers))
	for _, ctrl := range c.Controllers {
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		entry := map[string]any{
			"host": manifest.MeshAddress(ctrl.Host),
			"id":   ctrl.ID,
			"port": port,
		}
		if ctrl.DirID != "" {
			entry["dir_id"] = ctrl.DirID
		}
		controllers = append(controllers, entry)
	}
	return controllers
}

func aggregatorKafkaClusterView(manifest *inventory.Manifest) *kafkaClusterView {
	if manifest == nil {
		return nil
	}
	for _, v := range allKafkaClusters(manifest) {
		if v.Role == "aggregator" {
			return &v
		}
	}
	return nil
}

func isAggregatorKafkaClusterView(_ *inventory.Manifest, cluster *kafkaClusterView) bool {
	if cluster == nil {
		return false
	}
	return cluster.Role == "aggregator"
}

func kafkaClusterAlias(manifest *inventory.Manifest, cluster *kafkaClusterView) string {
	if cluster == nil {
		return ""
	}
	if cluster.RegionID != "" {
		return cluster.RegionID
	}
	if region := singleKafkaBrokerRegion(manifest, cluster); region != "" {
		return region
	}
	return "central"
}

func singleKafkaBrokerRegion(manifest *inventory.Manifest, cluster *kafkaClusterView) string {
	if manifest == nil || cluster == nil {
		return ""
	}
	var region string
	for _, broker := range cluster.Brokers {
		host, ok := manifest.GetHost(broker.Host)
		if !ok {
			return ""
		}
		hostRegion := strings.TrimSpace(host.Labels["region"])
		if hostRegion == "" {
			return ""
		}
		if region == "" {
			region = hostRegion
			continue
		}
		if region != hostRegion {
			return ""
		}
	}
	return region
}

// nonAggregatorKafkaRegionIDs returns the region_id of every regional Kafka
// cluster that is not the aggregator. These are the MM2 source clusters and
// therefore the prefixes the aggregator Periscope-Ingest subscribes to.
func nonAggregatorKafkaRegionIDs(manifest *inventory.Manifest) []string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return nil
	}
	ids := make([]string, 0, len(manifest.Infrastructure.Kafka.Regional))
	for _, rc := range manifest.Infrastructure.Kafka.Regional {
		if rc.Role == "aggregator" {
			continue
		}
		if rc.RegionID == "" {
			continue
		}
		ids = append(ids, rc.RegionID)
	}
	return ids
}

// replicasForCluster returns the replication factor that lands within
// `cluster`'s broker count. operatorDefault wins when it is non-zero AND fits;
// otherwise we use the broker count itself. MM2's source-side topics
// (offset-syncs, heartbeats) live in the source cluster, so a 1-broker US
// source cannot host RF=3 — the worker startup would fail.
func replicasForCluster(cluster *kafkaClusterView, operatorDefault int) int {
	if cluster == nil {
		if operatorDefault > 0 {
			return operatorDefault
		}
		return 1
	}
	brokerCount := len(cluster.Brokers)
	if brokerCount == 0 {
		brokerCount = 1
	}
	if operatorDefault > 0 && operatorDefault <= brokerCount {
		return operatorDefault
	}
	return brokerCount
}

// kafkaBrokersBootstrap renders a Kafka bootstrap.servers string from a cluster's
// brokers using mesh-reachable hostnames.
func kafkaBrokersBootstrap(manifest *inventory.Manifest, c *kafkaClusterView) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, len(c.Brokers))
	for _, broker := range c.Brokers {
		port := broker.Port
		if port == 0 {
			port = 9092
		}
		parts = append(parts, fmt.Sprintf("%s:%d", manifest.MeshAddress(broker.Host), port))
	}
	return strings.Join(parts, ",")
}

// mirrorTopicsForRegional returns the MM2 topics regex for a given regional
// cluster, derived from RegionalKafkaCluster.MirrorTopics. When the manifest
// list is empty, falls back to the canonical mirrored set.
func mirrorTopicsForRegional(manifest *inventory.Manifest, regionID string) string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return ""
	}
	var topics []string
	for _, rc := range manifest.Infrastructure.Kafka.Regional {
		if rc.RegionID != regionID {
			continue
		}
		topics = rc.MirrorTopics
		break
	}
	if len(topics) == 0 {
		topics = []string{"analytics_events", "service_events", "decklog_events_dlq", "billing.usage_reports"}
	}
	return strings.Join(topics, ",")
}

func kafkaBrokersToMetadata(manifest *inventory.Manifest, c *kafkaClusterView) []map[string]any {
	if c == nil {
		return nil
	}
	brokers := make([]map[string]any, 0, len(c.Brokers))
	for _, broker := range c.Brokers {
		port := broker.Port
		if port == 0 {
			port = 9092
		}
		brokers = append(brokers, map[string]any{
			"host": manifest.MeshAddress(broker.Host),
			"id":   broker.ID,
			"port": port,
		})
	}
	return brokers
}

func buildInitialControllers(manifest *inventory.Manifest, c *kafkaClusterView) string {
	if c == nil {
		return ""
	}
	parts := make([]string, 0, len(c.Controllers))
	for _, ctrl := range c.Controllers {
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		parts = append(parts, fmt.Sprintf("%d@%s:%d:%s", ctrl.ID, manifest.MeshAddress(ctrl.Host), port, ctrl.DirID))
	}
	return strings.Join(parts, ",")
}

func kafkaTopicsToMetadata(topics []inventory.KafkaTopic) []map[string]any {
	metadata := make([]map[string]any, 0, len(topics))
	for _, topic := range topics {
		metadata = append(metadata, map[string]any{
			"name":               topic.Name,
			"partitions":         topic.Partitions,
			"replication_factor": topic.ReplicationFactor,
			"config":             topic.Config,
		})
	}
	return metadata
}

// extractInfraCredentials picks database credentials out of the preloaded
// shared env for infrastructure Initialize/Configure steps.
func extractInfraCredentials(env map[string]string) map[string]any {
	result := make(map[string]any)
	if v := env["DATABASE_PASSWORD"]; v != "" {
		result["postgres_password"] = v
	}
	if v := env["CLICKHOUSE_PASSWORD"]; v != "" {
		result["clickhouse_password"] = v
	}
	if v := env["CLICKHOUSE_READONLY_PASSWORD"]; v != "" {
		result["clickhouse_readonly_password"] = v
	}
	return result
}

func startTaskProgressLogger(cmd *cobra.Command, task *orchestrator.Task, interval time.Duration) func() {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	start := time.Now()
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				fmt.Fprintf(cmd.OutOrStdout(), "    ... still provisioning %s (elapsed %s)\n", task.Name, time.Since(start).Round(time.Second))
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

const (
	provisionDetectTimeout     = 10 * time.Second
	provisionApplyTimeout      = 10 * time.Minute
	provisionValidateTimeout   = 75 * time.Second
	provisionInitializeTimeout = 2 * time.Minute
	quartermasterRPCTimeout    = 5 * time.Second
	frameworksSystemTenantID   = "00000000-0000-0000-0000-000000000001"
	nodeBaselineConcurrency    = 8
)

func runProvisionPhase(parent context.Context, timeout time.Duration, phase string, fn func(context.Context) error) error {
	phaseCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	if err := fn(phaseCtx); err != nil {
		if parent.Err() != nil {
			return fmt.Errorf("%s interrupted by parent context: %w", phase, err)
		}
		if errors.Is(phaseCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out after %s: %w", phase, timeout.Round(time.Second), err)
		}
		return err
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(phaseCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s timed out after %s", phase, timeout.Round(time.Second))
	}
	return nil
}

// rollbackProvisionedTasks stops previously provisioned services in reverse order.
// Cleanup errors are collected and reported, not swallowed.
func rollbackProvisionedTasks(ctx context.Context, cmd *cobra.Command, pool *ssh.Pool, tasks []provisionedTask) {
	if len(tasks) == 0 {
		return
	}

	rollbackTasks := make([]provisionedTask, 0, len(tasks))
	preservedMesh := 0
	for _, task := range tasks {
		if task.task.Phase == orchestrator.PhaseMesh {
			preservedMesh++
			continue
		}
		rollbackTasks = append(rollbackTasks, task)
	}
	if preservedMesh > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  Preserving %d mesh substrate service(s).\n", preservedMesh)
	}
	if len(rollbackTasks) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  No non-mesh services to roll back.")
		return
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Stopping %d previously provisioned services...\n", len(rollbackTasks))

	var rollbackFailures []string

	// Rollback in reverse order (most recent first)
	for i := len(rollbackTasks) - 1; i >= 0; i-- {
		t := rollbackTasks[i]
		fmt.Fprintf(cmd.OutOrStdout(), "    Stopping %s on %s...\n", t.task.Name, t.task.Host)

		prov, err := provisioner.GetProvisioner(t.task.Type, pool)
		if err != nil {
			msg := fmt.Sprintf("%s on %s: could not get provisioner: %v", t.task.Name, t.task.Host, err)
			rollbackFailures = append(rollbackFailures, msg)
			ux.Fail(cmd.OutOrStdout(), msg)
			continue
		}

		if err := prov.Cleanup(ctx, t.host, t.config); err != nil {
			msg := fmt.Sprintf("%s on %s: cleanup failed: %v", t.task.Name, t.task.Host, err)
			rollbackFailures = append(rollbackFailures, msg)
			ux.Fail(cmd.OutOrStdout(), msg)
		} else {
			ux.Success(cmd.OutOrStdout(), "Stopped")
		}
	}

	if len(rollbackFailures) > 0 {
		ux.Warn(cmd.OutOrStdout(), fmt.Sprintf("Rollback completed with %d failure(s):", len(rollbackFailures)))
		for _, f := range rollbackFailures {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", f)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "  Cluster is in inconsistent state. Manual cleanup may be required.")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "  Rollback complete — all services stopped.")
	}
	fmt.Fprintln(cmd.OutOrStdout(), "  Fix the issue and re-run provisioning.")
}

func captureQuartermasterDiagnostics(ctx context.Context, out io.Writer, manifest *inventory.Manifest, pool *ssh.Pool) {
	if manifest == nil || pool == nil {
		return
	}
	svc, ok := manifest.Services["quartermaster"]
	if !ok {
		return
	}
	hostName := svc.Host
	if hostName == "" && len(svc.Hosts) > 0 {
		hostName = svc.Hosts[0]
	}
	host, ok := manifest.GetHost(hostName)
	if !ok {
		fmt.Fprintf(out, "  Quartermaster diagnostics skipped: host %q not found in manifest\n", hostName)
		return
	}

	fmt.Fprintln(out, "\n  Quartermaster diagnostics before rollback:")
	base := provisioner.NewBaseProvisioner("quartermaster-diagnostics", pool)
	result, err := base.RunCommand(ctx, host, `
set +e
echo "== systemctl status frameworks-quartermaster =="
systemctl status frameworks-quartermaster --no-pager --full
echo
echo "== journalctl -u frameworks-quartermaster -n 200 =="
journalctl -u frameworks-quartermaster -n 200 --no-pager -o short-iso
echo
echo "== service bootstrap failure artifacts =="
ls -l /var/lib/frameworks/quartermaster/bootstrap-failed-*.yaml 2>/dev/null || true
echo
echo "== listeners 18002/19002 =="
ss -ltnp 2>/dev/null | awk '$4 ~ /:(18002|19002)$/ { print }'
echo
echo "== addresses =="
ip -br addr show 2>/dev/null
`)
	if err != nil {
		fmt.Fprintf(out, "    diagnostics failed: %v\n", err)
		return
	}
	text := strings.TrimSpace(result.Stdout)
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		if text != "" {
			text += "\n"
		}
		text += "stderr:\n" + stderr
	}
	if text == "" {
		text = "(no output)"
	}
	fmt.Fprintln(out, text)
}

func capturePrivateerDiagnostics(ctx context.Context, out io.Writer, host inventory.Host, pool *ssh.Pool) {
	fmt.Fprintf(out, "\n  Privateer diagnostics for %s before rollback:\n", host.Name)
	base := provisioner.NewBaseProvisioner("privateer-diagnostics", pool)
	result, err := base.RunCommand(ctx, host, `
set +e
echo "== systemctl status frameworks-privateer =="
systemctl status frameworks-privateer --no-pager --full
echo
echo "== systemctl status frameworks-privateer-resolved =="
systemctl status frameworks-privateer-resolved --no-pager --full
echo
echo "== journalctl -u frameworks-privateer -n 200 =="
journalctl -u frameworks-privateer -n 200 --no-pager -o short-iso
echo
echo "== listeners 18012/53 =="
ss -ltnup 2>/dev/null | awk '$5 ~ /:(18012|53)$/ || $4 ~ /:(18012|53)$/ { print }'
echo
echo "== wg0 =="
ip -br addr show wg0 2>/dev/null
wg show wg0 2>/dev/null
echo
echo "== privateer health =="
curl -fsS --max-time 3 http://127.0.0.1:18012/health 2>&1
echo
echo "== .internal resolver =="
resolvectl status wg0 2>/dev/null
getent hosts quartermaster.internal 2>&1
echo
echo "== privateer env keys =="
test -f /etc/privateer/privateer.env && sed 's/=.*/=<redacted>/' /etc/privateer/privateer.env
`)
	if err != nil {
		fmt.Fprintf(out, "    diagnostics failed: %v\n", err)
		return
	}
	text := strings.TrimSpace(result.Stdout)
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		if text != "" {
			text += "\n"
		}
		text += "stderr:\n" + stderr
	}
	if text == "" {
		text = "(no output)"
	}
	fmt.Fprintln(out, text)
}

// provisionTask provisions a single task
func provisionTask(ctx context.Context, task *orchestrator.Task, host inventory.Host, pool *ssh.Pool, manifest *inventory.Manifest, force, ignoreValidation bool, runtimeData map[string]any, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string, releaseRepos []string) (*taskProvisionOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Get provisioner from registry
	prov, err := provisioner.GetProvisioner(task.Type, pool)
	if err != nil {
		return nil, fmt.Errorf("failed to get provisioner: %w", err)
	}

	var beforeState *detect.ServiceState
	if phaseErr := runProvisionPhase(ctx, provisionDetectTimeout, "detect", func(phaseCtx context.Context) error {
		var detectErr error
		beforeState, detectErr = prov.Detect(phaseCtx, host)
		return detectErr
	}); phaseErr != nil {
		beforeState = nil
	}

	config, err := buildTaskConfig(task, manifest, runtimeData, force, manifestDir, sharedEnv, clusterEnvs, releaseRepos)
	if err != nil {
		return nil, err
	}

	// Infrastructure roles need shared credentials during the initial
	// Provision/Validate run as well, not only during Initialize. ClickHouse in
	// particular applies auth in its configure path and then reuses the same
	// credentials for init-time database creation.
	if task.Phase == orchestrator.PhaseInfrastructure {
		infraCreds := extractInfraCredentials(sharedEnv)
		for k, v := range infraCreds {
			if config.Metadata == nil {
				config.Metadata = make(map[string]any)
			}
			config.Metadata[k] = v
		}
	}

	// Preflight: check required external env vars
	if required := servicedefs.RequiredExternalEnv(task.Type); len(required) > 0 {
		var missing []servicedefs.RequiredEnvVar
		for _, req := range required {
			if v, ok := config.EnvVars[req.Key]; !ok || v == "" {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			ux.Fail(os.Stderr, fmt.Sprintf("%s: missing required config:", task.Name))
			for _, mk := range missing {
				fmt.Printf("      %s — %s\n", mk.Key, mk.SetupGuide)
			}
			if !ignoreValidation {
				return nil, fmt.Errorf("%s requires %d missing env var(s) — provide them in shared env files, a per-service env_file, or use --ignore-validation to deploy without starting", task.Name, len(missing))
			}
			config.DeferStart = true
			fmt.Printf("  ⏸ %s: deploying without starting (--ignore-validation)\n", task.Name)
		}
	}

	if err := runProvisionPhase(ctx, provisionApplyTimeout, "provision", func(phaseCtx context.Context) error {
		return prov.Provision(phaseCtx, host, config)
	}); err != nil {
		return nil, err
	}

	// Skip validation for deferred services
	if config.DeferStart {
		fmt.Printf("  ⏸ %s deployed but not started. Add missing config to the shared env files or service env_file, then re-run.\n", task.Name)
		return &taskProvisionOutcome{
			config:            config,
			previouslyRunning: serviceRunning(beforeState),
			deferred:          true,
		}, nil
	}

	if err := runProvisionPhase(ctx, provisionValidateTimeout, "validate", func(phaseCtx context.Context) error {
		return prov.Validate(phaseCtx, host, config)
	}); err != nil {
		if ignoreValidation {
			fmt.Printf("    Warning: validation failed (ignored due to --ignore-validation): %v\n", err)
		} else {
			return nil, fmt.Errorf("validation failed for %s: %w (use --ignore-validation to continue anyway)", task.Name, err)
		}
	}

	// Infrastructure tasks: run Initialize after Provision/Validate.
	if task.Phase == orchestrator.PhaseInfrastructure {
		if !deferInfrastructureInitialize(task.Type) {
			if initErr := runProvisionPhase(ctx, provisionInitializeTimeout, "initialize", func(phaseCtx context.Context) error {
				return prov.Initialize(phaseCtx, host, config)
			}); initErr != nil {
				return nil, fmt.Errorf("initialization failed for %s: %w", task.Name, initErr)
			}
		}
	}

	var afterState *detect.ServiceState
	if err := runProvisionPhase(ctx, provisionDetectTimeout, "detect", func(phaseCtx context.Context) error {
		var detectErr error
		afterState, detectErr = prov.Detect(phaseCtx, host)
		return detectErr
	}); err != nil {
		afterState = nil
	}

	return &taskProvisionOutcome{
		config:            config,
		previouslyRunning: serviceRunning(beforeState),
		running:           serviceRunning(afterState),
	}, nil
}

func deferInfrastructureInitialize(taskType string) bool {
	switch taskType {
	case "yugabyte", "kafka", "kafka-controller", "kafka-mirrormaker":
		return true
	default:
		return false
	}
}

// publicServiceType is shared with cli/pkg/clusterderive so the post-Ansible
// chain and the bootstrap-desired-state renderer agree on the public service
// surface.
var publicServiceType = clusterderive.PublicServiceType

func serviceRegistrationMetadata(name, hostName, clusterID string, manifest *inventory.Manifest, runtimeData map[string]any, manifestDir string, sharedEnv map[string]string, releaseRepos []string) (map[string]string, error) {
	if name != "livepeer-gateway" {
		return nil, nil
	}

	task := &orchestrator.Task{
		Name:      name,
		Type:      name,
		ServiceID: name,
		Host:      hostName,
		ClusterID: clusterID,
		Phase:     orchestrator.PhaseApplications,
	}

	config, err := buildTaskConfig(task, manifest, runtimeData, false, manifestDir, sharedEnv, nil, releaseRepos)
	if err != nil {
		return nil, err
	}

	hostInfo, ok := manifest.GetHost(hostName)
	if !ok {
		return nil, fmt.Errorf("host %q not found in manifest", hostName)
	}

	metadata := map[string]string{
		servicedefs.LivepeerGatewayMetadataPublicPort:   "443",
		servicedefs.LivepeerGatewayMetadataPublicScheme: "https",
		servicedefs.LivepeerGatewayMetadataAdminHost:    hostInfo.ExternalIP,
		servicedefs.LivepeerGatewayMetadataAdminPort:    strconv.Itoa(portFromBindAddr(config.EnvVars["cli_addr"], 7935)),
	}
	if walletAddr := firstNonEmptyEnv(config.EnvVars, "eth_acct_addr", "LIVEPEER_ETH_ACCT_ADDR"); walletAddr != "" {
		metadata[servicedefs.LivepeerGatewayMetadataWalletAddress] = walletAddr
	}

	return metadata, nil
}

// buildServiceEnvVars generates merged environment variables for a service.
// Merge order (later wins): auto-generated → shared env_files → cluster
// env_files (matched by task.ClusterID) → per-service env_file → inline config.
func buildServiceEnvVars(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]any, perServiceEnvFile string, manifestDir string, sharedEnv map[string]string, clusterEnvs map[string]map[string]string) (map[string]string, error) {
	env := make(map[string]string)

	// 1. Auto-generated infrastructure env vars
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		port := pg.EffectivePort()
		var pgHost string
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			// Use first node for DATABASE_HOST (services need a single endpoint)
			pgHost = manifestMeshHostname(manifest, pg.Nodes[0].Host)
		} else {
			pgHost = manifestMeshHostname(manifest, pg.Host)
		}
		if pgHost != "" {
			env["DATABASE_HOST"] = pgHost
			env["DATABASE_PORT"] = strconv.Itoa(port)
		}
		for _, inst := range pg.Instances {
			instHost := manifestMeshHostname(manifest, inst.Host)
			if instHost == "" {
				continue
			}
			if strings.TrimSpace(inst.Host) == strings.TrimSpace(task.Host) {
				instHost = "127.0.0.1"
			}
			instPort := postgresInstancePort(&inst)
			prefix := fmt.Sprintf("POSTGRES_%s", envNameToken(inst.Name))
			env[prefix+"_HOST"] = instHost
			env[prefix+"_PORT"] = strconv.Itoa(instPort)
			env[prefix+"_ADDR"] = fmt.Sprintf("%s:%d", instHost, instPort)
			if inst.Password != "" {
				env[prefix+"_PASSWORD"] = inst.Password
			} else if password := strings.TrimSpace(sharedEnv[prefix+"_PASSWORD"]); password != "" {
				env[prefix+"_PASSWORD"] = password
			} else if password := strings.TrimSpace(sharedEnv["DATABASE_PASSWORD"]); password != "" {
				env[prefix+"_PASSWORD"] = password
			}
		}
	}

	if kafka := manifest.Infrastructure.Kafka; kafka != nil && kafka.Enabled {
		var kc *kafkaClusterView
		if isAggregatorPinnedService(task.Type) {
			// Central-only consumers/producers (analytics ingest, billing,
			// federation control) bind aggregator Kafka regardless of where
			// the binary happens to run. Pinning here prevents a regional
			// host placement from silently routing them at the local cluster
			// (which would dual-write ClickHouse or miss billing rows).
			kc = aggregatorKafkaClusterView(manifest)
			if kc == nil {
				kc = &kafkaClusterView{}
			}
		} else {
			kc = serviceKafkaCluster(manifest, task.ClusterID, manifestTaskRegion(manifest, task))
		}
		var brokers []string
		for _, b := range kc.Brokers {
			brokerHost := manifestMeshHostname(manifest, b.Host)
			if brokerHost != "" {
				port := b.Port
				if port == 0 {
					port = 9092
				}
				brokers = append(brokers, fmt.Sprintf("%s:%d", brokerHost, port))
			}
		}
		if len(brokers) > 0 {
			env["KAFKA_BROKERS"] = strings.Join(brokers, ",")
		}
		if kc.ClusterID != "" {
			env["KAFKA_CLUSTER_ID"] = kc.ClusterID
		}
	}

	if ch := manifest.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
		if chHost := manifestMeshHostname(manifest, ch.Host); chHost != "" {
			port := ch.Port
			if port == 0 {
				port = 9000
			}
			env["CLICKHOUSE_ADDR"] = fmt.Sprintf("%s:%d", chHost, port)
			env["CLICKHOUSE_PORT"] = strconv.Itoa(port)
			env["CLICKHOUSE_DB"] = "periscope"
			env["CLICKHOUSE_USER"] = "frameworks"
			if len(ch.Databases) > 0 {
				env["CLICKHOUSE_DB"] = ch.Databases[0]
			}
		}
	}

	if redis := manifest.Infrastructure.Redis; redis != nil && redis.Enabled {
		// Per-instance Cluster scoping: an instance with Cluster set is only
		// visible under its Name to tasks whose ClusterID matches. Cluster-
		// scoped wins over cluster-empty when both exist with the same Name.
		// Without this, a single named "foghorn" instance would forcibly route
		// every regional Foghorn at one Redis (transatlantic for the wrong cell).
		selected := map[string]inventory.RedisInstance{}
		for _, inst := range redis.Instances {
			matchesCluster := inst.Cluster == "" || inst.Cluster == task.ClusterID
			if !matchesCluster {
				continue
			}
			key := strings.ToUpper(inst.Name)
			cur, ok := selected[key]
			if !ok {
				selected[key] = inst
				continue
			}
			if cur.Cluster == "" && inst.Cluster != "" {
				selected[key] = inst
			}
		}
		for _, inst := range selected {
			rHost := manifestMeshHostname(manifest, inst.Host)
			if rHost == "" {
				continue
			}
			port := inst.Port
			if port == 0 {
				port = 6379
			}
			if strings.TrimSpace(inst.Host) == strings.TrimSpace(task.Host) {
				rHost = "127.0.0.1"
				if task.ServiceID == "chatwoot" {
					if host, ok := manifest.GetHost(inst.Host); ok && strings.TrimSpace(host.WireguardIP) != "" {
						rHost = strings.TrimSpace(host.WireguardIP)
					}
				}
			}
			prefix := fmt.Sprintf("REDIS_%s", strings.ToUpper(inst.Name))
			env[prefix+"_ADDR"] = fmt.Sprintf("%s:%d", rHost, port)
			if inst.Password != "" {
				password, err := inventory.ResolveSharedEnvPlaceholder(inst.Password, sharedEnv)
				if err != nil {
					return nil, fmt.Errorf("redis %s password: %w", inst.Name, err)
				}
				env[prefix+"_PASSWORD"] = password
			} else if password := strings.TrimSpace(sharedEnv[prefix+"_PASSWORD"]); password != "" {
				env[prefix+"_PASSWORD"] = password
			}
			// Sentinel mode: surface the quorum addrs and master name so the
			// consumer dials through go-redis' Sentinel client (HA with
			// automatic failover on primary loss). Foghorn picks this up via
			// REDIS_MODE/REDIS_ADDRS/REDIS_MASTER_NAME in its config.Load.
			if strings.EqualFold(inst.Mode, "sentinel") && len(inst.Sentinels) > 0 {
				sentinelAddrs := make([]string, 0, len(inst.Sentinels))
				for _, sn := range inst.Sentinels {
					sp := sn.Port
					if sp == 0 {
						sp = 26379
					}
					sh := manifestMeshHostname(manifest, sn.Host)
					if sh == "" {
						continue
					}
					sentinelAddrs = append(sentinelAddrs, fmt.Sprintf("%s:%d", sh, sp))
				}
				if len(sentinelAddrs) > 0 {
					master := strings.TrimSpace(inst.MasterName)
					if master == "" {
						master = inst.Name
					}
					env[prefix+"_SENTINEL_ADDRS"] = strings.Join(sentinelAddrs, ",")
					env[prefix+"_MASTER_NAME"] = master
				}
			}
		}
	}

	// Backend dependencies use mesh-reachable DNS names (resolved by Privateer after mesh is up).
	// Public/external access is handled separately by service registration and edge provisioning.
	for _, grpc := range servicedefs.GRPCServices() {
		_, svc, ok := serviceConfigForDependency(manifest.Services, grpc.ServiceID, task.ClusterID, task.Host)
		if !ok || !svc.Enabled {
			continue
		}
		port := grpc.Port
		if svc.GRPCPort != 0 {
			port = svc.GRPCPort
		}
		env[grpc.EnvKey] = fmt.Sprintf("%s.internal:%d", grpc.ServiceID, port)
	}

	// Per-binary env injection. Keys off task.Type (the deploy slug) so
	// multiple manifest entries that deploy the same binary against
	// different clusters (e.g. foghorn-eu + foghorn-us both deploy foghorn)
	// share the same env wiring.
	baseName := task.Type
	if baseName == "foghorn" {
		env["FOGHORN_CONTROL_BIND_ADDR"] = fmt.Sprintf(":%d", defaultGRPCPort("foghorn"))
		if chandlerSvc, ok := chandlerForCluster(manifest, task.ClusterID); ok {
			env["CHANDLER_INTERNAL_URL"] = strings.Join(chandlerInternalURLs(manifest, chandlerSvc), ",")
		}
		// Wire Redis for HA state sync. Sentinel mode (REDIS_FOGHORN_SENTINEL_ADDRS
		// set) takes precedence: the Foghorn binary's pkgredis.Config picks up
		// REDIS_MODE/REDIS_ADDRS/REDIS_MASTER_NAME and dials through the Sentinel
		// quorum with automatic failover. Otherwise we keep REDIS_URL for the
		// single-node path.
		if sentinels := env["REDIS_FOGHORN_SENTINEL_ADDRS"]; sentinels != "" {
			env["REDIS_MODE"] = "sentinel"
			env["REDIS_ADDRS"] = sentinels
			if master := env["REDIS_FOGHORN_MASTER_NAME"]; master != "" {
				env["REDIS_MASTER_NAME"] = master
			}
			if pw := env["REDIS_FOGHORN_PASSWORD"]; pw != "" {
				env["REDIS_PASSWORD"] = pw
				env["REDIS_SENTINEL_PASSWORD"] = pw
			}
		} else if addr := env["REDIS_FOGHORN_ADDR"]; addr != "" {
			env["REDIS_URL"] = redisURLWithOptionalPassword(addr, env["REDIS_FOGHORN_PASSWORD"])
		}
		// Instance ID for HA state sync — stable across restarts
		if env["FOGHORN_INSTANCE_ID"] == "" {
			if task.Host != "" {
				env["FOGHORN_INSTANCE_ID"] = fmt.Sprintf("foghorn-%s", task.Host)
			} else {
				env["FOGHORN_INSTANCE_ID"] = "foghorn-1"
			}
		}
		// Default storage base — must match helmsman's HELMSMAN_STORAGE_LOCAL_PATH
		if env["FOGHORN_DEFAULT_STORAGE_BASE"] == "" {
			env["FOGHORN_DEFAULT_STORAGE_BASE"] = "/var/lib/mistserver/recordings"
		}
	}
	if baseName == "navigator" {
		env["NAVIGATOR_PORT"] = strconv.Itoa(defaultPort("navigator"))
		env["NAVIGATOR_GRPC_PORT"] = strconv.Itoa(defaultGRPCPort("navigator"))
	}
	if baseName == "bridge" {
		if skipper, ok := manifest.Services["skipper"]; ok && skipper.Enabled {
			port := skipper.Port
			if port == 0 {
				port = defaultPort("skipper")
			}
			env["SKIPPER_SPOKE_URL"] = fmt.Sprintf("http://skipper.internal:%d/mcp/spoke", port)
		}
		// Per-region Signalman dial: the default address remains
		// signalman.internal so local placement is owned by mesh DNS. Stream
		// origin overrides use region-scoped service aliases, not concrete
		// node names, so TLS still verifies the Signalman service identity.
		if signalmanSvc, ok := manifest.Services["signalman"]; ok && signalmanSvc.Enabled {
			if env["SIGNALMAN_GRPC_ADDR"] == "" {
				port := signalmanSvc.GRPCPort
				if port == 0 {
					port = defaultGRPCPort("signalman")
				}
				env["SIGNALMAN_GRPC_ADDR"] = fmt.Sprintf("signalman.internal:%d", port)
			}
			byRegionMulti := signalmanAddrsByRegionMulti(manifest)

			if len(byRegionMulti) > 0 {
				regions := make([]string, 0, len(byRegionMulti))
				for r := range byRegionMulti {
					regions = append(regions, r)
				}
				sort.Strings(regions)

				singlePairs := make([]string, 0, len(regions))
				multiPairs := make([]string, 0, len(regions))
				for _, r := range regions {
					addrs := byRegionMulti[r]
					if len(addrs) == 0 {
						continue
					}
					singlePairs = append(singlePairs, fmt.Sprintf("%s=%s", r, addrs[0]))
					multiPairs = append(multiPairs, fmt.Sprintf("%s=%s", r, strings.Join(addrs, ",")))
				}
				env["SIGNALMAN_GRPC_ADDR_BY_REGION"] = strings.Join(singlePairs, ",")
				env["SIGNALMAN_GRPC_ADDRS_BY_REGION"] = strings.Join(multiPairs, ";")
			}
		}
	}
	if baseName == "skipper" {
		if manifestServiceEnabledForDeploy(manifest, "bridge") {
			urls := gatewayMCPURLs(manifest, task)
			if len(urls) == 0 {
				bridge := manifest.Services["bridge"]
				port := bridge.Port
				if port == 0 {
					port = defaultPort("bridge")
				}
				urls = []string{fmt.Sprintf("http://bridge.internal:%d/mcp", port)}
			}
			env["GATEWAY_MCP_URL"] = urls[0]
			env["GATEWAY_MCP_URLS"] = strings.Join(urls, ",")
		}
	}

	// Privateer reaches Navigator over the mesh for both internal mTLS and
	// public ingress TLS bundle sync. Default the address to navigator's mesh
	// hostname so the agent can run cert sync without operators having to
	// hand-set NAVIGATOR_GRPC_ADDR in env files. An explicit override still
	// wins because shared/per-service env files merge in later.
	if baseName == "privateer" {
		if navSvc, ok := manifest.Services["navigator"]; ok && navSvc.Enabled {
			if navHost := manifestMeshHostname(manifest, navSvc.Host); navHost != "" && env["NAVIGATOR_GRPC_ADDR"] == "" {
				env["NAVIGATOR_GRPC_ADDR"] = fmt.Sprintf("%s:%d", navHost, defaultGRPCPort("navigator"))
			}
		}
	}

	// Listmonk URL — self-hosted, address from manifest
	if listmonk, ok := manifest.Services["listmonk"]; ok && listmonk.Enabled {
		lmHost := listmonk.Host
		if lmHost == "" && len(listmonk.Hosts) > 0 {
			lmHost = listmonk.Hosts[0]
		}
		if lmInternalHost := manifestMeshHostname(manifest, lmHost); lmInternalHost != "" {
			lmPort := listmonk.Port
			if lmPort == 0 {
				lmPort = 9001
			}
			env["LISTMONK_URL"] = fmt.Sprintf("http://%s:%d", lmInternalHost, lmPort)
		}
	}

	// Chatwoot host/port for deckhand — self-hosted, address from manifest
	if chatwoot, ok := manifest.Services["chatwoot"]; ok && chatwoot.Enabled {
		cwHost := chatwoot.Host
		if cwHost == "" && len(chatwoot.Hosts) > 0 {
			cwHost = chatwoot.Hosts[0]
		}
		if cwInternalHost := manifestMeshHostname(manifest, cwHost); cwInternalHost != "" {
			cwPort := chatwoot.Port
			if cwPort == 0 {
				cwPort = 18092
			}
			env["CHATWOOT_HOST"] = cwInternalHost
			env["CHATWOOT_PORT"] = strconv.Itoa(cwPort)
		}
	}

	// Periscope-Ingest is pinned to aggregator Kafka even when its process is
	// placed on a regional host. Every replica must consume the same local +
	// mirrored topic set, otherwise regional placements are not HA-capable
	// substitutes for aggregator-region workers.
	if baseName == "periscope-ingest" && env["MIRROR_REGION_PREFIXES"] == "" {
		prefixes := nonAggregatorKafkaRegionIDs(manifest)
		if len(prefixes) > 0 {
			env["MIRROR_REGION_PREFIXES"] = strings.Join(prefixes, ",")
		}
	}

	// Signalman is a broadcast consumer: N replicas per region, each receiving
	// every event. The provisioner owns the per-instance identity. Group is
	// keyed by host so a fresh replica gets a fresh group. reset=latest
	// prevents backlog replay to currently connected clients.
	if baseName == "signalman" && task.Host != "" {
		instance := "signalman-" + task.Host
		if env["KAFKA_GROUP_ID"] == "" {
			env["KAFKA_GROUP_ID"] = instance
		}
		if env["KAFKA_CLIENT_ID"] == "" {
			env["KAFKA_CLIENT_ID"] = instance
		}
		if env["KAFKA_CONSUME_RESET_OFFSET"] == "" {
			env["KAFKA_CONSUME_RESET_OFFSET"] = "latest"
		}
	}

	// Cluster and node identity
	if task.ClusterID != "" {
		env["CLUSTER_ID"] = task.ClusterID
	}
	if task.Host != "" {
		env["NODE_ID"] = task.Host
	}
	if region := manifestTaskRegion(manifest, task); region != "" && env["REGION"] == "" {
		env["REGION"] = region
	}
	if _, ok := manifest.Services["navigator"]; ok {
		env["GRPC_TLS_CA_PATH"] = "/etc/frameworks/pki/ca.crt"
	}
	if leafServiceName := internalGRPCLeafServiceName(task); leafServiceName != "" {
		env["GRPC_TLS_CERT_PATH"] = fmt.Sprintf("/etc/frameworks/pki/services/%s/tls.crt", leafServiceName)
		env["GRPC_TLS_KEY_PATH"] = fmt.Sprintf("/etc/frameworks/pki/services/%s/tls.key", leafServiceName)
	}

	// Service token
	if token, ok := runtimeData["service_token"].(string); ok && token != "" {
		env["SERVICE_TOKEN"] = token
	}

	// Enrollment token — per-cluster only
	if tokens, ok := runtimeData["enrollment_tokens"].(map[string]string); ok && task.ClusterID != "" {
		if token, ok := tokens[task.ClusterID]; ok {
			env["ENROLLMENT_TOKEN"] = token
		}
	}

	// 2. Shared env (preloaded once per provision run from manifest env_files)
	for k, v := range sharedEnv {
		env[k] = v
	}
	removeBootstrapOnlyEnv(env)

	// 3. Cluster env_files (preloaded per cluster from ClusterConfig.EnvFiles).
	// Keyed by task.ClusterID so each (service, cluster) replica picks up only
	// its own cluster's env. Cluster env wins over shared so region-specific
	// values like STORAGE_S3_* can override platform defaults.
	if task.ClusterID != "" {
		if clusterEnv, ok := clusterEnvs[task.ClusterID]; ok {
			maps.Copy(env, clusterEnv)
		}
	}

	// 4. Per-service env_file override
	if perServiceEnvFile != "" {
		if manifestDir != "" && filepath.IsAbs(perServiceEnvFile) {
			return nil, fmt.Errorf("service env_file: absolute path %q is not allowed — use a relative path from the manifest directory", perServiceEnvFile)
		}
		envPath := perServiceEnvFile
		if manifestDir != "" {
			envPath = filepath.Join(manifestDir, envPath)
		}
		if err := loadEnvFile(envPath, env); err != nil {
			return nil, fmt.Errorf("service env_file: %w", err)
		}
	}

	// 4. Inline config map from manifest service definition
	if _, svc, ok := serviceConfigForTask(manifest.Services, task); ok {
		for k, v := range svc.Config {
			env[k] = v
		}
	}
	if _, iface, ok := serviceConfigForTask(manifest.Interfaces, task); ok {
		for k, v := range iface.Config {
			env[k] = v
		}
	}
	if _, obs, ok := serviceConfigForTask(manifest.Observability, task); ok {
		for k, v := range obs.Config {
			env[k] = v
		}
	}
	if baseName == "foghorn" || baseName == "vmauth" {
		if err := ensureEdgeTelemetryJWTKeypair(runtimeData); err != nil {
			return nil, err
		}
		if env["EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64"] == "" {
			if value, ok := runtimeData["edge_telemetry_jwt_private_key_pem_b64"].(string); ok {
				env["EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64"] = value
			}
		}
		if env["EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64"] == "" {
			if value, ok := runtimeData["edge_telemetry_jwt_public_key_pem_b64"].(string); ok {
				env["EDGE_TELEMETRY_JWT_PUBLIC_KEY_PEM_B64"] = value
			}
		}
	}
	if baseName == "vmauth" && env["VMAUTH_UPSTREAM_WRITE_URL"] == "" {
		if url := defaultVictoriaMetricsWriteURL(manifest); url != "" {
			env["VMAUTH_UPSTREAM_WRITE_URL"] = url
		}
	}
	if vmObs, ok := manifest.Observability["victoriametrics"]; ok {
		if env["VM_HTTP_AUTH_USERNAME"] == "" {
			env["VM_HTTP_AUTH_USERNAME"] = vmObs.Config["VM_HTTP_AUTH_USERNAME"]
		}
		if env["VM_HTTP_AUTH_PASSWORD"] == "" {
			env["VM_HTTP_AUTH_PASSWORD"] = vmObs.Config["VM_HTTP_AUTH_PASSWORD"]
		}
	}

	// Env-injection set must match the upload set: any service that receives
	// GEOIP_MMDB_PATH here must also be a target of effectiveGeoIPServices,
	// otherwise we configure a path whose MMDB never gets uploaded. Single
	// source of truth — including for explicit manifest.GeoIP.Services
	// overrides that omit livepeer-gateway.
	if manifest.GeoIP != nil && manifest.GeoIP.Enabled {
		if slices.Contains(effectiveGeoIPServices(manifest, nil), baseName) {
			if env["GEOIP_MMDB_PATH"] == "" {
				env["GEOIP_MMDB_PATH"] = effectiveGeoIPRemotePath(manifest, "")
			}
		}
	}

	// FrameWorks gateway telemetry context. The Livepeer gateway emits per-orch
	// discovery, state, and outcome events to Decklog (SendGatewayTelemetry RPC).
	// All events stamp cluster_owner_tenant_id from gitops; per-session events
	// also carry the stream's tenant via the extended Foghorn auth response.
	// See docs/architecture/orchestrator-visibility.md.
	if baseName == "livepeer-gateway" {
		if env["FRAMEWORKS_CLUSTER_ID"] == "" && task.ClusterID != "" {
			env["FRAMEWORKS_CLUSTER_ID"] = task.ClusterID
		}
		if env["FRAMEWORKS_GATEWAY_ID"] == "" && task.Host != "" {
			env["FRAMEWORKS_GATEWAY_ID"] = task.Host
		}
		if env["FRAMEWORKS_GATEWAY_REGION"] == "" {
			if region := manifestTaskRegion(manifest, task); region != "" {
				env["FRAMEWORKS_GATEWAY_REGION"] = region
			}
		}
		// Cluster owner tenant: resolved per-alias by the bootstrap step into
		// runtimeData["owner_tenant_ids_by_alias"]. Covers platform-official
		// clusters (alias "frameworks" → system tenant UUID) AND non-platform
		// clusters (private/customer/marketplace) so their gateway telemetry
		// attributes correctly. Empty alias is treated as "frameworks".
		if env["FRAMEWORKS_CLUSTER_OWNER_TENANT_ID"] == "" && task.ClusterID != "" {
			if cluster, ok := manifest.Clusters[task.ClusterID]; ok {
				ownerAlias := strings.TrimSpace(cluster.OwnerTenant)
				if ownerAlias == "" {
					ownerAlias = "frameworks"
				}
				if ownerMap, ok := runtimeData["owner_tenant_ids_by_alias"].(map[string]string); ok {
					if id, ok := ownerMap[ownerAlias]; ok && id != "" {
						env["FRAMEWORKS_CLUSTER_OWNER_TENANT_ID"] = id
					}
				}
				// Fallback to system_tenant_id for the platform-cluster case
				// when the owner-alias resolution didn't run (e.g., during a
				// degraded provision where QM bootstrap succeeded but the
				// alias batch failed).
				if env["FRAMEWORKS_CLUSTER_OWNER_TENANT_ID"] == "" && ownerAlias == "frameworks" {
					if id, ok := runtimeData["system_tenant_id"].(string); ok && id != "" {
						env["FRAMEWORKS_CLUSTER_OWNER_TENANT_ID"] = id
					}
				}
			}
		}
		// Decklog endpoint discovery. Internal service DNS owns regional
		// placement; clients dial the service identity so TLS verifies against
		// decklog.internal instead of a concrete node name.
		if env["FRAMEWORKS_DECKLOG_GRPC_ADDR"] == "" {
			if addr, err := resolveServiceGRPCAddr(manifest, "decklog", defaultGRPCPort("decklog")); err == nil {
				env["FRAMEWORKS_DECKLOG_GRPC_ADDR"] = addr
			}
		}
		if env["FRAMEWORKS_DECKLOG_TLS_MODE"] == "" {
			if _, ok := manifest.Services["navigator"]; ok {
				env["FRAMEWORKS_DECKLOG_TLS_MODE"] = "mtls"
			} else {
				env["FRAMEWORKS_DECKLOG_TLS_MODE"] = "disabled"
			}
		}
		// Per-cluster Foghorn for the auth webhook. Without this, every
		// regional gateway resolves `foghorn.internal` to whichever Foghorn
		// Privateer happens to return, including the wrong-cell one.
		if env["auth_webhook_url"] == "" {
			if foghornSvc, ok := foghornForCluster(manifest, task.ClusterID); ok {
				if host, ok := firstServiceHost(manifest, foghornSvc); ok {
					port := foghornSvc.Port
					if port == 0 {
						port = defaultPort("foghorn")
					}
					env["auth_webhook_url"] = fmt.Sprintf("http://%s:%d/webhooks/livepeer/auth", manifestMeshHostname(manifest, host.Name), port)
				}
			}
		}
	}

	if baseName == "victoriametrics" {
		if env["VM_RETENTION_PERIOD"] == "" {
			env["VM_RETENTION_PERIOD"] = "90d"
		}
	}

	if baseName == "vmagent" {
		if env["VMAGENT_REMOTE_WRITE_URL"] == "" {
			if url := defaultVictoriaMetricsWriteURL(manifest); url != "" {
				env["VMAGENT_REMOTE_WRITE_URL"] = url
			}
		}
		if env["VMAGENT_REMOTE_WRITE_BASIC_AUTH_USERNAME"] == "" && env["VM_HTTP_AUTH_USERNAME"] != "" {
			env["VMAGENT_REMOTE_WRITE_BASIC_AUTH_USERNAME"] = env["VM_HTTP_AUTH_USERNAME"]
		}
		if env["VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD"] == "" && env["VM_HTTP_AUTH_PASSWORD"] != "" {
			env["VMAGENT_REMOTE_WRITE_BASIC_AUTH_PASSWORD"] = env["VM_HTTP_AUTH_PASSWORD"]
		}
		if env["VMAGENT_SCRAPE_INTERVAL"] == "" {
			env["VMAGENT_SCRAPE_INTERVAL"] = "30s"
		}
	}

	if baseName == "grafana" && env["VICTORIAMETRICS_URL"] == "" {
		if url := defaultVictoriaMetricsDatasourceURL(manifest); url != "" {
			env["VICTORIAMETRICS_URL"] = url
		}
	}
	if baseName != "navigator" {
		removeNavigatorInternalCAEnv(env)
	}

	applyProductionRuntimeDefaults(manifest, env)
	if err := validateProductionServiceEnv(manifest, baseName, env); err != nil {
		return nil, err
	}

	if baseName == "livepeer-gateway" || baseName == "livepeer-signer" {
		applyLivepeerRPCPool(env, livepeerServiceHostIndex(task, manifest))
	}
	normalizeServiceEnvVars(baseName, env)

	// Shared platform secrets are validated (non-dev) or generated (dev) once
	// in runProvision before tasks run — not per-task.

	// 6. Derive COOKIE_DOMAIN from manifest root_domain
	if manifest.RootDomain != "" && env["COOKIE_DOMAIN"] == "" {
		env["COOKIE_DOMAIN"] = manifest.RootDomain
	}
	if manifest.RootDomain != "" && env["BRAND_DOMAIN"] == "" {
		env["BRAND_DOMAIN"] = manifest.RootDomain
	}
	applySharedPostgresDatabaseDefaults(baseName, env)
	if env["DATABASE_USER"] == "" {
		env["DATABASE_USER"] = strings.ReplaceAll(task.ServiceID, "-", "_")
	}
	applyDeclaredPostgresDatabaseDefaults(task, manifest, env)

	// Construct DATABASE_URL from merged credentials (operator may have set
	// DATABASE_USER / DATABASE_PASSWORD in their env_file).
	// Skip if operator explicitly provided DATABASE_URL.
	if env["DATABASE_HOST"] != "" && env["DATABASE_URL"] == "" {
		dbUser := env["DATABASE_USER"]
		dbPass := env["DATABASE_PASSWORD"]
		dbHost := env["DATABASE_HOST"]
		dbPort := env["DATABASE_PORT"]
		if dbPort == "" {
			dbPort = "5432"
		}
		var userInfo string
		if dbPass != "" {
			userInfo = url.UserPassword(dbUser, dbPass).String()
		} else {
			userInfo = url.User(dbUser).String()
		}
		dbName := strings.ReplaceAll(task.ServiceID, "-", "_")
		if env["DATABASE_NAME"] != "" {
			dbName = env["DATABASE_NAME"]
		}
		env["DATABASE_URL"] = fmt.Sprintf("postgres://%s@%s/%s?sslmode=disable", userInfo, net.JoinHostPort(dbHost, dbPort), dbName)
	}

	return env, nil
}

func applySharedPostgresDatabaseDefaults(serviceID string, env map[string]string) {
	switch serviceID {
	case "periscope-query", "periscope-ingest":
		if env["DATABASE_USER"] == "" {
			env["DATABASE_USER"] = "periscope"
		}
		if env["DATABASE_NAME"] == "" {
			env["DATABASE_NAME"] = "periscope"
		}
	}
}

func applyDeclaredPostgresDatabaseDefaults(task *orchestrator.Task, manifest *inventory.Manifest, env map[string]string) {
	if task == nil || manifest == nil || manifest.Infrastructure.Postgres == nil {
		return
	}
	if strings.TrimSpace(env["DATABASE_URL"]) != "" {
		return
	}
	inst, db, ok := declaredPostgresDatabaseForService(task, manifest, env)
	if !ok {
		return
	}

	prefix := "POSTGRES_" + envNameToken(inst.Name)
	if host := strings.TrimSpace(env[prefix+"_HOST"]); host != "" {
		env["DATABASE_HOST"] = host
	} else if host := manifestMeshHostname(manifest, inst.Host); host != "" {
		if strings.TrimSpace(inst.Host) == strings.TrimSpace(task.Host) {
			host = "127.0.0.1"
		}
		env["DATABASE_HOST"] = host
	}
	if port := strings.TrimSpace(env[prefix+"_PORT"]); port != "" {
		env["DATABASE_PORT"] = port
	} else {
		env["DATABASE_PORT"] = strconv.Itoa(postgresInstancePort(inst))
	}
	if db.Name != "" {
		env["DATABASE_NAME"] = db.Name
	}
	owner := db.Owner
	if owner == "" {
		owner = db.Name
	}
	if owner != "" {
		env["DATABASE_USER"] = owner
	}
	if password := strings.TrimSpace(env[prefix+"_PASSWORD"]); password != "" {
		env["DATABASE_PASSWORD"] = password
	} else if password := strings.TrimSpace(inst.Password); password != "" {
		env["DATABASE_PASSWORD"] = password
	}
}

func declaredPostgresDatabaseForService(task *orchestrator.Task, manifest *inventory.Manifest, env map[string]string) (*inventory.PostgresInstance, inventory.DatabaseConfig, bool) {
	pg := manifest.Infrastructure.Postgres
	if pg == nil {
		return nil, inventory.DatabaseConfig{}, false
	}
	serviceDBName := strings.ReplaceAll(task.ServiceID, "-", "_")
	targetNames := map[string]struct{}{}
	for _, value := range []string{serviceDBName, env["DATABASE_NAME"], env["DATABASE_USER"]} {
		value = strings.TrimSpace(value)
		if value != "" {
			targetNames[value] = struct{}{}
		}
	}
	for i := range pg.Instances {
		inst := &pg.Instances[i]
		for _, db := range inst.Databases {
			owner := db.Owner
			if owner == "" {
				owner = db.Name
			}
			if _, ok := targetNames[db.Name]; ok {
				return inst, db, true
			}
			if _, ok := targetNames[owner]; ok {
				return inst, db, true
			}
		}
	}
	return nil, inventory.DatabaseConfig{}, false
}

func isDevProfile(manifest *inventory.Manifest) bool {
	if manifest == nil {
		return false
	}
	p := strings.ToLower(strings.TrimSpace(manifest.Profile))
	return p == "dev" || p == "development"
}

func applyProductionRuntimeDefaults(manifest *inventory.Manifest, env map[string]string) {
	if isDevProfile(manifest) {
		return
	}

	env["BUILD_ENV"] = "production"
	if strings.TrimSpace(env["GIN_MODE"]) == "" || strings.EqualFold(strings.TrimSpace(env["GIN_MODE"]), "debug") {
		env["GIN_MODE"] = "release"
	}

	env["GRPC_ALLOW_INSECURE"] = "false"
}

func validateProductionServiceEnv(manifest *inventory.Manifest, serviceID string, env map[string]string) error {
	if isDevProfile(manifest) {
		return nil
	}

	switch serviceID {
	case "navigator":
		return validateNavigatorProductionEnv(env)
	case "quartermaster", "commodore", "purser":
		if strings.TrimSpace(env["DATABASE_HOST"]) == "" {
			return fmt.Errorf("service %s: non-dev deploy requires DATABASE_HOST", serviceID)
		}
		if strings.TrimSpace(env["DATABASE_PASSWORD"]) == "" && strings.TrimSpace(env["DATABASE_URL"]) == "" {
			return fmt.Errorf("service %s: non-dev deploy requires DATABASE_PASSWORD (or DATABASE_URL with embedded credentials)", serviceID)
		}
	}

	return nil
}

func validateNavigatorProductionEnv(env map[string]string) error {
	fileKeys := []string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE",
	}
	b64Keys := []string{
		"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64",
		"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64",
	}

	allFileKeysPresent := true
	for _, key := range fileKeys {
		if strings.TrimSpace(env[key]) == "" {
			allFileKeysPresent = false
			break
		}
	}
	if allFileKeysPresent {
		return nil
	}

	allB64KeysPresent := true
	for _, key := range b64Keys {
		if strings.TrimSpace(env[key]) == "" {
			allB64KeysPresent = false
			break
		}
	}
	if allB64KeysPresent {
		return nil
	}

	return fmt.Errorf(
		"service navigator: non-dev deploy requires managed internal CA env vars via either files (%s) or base64 PEM envs (%s)",
		strings.Join(fileKeys, ", "),
		strings.Join(b64Keys, ", "),
	)
}

func normalizeServiceEnvVars(serviceID string, env map[string]string) {
	switch serviceID {
	case "livepeer-gateway":
		normalizeLivepeerEnvVars(env)
		applyLivepeerGatewayRuntimeDefaults(env)
		setEnvIfEmpty(env, "auth_webhook_url", "LIVEPEER_AUTH_WEBHOOK_URL")
		if strings.TrimSpace(env["auth_webhook_url"]) == "" {
			env["auth_webhook_url"] = defaultLivepeerGatewayAuthWebhookURL
		}
	case "livepeer-signer":
		normalizeLivepeerEnvVars(env)
	}
}

func missingRequiredGeneratedEnv(manifest *inventory.Manifest, serviceID string, env map[string]string) []string {
	if isDevProfile(manifest) {
		return nil
	}
	requirements := topology.RequiredServiceEnv(serviceID)
	if len(requirements) == 0 {
		return nil
	}
	missing := make([]string, 0, len(requirements))
	for _, req := range requirements {
		if req.TargetServiceID != "" && !manifestServiceEnabledForDeploy(manifest, req.TargetServiceID) {
			continue
		}
		if strings.TrimSpace(env[req.EnvKey]) == "" {
			missing = append(missing, req.EnvKey)
		}
	}
	return missing
}

func manifestServiceEnabledForDeploy(manifest *inventory.Manifest, deploy string) bool {
	for _, svc := range servicesByDeploy(manifest, deploy) {
		if svc.Enabled {
			return true
		}
	}
	return false
}

func applyLivepeerGatewayRuntimeDefaults(env map[string]string) {
	defaults := map[string]string{
		"network":                "arbitrum-one-mainnet",
		"http_addr":              "0.0.0.0:8935",
		"http_ingest":            "true",
		"cli_addr":               ":7935",
		"rtmp_addr":              "",
		"max_sessions":           "500",
		"max_price_per_unit":     "1200",
		"pixels_per_unit":        "1",
		"max_ticket_ev":          "3000000000000",
		"deposit_multiplier":     "1",
		"block_polling_interval": "20",
	}
	for key, value := range defaults {
		if _, ok := env[key]; !ok {
			env[key] = value
		}
	}
}

func applyLivepeerRPCPool(env map[string]string, index int) {
	if strings.TrimSpace(env["eth_url"]) != "" || strings.TrimSpace(env["LIVEPEER_ETH_URL"]) != "" {
		return
	}
	for _, key := range livepeerRPCPoolEnvKeys(env) {
		urls := splitLivepeerRPCURLs(env[key])
		if len(urls) == 0 {
			continue
		}
		if index < 0 {
			index = 0
		}
		env["eth_url"] = urls[index%len(urls)]
		return
	}
}

func livepeerRPCPoolEnvKeys(env map[string]string) []string {
	keys := []string{"eth_urls", "LIVEPEER_ETH_URLS"}
	switch strings.ToLower(strings.TrimSpace(env["network"])) {
	case "", "arbitrum", "arbitrum-mainnet", "arbitrum-one-mainnet":
		return append(keys, "ARBITRUM_RPC_ENDPOINTS")
	case "arbitrum-sepolia":
		return append(keys, "ARBITRUM_SEPOLIA_RPC_ENDPOINTS")
	case "base", "base-mainnet":
		return append(keys, "BASE_RPC_ENDPOINTS")
	case "base-sepolia":
		return append(keys, "BASE_SEPOLIA_RPC_ENDPOINTS")
	case "ethereum", "ethereum-mainnet", "mainnet":
		return append(keys, "ETH_RPC_ENDPOINTS")
	default:
		return keys
	}
}

func splitLivepeerRPCURLs(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func livepeerServiceHostIndex(task *orchestrator.Task, manifest *inventory.Manifest) int {
	if task == nil || manifest == nil {
		return 0
	}
	svc, ok := manifest.Services[task.ServiceID]
	if !ok {
		return 0
	}
	for i, host := range serviceHosts(svc) {
		if host == task.Host {
			return i
		}
	}
	return 0
}

func removeNavigatorInternalCAEnv(env map[string]string) {
	for key := range env {
		if strings.HasPrefix(key, "NAVIGATOR_INTERNAL_CA_") {
			delete(env, key)
		}
	}
}

func removeBootstrapOnlyEnv(env map[string]string) {
	for _, key := range []string{
		"PLATFORM_ADMIN_PASSWORD",
		"BOOTSTRAP_ADMIN_PASSWORD",
		"ADMIN_PASSWORD",
	} {
		delete(env, key)
	}
}

func manifestTaskRegion(manifest *inventory.Manifest, task *orchestrator.Task) string {
	if manifest == nil || task == nil {
		return ""
	}
	if task.Host != "" {
		if region := manifestHostRegion(manifest, task.Host); region != "" {
			return region
		}
	}
	if task.ClusterID != "" {
		if cluster, ok := manifest.Clusters[task.ClusterID]; ok {
			return strings.TrimSpace(cluster.Region)
		}
	}
	return ""
}

func manifestHostRegion(manifest *inventory.Manifest, hostName string) string {
	if manifest == nil || hostName == "" {
		return ""
	}
	host, ok := manifest.Hosts[hostName]
	if !ok {
		return ""
	}
	if region := strings.TrimSpace(host.Labels["region"]); region != "" {
		return region
	}
	clusterID := strings.TrimSpace(host.Cluster)
	if clusterID == "" {
		return ""
	}
	if cluster, ok := manifest.Clusters[clusterID]; ok {
		return strings.TrimSpace(cluster.Region)
	}
	return ""
}

// signalmanAddrsByRegionMulti builds region→service-alias addrs. The alias
// resolves through Privateer DNS to all Signalman replicas in that region, so
// callers keep a service-identity target instead of concrete node names.
func signalmanAddrsByRegionMulti(manifest *inventory.Manifest) map[string][]string {
	if manifest == nil {
		return nil
	}
	svc, ok := manifest.Services["signalman"]
	if !ok || !svc.Enabled {
		return nil
	}
	port := svc.GRPCPort
	if port == 0 {
		port = defaultGRPCPort("signalman")
	}
	out := map[string][]string{}
	for recordName := range signalmanRegionalDNSRecords(manifest) {
		region := strings.TrimPrefix(recordName, "signalman.")
		if region == recordName || region == "" {
			continue
		}
		out[region] = []string{fmt.Sprintf("%s.internal:%d", recordName, port)}
	}
	return out
}

func signalmanRegionalDNSRecords(manifest *inventory.Manifest) map[string][]string {
	if manifest == nil {
		return nil
	}
	svc, ok := manifest.Services["signalman"]
	if !ok || !svc.Enabled {
		return nil
	}
	out := map[string][]string{}
	for _, hostName := range serviceHosts(svc) {
		region := pkgdns.SanitizeLabel(privateerHostRegion(manifest, hostName))
		if region == "" {
			continue
		}
		recordName := "signalman." + region
		out[recordName] = append(out[recordName], hostName)
	}
	for recordName := range out {
		sort.Strings(out[recordName])
	}
	return out
}

const defaultLivepeerGatewayAuthWebhookURL = "http://foghorn.internal:18008/webhooks/livepeer/auth"

// servicesByDeploy returns every Services entry whose Deploy slug matches,
// including entries that omit Deploy and rely on the manifest key being the
// canonical slug (foghorn, chandler, ...).
func servicesByDeploy(manifest *inventory.Manifest, slug string) []inventory.ServiceConfig {
	if manifest == nil {
		return nil
	}
	out := []inventory.ServiceConfig{}
	for name, svc := range manifest.Services {
		if svc.Deploy == slug || (svc.Deploy == "" && name == slug) {
			out = append(out, svc)
		}
	}
	return out
}

func serviceConfigForTask(configs map[string]inventory.ServiceConfig, task *orchestrator.Task) (string, inventory.ServiceConfig, bool) {
	if task == nil {
		return "", inventory.ServiceConfig{}, false
	}
	if svc, ok := configs[task.ServiceID]; ok {
		return task.ServiceID, svc, true
	}
	return serviceConfigForDeploy(configs, task.Type, task.ClusterID, task.Host)
}

func serviceConfigForDeploy(configs map[string]inventory.ServiceConfig, deploy, clusterID, hostName string) (string, inventory.ServiceConfig, bool) {
	bestName := ""
	bestScore := -1
	var best inventory.ServiceConfig
	for name, svc := range configs {
		if !serviceDeployMatches(name, svc, deploy) {
			continue
		}
		score, ok := servicePlacementScore(svc, clusterID, hostName)
		if !ok {
			continue
		}
		if score > bestScore || (score == bestScore && (bestName == "" || name < bestName)) {
			bestName = name
			best = svc
			bestScore = score
		}
	}
	if bestScore < 0 {
		return "", inventory.ServiceConfig{}, false
	}
	return bestName, best, true
}

func serviceConfigForDependency(configs map[string]inventory.ServiceConfig, deploy, clusterID, hostName string) (string, inventory.ServiceConfig, bool) {
	if name, svc, ok := serviceConfigForDeploy(configs, deploy, clusterID, hostName); ok {
		if svc.Enabled {
			return name, svc, true
		}
	}

	bestName := ""
	bestScore := -1
	var best inventory.ServiceConfig
	for name, svc := range configs {
		if !serviceDeployMatches(name, svc, deploy) || !svc.Enabled {
			continue
		}
		score := 0
		if svc.Cluster == "" && len(svc.Clusters) == 0 {
			score += 20
		}
		if len(serviceHosts(svc)) == 0 {
			score += 10
		}
		if score > bestScore || (score == bestScore && (bestName == "" || name < bestName)) {
			bestName = name
			best = svc
			bestScore = score
		}
	}
	if bestScore < 0 {
		return "", inventory.ServiceConfig{}, false
	}
	return bestName, best, true
}

func serviceDeployMatches(name string, svc inventory.ServiceConfig, deploy string) bool {
	return svc.Deploy == deploy || (svc.Deploy == "" && name == deploy)
}

func servicePlacementScore(svc inventory.ServiceConfig, clusterID, hostName string) (int, bool) {
	score := 0
	if clusterID != "" {
		if svc.Cluster == clusterID {
			score += 20
		} else if slices.Contains(svc.Clusters, clusterID) {
			score += 19
		} else if svc.Cluster != "" || len(svc.Clusters) > 0 {
			return 0, false
		}
	}
	hosts := serviceHosts(svc)
	if hostName != "" && len(hosts) > 0 {
		if slices.Contains(hosts, hostName) {
			score += 10
		} else {
			return 0, false
		}
	}
	return score, true
}

// foghornForCluster returns the Foghorn service entry assigned to the given
// media cluster, or false when none matches. With M:N service split each
// regional Foghorn (foghorn-eu / foghorn-us) deploys the same binary against
// a different cluster, so the lookup keys on Cluster/Clusters.
func foghornForCluster(manifest *inventory.Manifest, clusterID string) (inventory.ServiceConfig, bool) {
	if manifest == nil {
		return inventory.ServiceConfig{}, false
	}
	_, svc, ok := serviceConfigForDeploy(manifest.Services, "foghorn", clusterID, "")
	if !ok || !svc.Enabled {
		return inventory.ServiceConfig{}, false
	}
	return svc, true
}

// chandlerForCluster returns the Chandler service entry assigned to the
// given media cluster, or false when none matches.
func chandlerForCluster(manifest *inventory.Manifest, clusterID string) (inventory.ServiceConfig, bool) {
	if manifest == nil {
		return inventory.ServiceConfig{}, false
	}
	_, svc, ok := serviceConfigForDeploy(manifest.Services, "chandler", clusterID, "")
	if !ok || !svc.Enabled {
		return inventory.ServiceConfig{}, false
	}
	return svc, true
}

func anyClusterRunsLivepeerGateway(manifest *inventory.Manifest) bool {
	return len(servicesByDeploy(manifest, "livepeer-gateway")) > 0
}

func normalizeLivepeerEnvVars(env map[string]string) {
	setEnvIfEmpty(env, "eth_url", livepeerRPCEnvKeys(env)...)
	setEnvIfEmpty(env, "eth_acct_addr", "LIVEPEER_ETH_ACCT_ADDR")
	setEnvIfEmpty(env, "orch_webhook_url", "LIVEPEER_ORCH_WEBHOOK_URL")
	setEnvIfEmpty(env, "remote_signer_url", "LIVEPEER_REMOTE_SIGNER_URL")
	setEnvIfEmpty(env, "auth_webhook_url", "LIVEPEER_AUTH_WEBHOOK_URL")
}

func validateGatewayMeshCoverage(manifest *inventory.Manifest) error {
	gateways := servicesByDeploy(manifest, "livepeer-gateway")
	if len(gateways) == 0 {
		return nil
	}

	privateerSvc, ok := manifest.Services["privateer"]
	if !ok || !privateerSvc.Enabled {
		return fmt.Errorf("livepeer-gateway requires privateer to resolve foghorn.internal")
	}

	privateerHosts := make(map[string]struct{})
	for _, hostName := range orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts) {
		privateerHosts[hostName] = struct{}{}
	}

	for _, gatewaySvc := range gateways {
		if !gatewaySvc.Enabled {
			continue
		}
		var gatewayHosts []string
		if len(gatewaySvc.Hosts) > 0 {
			gatewayHosts = gatewaySvc.Hosts
		} else if gatewaySvc.Host != "" {
			gatewayHosts = []string{gatewaySvc.Host}
		}
		for _, hostName := range gatewayHosts {
			if _, ok := privateerHosts[hostName]; !ok {
				return fmt.Errorf("livepeer-gateway host %q is not covered by privateer; gateway auth webhook uses foghorn.internal", hostName)
			}
		}
	}

	return nil
}

func validateInternalGRPCTLSCoverage(manifest *inventory.Manifest) error {
	internalHosts := make(map[string][]string)
	for serviceName, svc := range manifest.Services {
		if !svc.Enabled || !usesInternalGRPCLeaf(serviceName) {
			continue
		}
		for _, hostName := range serviceHosts(svc) {
			internalHosts[hostName] = append(internalHosts[hostName], serviceName)
		}
	}
	if len(internalHosts) == 0 {
		return nil
	}

	navigatorSvc, ok := manifest.Services["navigator"]
	if !ok || !navigatorSvc.Enabled {
		return fmt.Errorf("internal gRPC TLS requires navigator to issue CA bundles and service leaf certificates")
	}

	privateerSvc, ok := manifest.Services["privateer"]
	if !ok || !privateerSvc.Enabled {
		return fmt.Errorf("internal gRPC TLS requires privateer so nodes receive /etc/frameworks/pki materials")
	}

	privateerHosts := make(map[string]struct{})
	for _, hostName := range orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts) {
		privateerHosts[hostName] = struct{}{}
	}

	for hostName, services := range internalHosts {
		if _, ok := privateerHosts[hostName]; ok {
			continue
		}
		sort.Strings(services)
		return fmt.Errorf("host %q runs internal gRPC services %s but is not covered by privateer", hostName, strings.Join(services, ", "))
	}

	return nil
}

func serviceHosts(svc inventory.ServiceConfig) []string {
	if len(svc.Hosts) > 0 {
		return svc.Hosts
	}
	if svc.Host != "" {
		return []string{svc.Host}
	}
	return nil
}

func chandlerInternalURLs(manifest *inventory.Manifest, svc inventory.ServiceConfig) []string {
	port := svc.Port
	if port == 0 {
		port = defaultPort("chandler")
	}

	hosts := serviceHosts(svc)
	if len(hosts) == 0 {
		return []string{fmt.Sprintf("http://chandler.internal:%d", port)}
	}

	urls := make([]string, 0, len(hosts))
	seen := make(map[string]bool, len(hosts))
	for _, hostName := range hosts {
		meshHost := manifestMeshHostname(manifest, hostName)
		if meshHost == "" {
			continue
		}
		url := fmt.Sprintf("http://%s:%d", meshHost, port)
		if seen[url] {
			continue
		}
		seen[url] = true
		urls = append(urls, url)
	}
	if len(urls) == 0 {
		return []string{fmt.Sprintf("http://chandler.internal:%d", port)}
	}
	return urls
}

func gatewayMCPURLs(manifest *inventory.Manifest, task *orchestrator.Task) []string {
	if manifest == nil {
		return nil
	}
	type candidate struct {
		url    string
		region string
	}
	var candidates []candidate
	for _, svc := range servicesByDeploy(manifest, "bridge") {
		if !svc.Enabled {
			continue
		}
		port := svc.Port
		if port == 0 {
			port = defaultPort("bridge")
		}
		for _, hostName := range serviceHosts(svc) {
			if hostName != "" {
				candidates = append(candidates, candidate{
					url:    fmt.Sprintf("http://%s.internal:%d/mcp", hostName, port),
					region: manifestHostRegion(manifest, hostName),
				})
			}
		}
	}
	preferredRegion := manifestTaskRegion(manifest, task)
	sort.SliceStable(candidates, func(i, j int) bool {
		iPreferred := preferredRegion != "" && candidates[i].region == preferredRegion
		jPreferred := preferredRegion != "" && candidates[j].region == preferredRegion
		if iPreferred != jPreferred {
			return iPreferred
		}
		return candidates[i].url < candidates[j].url
	})
	urls := make([]string, 0, len(candidates))
	for _, c := range candidates {
		urls = append(urls, c.url)
	}
	return sortedUniqueStringsPreservingOrder(urls)
}

func sortedUniqueStringsPreservingOrder(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func portFromBindAddr(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	if strings.HasPrefix(raw, ":") {
		if port, err := strconv.Atoi(strings.TrimPrefix(raw, ":")); err == nil && port > 0 {
			return port
		}
		return fallback
	}
	if _, portStr, err := net.SplitHostPort(raw); err == nil {
		if port, convErr := strconv.Atoi(portStr); convErr == nil && port > 0 {
			return port
		}
	}
	if port, err := strconv.Atoi(raw); err == nil && port > 0 {
		return port
	}
	return fallback
}

func livepeerRPCEnvKeys(env map[string]string) []string {
	keys := []string{"LIVEPEER_ETH_URL"}

	switch strings.ToLower(strings.TrimSpace(env["network"])) {
	case "", "arbitrum", "arbitrum-mainnet", "arbitrum-one-mainnet":
		return append(keys, "ARBITRUM_RPC_ENDPOINT")
	case "arbitrum-sepolia":
		return append(keys, "ARBITRUM_SEPOLIA_RPC_ENDPOINT")
	case "base", "base-mainnet":
		return append(keys, "BASE_RPC_ENDPOINT")
	case "base-sepolia":
		return append(keys, "BASE_SEPOLIA_RPC_ENDPOINT")
	case "ethereum", "ethereum-mainnet", "mainnet":
		return append(keys, "ETH_RPC_ENDPOINT")
	default:
		return keys
	}
}

func setEnvIfEmpty(env map[string]string, target string, sourceKeys ...string) {
	if strings.TrimSpace(env[target]) != "" {
		return
	}

	for _, key := range sourceKeys {
		if v := strings.TrimSpace(env[key]); v != "" {
			env[target] = v
			return
		}
	}
}

func firstNonEmptyEnv(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(env[key]); v != "" {
			return v
		}
	}
	return ""
}

func envNameToken(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return strings.Trim(b.String(), "_")
}

// loadEnvFile reads a KEY=VALUE env file and merges values into the target map.
// Lines starting with # and empty lines are skipped. Later values overwrite earlier ones.
// SOPS-encrypted files are decrypted transparently using age keys.
func loadEnvFile(path string, target map[string]string) error {
	data, err := fwsops.DecryptFileIfEncrypted(path, "")
	if err != nil {
		return fmt.Errorf("env file %s: %w", path, err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k := strings.TrimSpace(key)
		v := strings.TrimSpace(value)
		if prev, exists := target[k]; exists && prev != v {
			fmt.Printf("    env override: %s changed by %s\n", k, filepath.Base(path))
		}
		target[k] = v
	}
	return nil
}

// verifyMeshHealth checks that Privateer is running and the rendered mesh
// policy was actually applied on privateer hosts.
// Called as a gate between Privateer provisioning and application service provisioning.
func verifyMeshHealth(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool, privateerHosts []string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "  Verifying mesh health on %d privateer host(s)...\n", len(privateerHosts))

	base := provisioner.NewBaseProvisioner("mesh-verify", pool)
	var failures []string
	meshIPs := make(map[string]string, len(privateerHosts))
	for _, hostName := range privateerHosts {
		if ip := manifest.MeshAddress(hostName); net.ParseIP(ip) != nil {
			meshIPs[hostName] = ip
		}
	}

	for _, hostName := range privateerHosts {
		hostInfo, ok := manifest.Hosts[hostName]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: not found in manifest", hostName))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "    Checking %s (%s)...\n", hostName, hostInfo.ExternalIP)

		// Check Privateer is running
		result, err := base.RunCommand(ctx, hostInfo, "systemctl is-active frameworks-privateer 2>/dev/null || echo inactive")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: SSH failed: %v", hostName, err))
			continue
		}
		svcStatus := strings.TrimSpace(result.Stdout)
		if svcStatus != "active" {
			failures = append(failures, fmt.Sprintf("%s: privateer is %s", hostName, svcStatus))
			continue
		}

		// Check normal host resolution, not only Privateer's DNS listener.
		result, err = base.RunCommand(ctx, hostInfo, "command -v getent >/dev/null 2>&1 || { echo 'MISSING_GETENT'; exit 0; }; getent hosts quartermaster.internal 2>/dev/null | awk '{print $1}' | head -n1")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: DNS check failed: %v", hostName, err))
			continue
		}
		resolved := strings.TrimSpace(result.Stdout)
		if resolved == "MISSING_GETENT" {
			failures = append(failures, fmt.Sprintf("%s: 'getent' is not installed - required for mesh DNS verification", hostName))
			continue
		}
		if resolved == "" {
			failures = append(failures, fmt.Sprintf("%s: system resolver cannot resolve 'quartermaster.internal'", hostName))
			continue
		}

		expectedPeerIPs := privateerExpectedPeerIPs(manifest, hostName)
		if len(expectedPeerIPs) > 0 {
			peerCmd := "wg show wg0 allowed-ips 2>/dev/null || true"
			peerResult, peerErr := base.RunCommand(ctx, hostInfo, peerCmd)
			if peerErr != nil {
				detail := strings.TrimSpace(routeResultOutput(peerResult))
				if detail == "" {
					detail = peerErr.Error()
				}
				failures = append(failures, fmt.Sprintf("%s: cannot read wg0 peer allowed-ips: %s", hostName, detail))
			} else {
				applied := strings.TrimSpace(peerResult.Stdout)
				for peerName, peerIP := range expectedPeerIPs {
					if !strings.Contains(applied, peerIP+"/32") {
						failures = append(failures, fmt.Sprintf("%s: expected mesh peer %s (%s/32) is missing from wg0 allowed-ips", hostName, peerName, peerIP))
					}
				}
			}
		}

		for peerName, peerIP := range meshIPs {
			if peerName == hostName {
				continue
			}
			routeCmd := fmt.Sprintf("ip route get %s | grep -q ' dev wg0 ' && echo OK || { ip route get %s; exit 1; }", peerIP, peerIP)
			routeResult, routeErr := base.RunCommand(ctx, hostInfo, routeCmd)
			if routeErr != nil {
				detail := strings.TrimSpace(routeResultOutput(routeResult))
				if detail == "" {
					detail = routeErr.Error()
				}
				failures = append(failures, fmt.Sprintf("%s: mesh route to %s (%s) is not via wg0: %s", hostName, peerName, peerIP, detail))
			}
		}

		ux.Success(cmd.OutOrStdout(), fmt.Sprintf("privateer active, system resolver maps quartermaster.internal to %s", resolved))
	}

	if len(failures) > 0 {
		return fmt.Errorf("mesh health check failed on %d host(s):\n  %s", len(failures), strings.Join(failures, "\n  "))
	}

	ux.Success(cmd.OutOrStdout(), "Mesh healthy on all privateer hosts")
	return nil
}

func privateerExpectedPeerIPs(manifest *inventory.Manifest, hostName string) map[string]string {
	out := map[string]string{}
	for _, peer := range buildPrivateerStaticPeers(manifest, hostName) {
		name, nameOK := peer["name"].(string)
		allowed, allowedOK := peer["allowed_ips"].([]string)
		if !nameOK || !allowedOK {
			continue
		}
		if name == "" || len(allowed) == 0 {
			continue
		}
		ip := strings.TrimSuffix(allowed[0], "/32")
		if net.ParseIP(ip) == nil {
			continue
		}
		out[name] = ip
	}
	return out
}

func verifyQuartermasterMeshReachability(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	privateerSvc, ok := manifest.Services["privateer"]
	if !ok || !privateerSvc.Enabled {
		return nil
	}
	qmSvc, ok := manifest.Services["quartermaster"]
	if !ok || !qmSvc.Enabled {
		return nil
	}
	qmHostName := qmSvc.Host
	if qmHostName == "" && len(qmSvc.Hosts) > 0 {
		qmHostName = qmSvc.Hosts[0]
	}
	qmIP := manifest.MeshAddress(qmHostName)
	if net.ParseIP(qmIP) == nil {
		return fmt.Errorf("quartermaster host %q has no mesh IP", qmHostName)
	}
	qmPort := defaultGRPCPort("quartermaster")
	if qmSvc.GRPCPort != 0 {
		qmPort = qmSvc.GRPCPort
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Verifying Quartermaster mesh reachability on %d privateer host(s)...\n", len(orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts)))
	base := provisioner.NewBaseProvisioner("quartermaster-mesh-verify", pool)
	var failures []string
	for _, hostName := range orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts) {
		hostInfo, ok := manifest.Hosts[hostName]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: not found in manifest", hostName))
			continue
		}
		cmdText := fmt.Sprintf("if command -v nc >/dev/null 2>&1; then nc -vz -w 3 %[1]s %[2]d; elif command -v timeout >/dev/null 2>&1 && command -v bash >/dev/null 2>&1; then timeout 4 bash -c 'cat < /dev/null > /dev/tcp/%[1]s/%[2]d'; else echo 'missing TCP probe tool: install nc or provide bash+timeout'; exit 127; fi", qmIP, qmPort)
		result, err := base.RunCommand(ctx, hostInfo, cmdText)
		if err != nil {
			detail := strings.TrimSpace(routeResultOutput(result))
			if detail == "" {
				detail = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s: cannot reach quartermaster %s:%d over mesh: %s", hostName, qmIP, qmPort, detail))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d host(s) failed:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	ux.Success(cmd.OutOrStdout(), fmt.Sprintf("Quartermaster reachable at %s:%d from all privateer hosts", qmIP, qmPort))
	return nil
}

func verifyKafkaControllerMesh(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool) error {
	if manifest == nil || manifest.Infrastructure.Kafka == nil || !manifest.Infrastructure.Kafka.Enabled || len(manifest.Infrastructure.Kafka.Controllers) == 0 {
		return nil
	}
	controllers := manifest.Infrastructure.Kafka.Controllers
	fmt.Fprintf(cmd.OutOrStdout(), "  Verifying Kafka controller mesh on %d controller(s)...\n", len(controllers))

	base := provisioner.NewBaseProvisioner("kafka-controller-mesh-verify", pool)
	var failures []string
	for _, source := range controllers {
		sourceHost, ok := manifest.Hosts[source.Host]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: source host not found", source.Host))
			continue
		}
		for _, target := range controllers {
			if source.Host == target.Host {
				continue
			}
			targetIP := manifest.MeshAddress(target.Host)
			targetPort := target.Port
			if targetPort == 0 {
				targetPort = 9093
			}
			checkCmd := fmt.Sprintf("timeout 3 bash -lc ':</dev/tcp/%s/%d'", targetIP, targetPort)
			result, err := base.RunCommand(ctx, sourceHost, checkCmd)
			if err != nil {
				detail := strings.TrimSpace(routeResultOutput(result))
				if detail == "" {
					detail = err.Error()
				}
				failures = append(failures, fmt.Sprintf("%s -> %s (%s:%d): %s", source.Host, target.Host, targetIP, targetPort, detail))
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("controller overlay TCP failed:\n  %s", strings.Join(failures, "\n  "))
	}
	ux.Success(cmd.OutOrStdout(), "Kafka controllers reachable over mesh")
	return nil
}

func routeResultOutput(result *ssh.CommandResult) string {
	if result == nil {
		return ""
	}
	return strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
}
