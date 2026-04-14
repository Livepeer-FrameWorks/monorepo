package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/credentials"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/githubapp"
	fwsops "frameworks/cli/pkg/sops"
	commodoreCli "frameworks/pkg/clients/commodore"
	purserclient "frameworks/pkg/clients/purser"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	infra "frameworks/pkg/models"
	pb "frameworks/pkg/proto"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	pkgdns "frameworks/pkg/dns"
	"frameworks/pkg/servicedefs"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// newClusterProvisionCmd creates the provision command
func newClusterProvisionCmd() *cobra.Command {
	var manifestPath string
	var only string
	var dryRun bool
	var force bool
	var ignoreValidation bool
	var repo string
	var githubAppID int64
	var githubInstallID int64
	var githubKeyPath string
	var ageKeyFile string

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision cluster infrastructure and services",
		Long: `Provision cluster infrastructure and services from manifest:

Phase Options (--only):
  infrastructure  - Provision Postgres, Redis, Kafka, Zookeeper, ClickHouse
  applications    - Provision FrameWorks services
  interfaces      - Provision Nginx/Caddy, Chartroom, Foredeck, Logbook
  all             - Provision everything (default)

Provisioning is idempotent - safe to run multiple times.
Existing services will be detected and skipped unless --force is used.

Use --repo to fetch manifests from a private GitHub repo via GitHub App credentials.`,
		Example: `  # Provision all infrastructure
  frameworks cluster provision --only infrastructure --manifest cluster.yaml

  # Dry-run to see what would be provisioned
  frameworks cluster provision --manifest cluster.yaml --dry-run

  # Provision from a GitHub repo (uses configured GitHub App credentials)
  frameworks cluster provision --repo org/infra-repo

  # Force re-provision even if services exist
  frameworks cluster provision --force --manifest cluster.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo != "" {
				return runProvisionFromRepo(cmd, repo, githubAppID, githubInstallID, githubKeyPath, ageKeyFile, manifestPath, only, dryRun, force, ignoreValidation)
			}
			return runProvision(cmd, manifestPath, ageKeyFile, only, dryRun, force, ignoreValidation)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file (or filename within repo)")
	cmd.Flags().StringVar(&only, "only", "all", "Phase to provision (infrastructure|applications|interfaces|all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show plan without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force re-provision even if exists")
	cmd.Flags().BoolVar(&ignoreValidation, "ignore-validation", false, "Continue even if health validation fails (DANGEROUS)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo (owner/repo) to fetch manifest from")
	cmd.Flags().Int64Var(&githubAppID, "github-app-id", 0, "GitHub App ID (overrides config)")
	cmd.Flags().Int64Var(&githubInstallID, "github-installation-id", 0, "GitHub Installation ID (overrides config)")
	cmd.Flags().StringVar(&githubKeyPath, "github-private-key", "", "Path to GitHub App private key PEM (overrides config)")
	cmd.Flags().StringVar(&ageKeyFile, "age-key", "", "Path to age private key for SOPS-encrypted env files (default: $SOPS_AGE_KEY_FILE or ~/.config/sops/age/keys.txt)")

	// Bootstrap admin user creation
	cmd.Flags().String("bootstrap-admin-email", "", "Create an initial operator user with this email")
	cmd.Flags().String("bootstrap-admin-password", "", "Plaintext password for bootstrap admin (prefer --bootstrap-admin-password-env or --bootstrap-admin-password-file)")
	cmd.Flags().String("bootstrap-admin-password-env", "", "Read bootstrap admin password from this environment variable")
	cmd.Flags().String("bootstrap-admin-password-file", "", "Read bootstrap admin password from this file")
	cmd.Flags().String("bootstrap-admin-first-name", "Admin", "First name for bootstrap admin")
	cmd.Flags().String("bootstrap-admin-last-name", "User", "Last name for bootstrap admin")

	// Control-plane validation
	cmd.Flags().Bool("strict-control-plane", false, "Fail (exit 1) if post-provision control-plane validation has warnings")

	return cmd
}

// runProvisionFromRepo fetches the manifest from a GitHub repo and provisions.
func runProvisionFromRepo(cmd *cobra.Command, repo string, appID, installID int64, keyPath, ageKeyFile, manifestFile, only string, dryRun, force, ignoreValidation bool) error {
	cfg, _, err := fwcfg.Load()
	if err != nil {
		return err
	}

	// Resolve credentials: flags > config
	gh := cfg.GitHub
	if gh == nil {
		gh = &fwcfg.GitHubApp{}
	}
	if appID == 0 {
		appID = gh.AppID
	}
	if installID == 0 {
		installID = gh.InstallationID
	}
	if keyPath == "" {
		keyPath = gh.PrivateKeyPath
	}
	if repo == "" {
		repo = gh.Repo
	}
	ref := gh.Ref

	if appID == 0 || installID == 0 || keyPath == "" {
		return fmt.Errorf("GitHub App credentials required: set via flags or 'frameworks config set github.*'")
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Fetching manifest from %s...\n", repo)

	ghClient, err := githubapp.NewClient(cmd.Context(), githubapp.Config{
		AppID:          appID,
		InstallationID: installID,
		PrivateKeyPath: keyPath,
		Repo:           repo,
		Ref:            ref,
	})
	if err != nil {
		return fmt.Errorf("GitHub App authentication failed: %w", err)
	}

	// Fetch the cluster manifest
	data, err := ghClient.Fetch(cmd.Context(), manifestFile)
	if err != nil {
		return fmt.Errorf("failed to fetch %s from %s: %w", manifestFile, repo, err)
	}

	// Parse manifest structure (validation happens after host inventory merge in runProvision)
	manifest, err := inventory.ParseManifest(data)
	if err != nil {
		return fmt.Errorf("failed to parse manifest from %s: %w", repo, err)
	}

	// Write manifest to a temp directory so all fetched files resolve correctly
	tmpDir, err := os.MkdirTemp("", "frameworks-provision-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Resolve manifest-relative paths to repo-relative for GitHub fetch.
	manifestDir := filepath.Dir(manifestFile)

	// Collect all referenced file paths (env files + host inventory)
	filesToFetch := []string{}
	for _, envFile := range manifest.SharedEnvFiles() {
		repoPath, pathErr := resolveManifestToRepoPath(manifestDir, envFile)
		if pathErr != nil {
			return pathErr
		}
		filesToFetch = append(filesToFetch, repoPath)
	}
	if manifest.HostsFile != "" {
		repoPath, pathErr := resolveManifestToRepoPath(manifestDir, manifest.HostsFile)
		if pathErr != nil {
			return pathErr
		}
		filesToFetch = append(filesToFetch, repoPath)
	}
	for _, svc := range manifest.Services {
		if svc.EnvFile != "" {
			repoPath, pathErr := resolveManifestToRepoPath(manifestDir, svc.EnvFile)
			if pathErr != nil {
				return pathErr
			}
			filesToFetch = append(filesToFetch, repoPath)
		}
	}
	for _, iface := range manifest.Interfaces {
		if iface.EnvFile != "" {
			repoPath, pathErr := resolveManifestToRepoPath(manifestDir, iface.EnvFile)
			if pathErr != nil {
				return pathErr
			}
			filesToFetch = append(filesToFetch, repoPath)
		}
	}

	// Deduplicate
	seen := make(map[string]bool)
	unique := filesToFetch[:0]
	for _, f := range filesToFetch {
		if !seen[f] {
			seen[f] = true
			unique = append(unique, f)
		}
	}

	for _, name := range unique {
		fileData, fetchErr := ghClient.Fetch(cmd.Context(), name)
		if fetchErr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warning: could not fetch %s: %v\n", name, fetchErr)
			continue
		}

		// Decrypt SOPS-encrypted files transparently (format inferred from extension)
		if fwsops.IsEncrypted(fileData) {
			plain, decErr := fwsops.DecryptData(fileData, fwsops.FormatFromPath(name), ageKeyFile)
			if decErr != nil {
				return fmt.Errorf("decrypt %s: %w", name, decErr)
			}
			fileData = plain
			fmt.Fprintf(cmd.OutOrStdout(), "Decrypted %s (SOPS/age)\n", name)
		}

		localPath := filepath.Join(tmpDir, name)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warning: could not create dir for %s: %v\n", name, err)
			continue
		}
		if writeErr := os.WriteFile(localPath, fileData, 0o600); writeErr != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Warning: could not write %s: %v\n", name, writeErr)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Fetched %s\n", name)
		}
	}

	// Write fetched manifest preserving its directory structure so that
	// manifest-relative paths (env_files, hosts_file) resolve correctly.
	tmpManifest := filepath.Join(tmpDir, manifestFile)
	if err := os.MkdirAll(filepath.Dir(tmpManifest), 0o700); err != nil {
		return fmt.Errorf("failed to create manifest dir: %w", err)
	}
	if err := os.WriteFile(tmpManifest, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temp manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Provisioning cluster from %s/%s\n", repo, manifestFile)
	return runProvision(cmd, tmpManifest, ageKeyFile, only, dryRun, force, ignoreValidation)
}

// runProvision executes the provision command
func runProvision(cmd *cobra.Command, manifestPath, ageKeyFile, only string, dryRun, force, ignoreValidation bool) error {
	// Load manifest (merges host inventory from hosts_file if set)
	manifest, err := inventory.LoadWithHosts(manifestPath, ageKeyFile)
	if err != nil {
		return fmt.Errorf("failed to load manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Provisioning cluster from manifest: %s\n", manifestPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Cluster type: %s, Profile: %s\n", manifest.Type, manifest.Profile)
	fmt.Fprintf(cmd.OutOrStdout(), "Phase: %s\n\n", only)

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "[DRY-RUN MODE - No changes will be made]")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Convert only flag to phase
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

	if phaseRequiresGatewayMeshValidation(phase) {
		if err = validateGatewayMeshCoverage(manifest); err != nil {
			return fmt.Errorf("invalid manifest: %w", err)
		}
		if err = validateInternalGRPCTLSCoverage(manifest); err != nil {
			return fmt.Errorf("invalid manifest: %w", err)
		}
	}

	// Create execution plan
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

	// Show plan
	fmt.Fprintln(cmd.OutOrStdout(), "Execution Plan:")
	for i, batch := range plan.Batches {
		fmt.Fprintf(cmd.OutOrStdout(), "  Batch %d (parallel):\n", i+1)
		for _, task := range batch {
			if task.ClusterID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s) on %s [cluster: %s]\n", task.Name, task.Type, task.Host, task.ClusterID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s) on %s\n", task.Name, task.Type, task.Host)
			}
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal tasks: %d\n\n", len(plan.AllTasks))

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry-run complete. Use without --dry-run to execute.")
		return nil
	}

	// Execute provisioning
	manifestDir := filepath.Dir(manifestPath)
	if err := executeProvision(ctx, cmd, manifest, plan, force, ignoreValidation, manifestDir); err != nil {
		return fmt.Errorf("provisioning failed: %w", err)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n✓ Provisioning complete!")
	return nil
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
func executeProvision(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, force, ignoreValidation bool, manifestDir string) error {
	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Track successfully provisioned tasks for rollback
	var completed []provisionedTask

	// Execute each batch sequentially
	runtimeData := make(map[string]interface{})
	if err := ensureProvisionGeoIP(ctx, cmd.OutOrStdout(), manifest, manifestDir, sshPool); err != nil {
		return err
	}

	for batchNum, batch := range plan.Batches {
		fmt.Fprintf(cmd.OutOrStdout(), "Executing Batch %d/%d:\n", batchNum+1, len(plan.Batches))

		// Execute tasks in batch (could be parallelized)
		for _, task := range batch {
			if err := ctx.Err(); err != nil {
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("provisioning halted before %s: %w", task.Name, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Provisioning %s on %s...\n", task.Name, task.Host)

			// Get host config
			host, ok := manifest.GetHost(task.Host)
			if !ok {
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("host %s not found in manifest", task.Host)
			}

			// Provision based on task type
			stopProgress := startTaskProgressLogger(cmd, task, 30*time.Second)
			outcome, err := provisionTask(ctx, task, host, sshPool, manifest, force, ignoreValidation, runtimeData, manifestDir)
			if err != nil {
				stopProgress()
				fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Provisioning failed for %s: %v\n", task.Name, err)
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("failed to provision %s: %w", task.Name, err)
			}
			stopProgress()

			// Only roll back services this run actually started from a stopped/nonexistent state.
			if !outcome.previouslyRunning {
				completed = append(completed, provisionedTask{task: task, host: host, config: outcome.config})
			}

			// Bootstrap Logic: Run after Quartermaster is healthy
			if task.Type == "quartermaster" {
				fmt.Fprintln(cmd.OutOrStdout(), "  Running Cluster Bootstrap (System Tenant)...")
				result, err := runBootstrap(ctx, manifest, manifestDir)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Bootstrap failed: %v\n", err)
					fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("bootstrap failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "    ✓ System Tenant bootstrapped")
				// Store Quartermaster runtime data for downstream services.
				if result.SystemTenantID != "" {
					runtimeData["system_tenant_id"] = result.SystemTenantID
				}
				if result.ServiceToken != "" {
					runtimeData["service_token"] = result.ServiceToken
				}
				if result.QMGRPCAddr != "" {
					runtimeData["quartermaster_grpc_addr"] = result.QMGRPCAddr
				}
			}

			if err := maybeRegisterPublicServiceInstance(ctx, cmd.OutOrStdout(), manifest, task, host, outcome, runtimeData, manifestDir); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "    Warning: public service registration skipped for %s: %v\n", task.Name, err)
			}
			if err := maybeRegisterIngressDesiredState(ctx, cmd.OutOrStdout(), manifest, task, host, outcome); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "    Warning: ingress desired state registration skipped for %s: %v\n", task.Name, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s provisioned\n", task.Name)
		}

		if err := maybeReconcileBatchFoghornAssignments(ctx, cmd, batch, manifest, runtimeData); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Foghorn reconciliation failed: %v\n", err)
			fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
			rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
			return fmt.Errorf("foghorn reconciliation failed: %w", err)
		}

		// Mesh preflight gate: after a batch containing Privateer tasks,
		// verify mesh health before proceeding to application services.
		if batchContainsPrivateer(batch) && batchNum+1 < len(plan.Batches) {
			fmt.Fprintln(cmd.OutOrStdout(), "")
			privateerSvc := manifest.Services["privateer"]
			meshHosts := orchestrator.EffectivePrivateerHosts(privateerSvc, manifest.Hosts)
			if err := verifyMeshHealth(ctx, cmd, manifest, sshPool, meshHosts); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Mesh verification failed: %v\n", err)
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

	// Post-provision: bootstrap Purser cluster pricing, admin user, control-plane validation
	if err := postProvisionFinalize(ctx, cmd, manifest, runtimeData); err != nil {
		return err
	}

	return nil
}

// postProvisionFinalize handles Purser pricing bootstrap, optional admin user creation,
// and control-plane validation after all service batches are complete.
func postProvisionFinalize(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]interface{}) error {
	systemTenantID, ok := runtimeData["system_tenant_id"].(string)
	serviceToken, stOK := runtimeData["service_token"].(string)

	if !ok || !stOK || systemTenantID == "" || serviceToken == "" {
		// Bootstrap didn't run (e.g. --only=interfaces), skip finalization
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout(), "  Control-plane finalization...")

	// 1. Bootstrap Purser cluster pricing for clusters with manifest pricing config
	if err := maybeBootstrapClusterPricing(ctx, cmd, manifest, serviceToken); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "    Warning: cluster pricing bootstrap failed: %v\n", err)
	}

	// 2. Optional bootstrap admin user
	if err := maybeBootstrapAdminUser(ctx, cmd, manifest, systemTenantID, serviceToken); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "    Warning: bootstrap admin user creation failed: %v\n", err)
	}

	// 3. Control-plane validation
	return validateControlPlane(ctx, cmd, manifest, runtimeData)
}

func maybeBootstrapClusterPricing(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, serviceToken string) error {
	hasPricingClusters := false
	for _, cc := range manifest.Clusters {
		if cc.Pricing != nil {
			hasPricingClusters = true
			break
		}
	}
	if !hasPricingClusters {
		return nil
	}

	purserAddr, err := resolveServiceGRPCAddr(manifest, "purser", 19003)
	if err != nil {
		return fmt.Errorf("cannot resolve Purser address: %w", err)
	}

	p, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:      purserAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Purser gRPC: %w", err)
	}
	defer p.Close()

	for clusterID, cc := range manifest.Clusters {
		if cc.Pricing == nil {
			continue
		}
		req := &pb.SetClusterPricingRequest{
			ClusterId:    clusterID,
			PricingModel: cc.Pricing.Model,
		}
		if cc.Pricing.RequiredTierLevel != nil {
			v := int32(*cc.Pricing.RequiredTierLevel)
			req.RequiredTierLevel = &v
		}
		if cc.Pricing.AllowFreeTier != nil {
			req.AllowFreeTier = cc.Pricing.AllowFreeTier
		}
		if len(cc.Pricing.DefaultQuotas) > 0 {
			m := make(map[string]interface{}, len(cc.Pricing.DefaultQuotas))
			for k, v := range cc.Pricing.DefaultQuotas {
				m[k] = float64(v)
			}
			s, sErr := structpb.NewStruct(m)
			if sErr == nil {
				req.DefaultQuotas = s
			}
		}
		if _, err := p.SetClusterPricing(ctx, req); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "    Warning: failed to set pricing for cluster %s: %v\n", clusterID, err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Cluster pricing set for %s (model=%s)\n", clusterID, cc.Pricing.Model)
		}
	}
	return nil
}

func maybeBootstrapAdminUser(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, systemTenantID, serviceToken string) error {
	email, err := cmd.Flags().GetString("bootstrap-admin-email")
	if err != nil || email == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\n  To create your first operator account:\n")
		fmt.Fprintf(cmd.OutOrStdout(), "    frameworks admin users create --tenant-id %s --email <email> --password <pw>\n\n", systemTenantID)
		return nil
	}

	// Resolve password: file > env > plaintext
	password := ""
	if pwFile, fErr := cmd.Flags().GetString("bootstrap-admin-password-file"); fErr == nil && pwFile != "" {
		data, readErr := os.ReadFile(pwFile)
		if readErr != nil {
			return fmt.Errorf("failed to read password file: %w", readErr)
		}
		password = strings.TrimSpace(string(data))
	}
	if password == "" {
		if pwEnv, eErr := cmd.Flags().GetString("bootstrap-admin-password-env"); eErr == nil && pwEnv != "" {
			password = os.Getenv(pwEnv)
		}
	}
	if password == "" {
		if pw, pErr := cmd.Flags().GetString("bootstrap-admin-password"); pErr == nil {
			password = pw
		}
	}
	if password == "" {
		return fmt.Errorf("--bootstrap-admin-email requires a password (use --bootstrap-admin-password, --bootstrap-admin-password-env, or --bootstrap-admin-password-file)")
	}

	firstName, _ := cmd.Flags().GetString("bootstrap-admin-first-name") //nolint:errcheck // flag always exists
	lastName, _ := cmd.Flags().GetString("bootstrap-admin-last-name")   //nolint:errcheck // flag always exists

	commodoreAddr, err := resolveServiceGRPCAddr(manifest, "commodore", 19001)
	if err != nil {
		return fmt.Errorf("cannot resolve Commodore address: %w", err)
	}

	cli, err := commodoreCli.NewGRPCClient(commodoreCli.GRPCConfig{
		GRPCAddr:      commodoreAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to Commodore gRPC: %w", err)
	}
	defer cli.Close()

	resp, err := cli.CreateUserInTenant(ctx, &pb.CreateUserInTenantRequest{
		TenantId:  systemTenantID,
		Email:     email,
		Password:  password,
		FirstName: firstName,
		LastName:  lastName,
		Role:      "owner",
	})
	if err != nil {
		return fmt.Errorf("failed to create bootstrap admin: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "    ✓ Created operator account: %s (user_id: %s) in tenant %s\n",
		resp.User.GetEmail(), resp.User.GetId(), systemTenantID)
	return nil
}

func validateControlPlane(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]interface{}) error {
	systemTenantID, stOk := runtimeData["system_tenant_id"].(string)
	serviceToken, tkOk := runtimeData["service_token"].(string)
	grpcAddr, gaOk := runtimeData["quartermaster_grpc_addr"].(string)

	if !stOk || !tkOk || !gaOk || systemTenantID == "" || serviceToken == "" || grpcAddr == "" {
		return nil
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "    Warning: could not connect to Quartermaster for validation: %v\n", err)
		return nil
	}
	defer client.Close()

	var warnings []string

	// Check clusters for default and official flags
	clustersResp, err := client.ListClusters(ctx, nil)
	if err == nil {
		hasDefault := false
		hasOfficial := false
		for _, c := range clustersResp.GetClusters() {
			if c.GetIsDefaultCluster() {
				hasDefault = true
			}
			if c.GetIsPlatformOfficial() {
				hasOfficial = true
			}
		}
		if !hasDefault {
			warnings = append(warnings, "No default cluster - new tenant signups will not auto-subscribe")
		}
		if !hasOfficial {
			warnings = append(warnings, "No platform-official cluster - billing tier access will not work")
		}
	}

	// Check for platform tenant user
	commodoreAddr, commodoreErr := resolveServiceGRPCAddr(manifest, "commodore", 19001)
	if commodoreErr == nil {
		cli, cliErr := commodoreCli.NewGRPCClient(commodoreCli.GRPCConfig{
			GRPCAddr:      commodoreAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  serviceToken,
			AllowInsecure: true,
		})
		if cliErr == nil {
			defer cli.Close()
			countResp, countErr := cli.GetTenantUserCount(ctx, systemTenantID)
			if countErr == nil && countResp.GetActiveCount() == 0 {
				warnings = append(warnings, "No users in platform tenant - use 'frameworks admin users create --role owner' or re-run with --bootstrap-admin-email")
			} else if countErr != nil {
				warnings = append(warnings, fmt.Sprintf("Could not check platform tenant users: %v", countErr))
			}
		}
	}

	// Check cluster pricing for official clusters
	purserAddr, purserErr := resolveServiceGRPCAddr(manifest, "purser", 19003)
	if purserErr == nil {
		p, pErr := purserclient.NewGRPCClient(purserclient.GRPCConfig{
			GRPCAddr:      purserAddr,
			Logger:        logging.NewLogger(),
			ServiceToken:  serviceToken,
			AllowInsecure: true,
		})
		if pErr == nil {
			defer p.Close()
			for clusterID, cc := range manifest.Clusters {
				if cc.Pricing == nil {
					continue
				}
				pricing, pricingErr := p.GetClusterPricing(ctx, clusterID)
				if pricingErr != nil || pricing == nil || pricing.GetPricingModel() == "" {
					warnings = append(warnings, fmt.Sprintf("No pricing config for cluster %s (manifest declares pricing but Purser has none)", clusterID))
				}
			}
		}
	}

	if len(warnings) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\n  Control-plane validation warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", w)
		}
		strict, _ := cmd.Flags().GetBool("strict-control-plane") //nolint:errcheck // flag always exists
		if strict {
			return fmt.Errorf("control-plane validation failed with %d warning(s) (--strict-control-plane is set)", len(warnings))
		}
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "    ✓ Control-plane validation passed")
	}

	return nil
}

// resolveServiceGRPCAddr resolves a service's gRPC address from the manifest,
// using the same host→ExternalIP pattern as resolveQuartermasterRuntimeData.
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

	return fmt.Sprintf("%s:%d", host.ExternalIP, grpcPort), nil
}

func maybeReconcileBatchFoghornAssignments(ctx context.Context, cmd *cobra.Command, batch []*orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]interface{}) error {
	if !batchContainsService(batch, "foghorn") {
		return nil
	}

	return reconcileFoghornClusterAssignments(ctx, cmd, manifest, runtimeData)
}

// batchContainsPrivateer returns true if any task in the batch is a Privateer deployment.
func batchContainsPrivateer(batch []*orchestrator.Task) bool {
	return batchContainsService(batch, "privateer")
}

func batchContainsService(batch []*orchestrator.Task, serviceName string) bool {
	for _, task := range batch {
		if task.ServiceID == serviceName {
			return true
		}
	}
	return false
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

func phaseRequiresGatewayMeshValidation(phase orchestrator.Phase) bool {
	return phase == orchestrator.PhaseApplications || phase == orchestrator.PhaseAll
}

func serviceRunning(state *detect.ServiceState) bool {
	return state != nil && state.Exists && state.Running
}

type foghornClusterAssigner interface {
	AssignFoghornToCluster(ctx context.Context, req *pb.AssignFoghornToClusterRequest) error
}

type bootstrapTokenCreator interface {
	CreateBootstrapToken(ctx context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error)
}

type publicServiceRegistrar interface {
	BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error)
}

type ingressDesiredStateRegistrar interface {
	UpsertTLSBundle(ctx context.Context, bundle *pb.TLSBundle) (*pb.TLSBundleResponse, error)
	UpsertIngressSite(ctx context.Context, site *pb.IngressSite) (*pb.IngressSiteResponse, error)
}

func resolveQuartermasterRuntimeData(manifest *inventory.Manifest) (string, string, error) {
	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		if cfg, _, err := fwcfg.Load(); err == nil {
			cliCtx := fwcfg.GetCurrent(cfg)
			cliCtx.Auth = fwcfg.ResolveAuth(cliCtx)
			serviceToken = cliCtx.Auth.ServiceToken
		}
	}
	if serviceToken == "" {
		return "", "", fmt.Errorf("service token required for bootstrapping (set SERVICE_TOKEN or run 'frameworks login')")
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

	grpcPort := 19002
	if qmSvc.GRPCPort != 0 {
		grpcPort = qmSvc.GRPCPort
	}

	return serviceToken, fmt.Sprintf("%s:%d", host.ExternalIP, grpcPort), nil
}

func ensureEdgeTelemetryJWTKeypair(runtimeData map[string]interface{}) error {
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

func ensurePrivateerEnrollmentToken(ctx context.Context, manifest *inventory.Manifest, runtimeData map[string]interface{}, clusterID string) error {
	if clusterID == "" {
		return fmt.Errorf("privateer task missing cluster_id")
	}

	if tokens, ok := runtimeData["enrollment_tokens"].(map[string]string); ok {
		if tokens[clusterID] != "" {
			return nil
		}
	} else {
		runtimeData["enrollment_tokens"] = map[string]string{}
	}

	serviceToken, grpcAddr, err := resolveQuartermasterRuntimeData(manifest)
	if err != nil {
		return err
	}
	systemTenantID, ok := runtimeData["system_tenant_id"].(string)
	if !ok || systemTenantID == "" {
		return fmt.Errorf("missing system tenant id for privateer enrollment token")
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("connect Quartermaster for privateer enrollment token: %w", err)
	}
	defer client.Close()

	if err := ensurePrivateerEnrollmentTokenWithClient(ctx, runtimeData, systemTenantID, clusterID, client); err != nil {
		return err
	}

	runtimeData["service_token"] = serviceToken
	runtimeData["quartermaster_grpc_addr"] = grpcAddr
	return nil
}

func ensurePrivateerEnrollmentTokenWithClient(ctx context.Context, runtimeData map[string]interface{}, systemTenantID, clusterID string, creator bootstrapTokenCreator) error {
	if systemTenantID == "" {
		return fmt.Errorf("missing system tenant id for privateer enrollment token")
	}

	resp, err := creator.CreateBootstrapToken(ctx, &pb.CreateBootstrapTokenRequest{
		Name:      fmt.Sprintf("Infrastructure Enrollment Token for %s", clusterID),
		Kind:      "infrastructure_node",
		Ttl:       "720h",
		TenantId:  &systemTenantID,
		ClusterId: &clusterID,
	})
	if err != nil {
		return fmt.Errorf("failed to create bootstrap token for cluster '%s': %w", clusterID, err)
	}

	tokens, ok := runtimeData["enrollment_tokens"].(map[string]string)
	if !ok || tokens == nil {
		tokens = make(map[string]string)
	}
	tokens[clusterID] = resp.GetToken().GetToken()
	runtimeData["enrollment_tokens"] = tokens
	return nil
}

func ensurePrivateerCertIssueToken(ctx context.Context, manifest *inventory.Manifest, runtimeData map[string]interface{}, clusterID, nodeID string) error {
	if clusterID == "" {
		return fmt.Errorf("privateer task missing cluster_id")
	}
	if nodeID == "" {
		return fmt.Errorf("privateer task missing node_id")
	}

	if tokens, ok := runtimeData["cert_issue_tokens"].(map[string]string); ok {
		if tokens[nodeID] != "" {
			return nil
		}
	}

	serviceToken, grpcAddr, err := resolveQuartermasterRuntimeData(manifest)
	if err != nil {
		return err
	}
	systemTenantID, ok := runtimeData["system_tenant_id"].(string)
	if !ok || systemTenantID == "" {
		return fmt.Errorf("missing system tenant id for privateer cert issue token")
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("connect Quartermaster for privateer cert issue token: %w", err)
	}
	defer client.Close()

	return ensurePrivateerCertIssueTokenWithClient(ctx, runtimeData, systemTenantID, clusterID, nodeID, client)
}

func ensurePrivateerCertIssueTokenWithClient(ctx context.Context, runtimeData map[string]interface{}, systemTenantID, clusterID, nodeID string, creator bootstrapTokenCreator) error {
	metadata, err := structpb.NewStruct(map[string]interface{}{
		"node_id": nodeID,
		"purpose": "cert_sync",
	})
	if err != nil {
		return fmt.Errorf("build cert issue token metadata: %w", err)
	}

	resp, err := creator.CreateBootstrapToken(ctx, &pb.CreateBootstrapTokenRequest{
		Name:      fmt.Sprintf("Internal Cert Sync Token for %s", nodeID),
		Kind:      "infrastructure_node",
		Ttl:       "720h",
		TenantId:  &systemTenantID,
		ClusterId: &clusterID,
		Metadata:  metadata,
	})
	if err != nil {
		return fmt.Errorf("failed to create cert issue token for node '%s': %w", nodeID, err)
	}

	tokens, ok := runtimeData["cert_issue_tokens"].(map[string]string)
	if !ok || tokens == nil {
		tokens = make(map[string]string)
	}
	tokens[nodeID] = resp.GetToken().GetToken()
	runtimeData["cert_issue_tokens"] = tokens
	return nil
}

func maybeRegisterPublicServiceInstance(ctx context.Context, out io.Writer, manifest *inventory.Manifest, task *orchestrator.Task, host inventory.Host, outcome *taskProvisionOutcome, runtimeData map[string]interface{}, manifestDir string) error {
	if outcome == nil || outcome.deferred || !outcome.running {
		return nil
	}

	serviceName := task.ServiceID
	if _, ok := publicServiceType(serviceName); !ok || selfRegisters(serviceName) {
		return nil
	}

	serviceToken, grpcAddr, err := resolveQuartermasterRuntimeData(manifest)
	if err != nil {
		return err
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("connect Quartermaster for public service registration: %w", err)
	}
	defer client.Close()

	runtimeData["service_token"] = serviceToken
	runtimeData["quartermaster_grpc_addr"] = grpcAddr
	return registerPublicServiceInstanceWithClient(ctx, out, manifest, task, host, runtimeData, manifestDir, client)
}

func maybeRegisterIngressDesiredState(ctx context.Context, out io.Writer, manifest *inventory.Manifest, task *orchestrator.Task, host inventory.Host, outcome *taskProvisionOutcome) error {
	if outcome == nil || outcome.deferred || !outcome.running {
		return nil
	}
	if task.Type != "nginx" && task.Type != "caddy" {
		return nil
	}

	serviceToken, grpcAddr, err := resolveQuartermasterRuntimeData(manifest)
	if err != nil {
		return err
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("connect Quartermaster for ingress desired state: %w", err)
	}
	defer client.Close()

	return registerIngressDesiredStateWithClient(ctx, out, manifest, task, host, client)
}

func registerPublicServiceInstanceWithClient(ctx context.Context, out io.Writer, manifest *inventory.Manifest, task *orchestrator.Task, host inventory.Host, runtimeData map[string]interface{}, manifestDir string, registrar publicServiceRegistrar) error {
	serviceName := task.ServiceID
	serviceType, ok := publicServiceType(serviceName)
	if !ok || selfRegisters(serviceName) {
		return nil
	}

	svcDef, hasDef := servicedefs.Lookup(serviceName)
	if !hasDef {
		return nil
	}

	hostCluster := manifest.HostCluster(task.Host)
	if hostCluster == "" {
		hostCluster = manifest.ResolveCluster(serviceName)
	}
	if hostCluster == "" {
		return fmt.Errorf("service %s has no resolved cluster", task.Name)
	}

	serviceToken, ok := runtimeData["service_token"].(string)
	if !ok || serviceToken == "" {
		return fmt.Errorf("missing Quartermaster service token")
	}
	healthPath := svcDef.HealthPath
	effectivePort := 0
	if svc, ok := manifest.Services[serviceName]; ok {
		if p, err := resolvePort(serviceName, svc); err == nil {
			effectivePort = p
		}
	} else if iface, ok := manifest.Interfaces[serviceName]; ok {
		if p, err := resolvePort(serviceName, iface); err == nil {
			effectivePort = p
		}
	}
	if effectivePort == 0 {
		effectivePort = svcDef.DefaultPort
	}
	port := int32(effectivePort)
	externalIP := host.ExternalIP
	req := &pb.BootstrapServiceRequest{
		ServiceToken:   &serviceToken,
		Type:           serviceType,
		Version:        "cli-provisioned",
		Protocol:       "http",
		HealthEndpoint: &healthPath,
		Port:           port,
		AdvertiseHost:  &externalIP,
		ClusterId:      &hostCluster,
		NodeId:         &task.Host,
	}
	if metadata, err := serviceRegistrationMetadata(serviceName, task.Host, hostCluster, manifest, runtimeData, manifestDir); err != nil {
		return fmt.Errorf("resolve metadata: %w", err)
	} else if len(metadata) > 0 {
		req.Metadata = metadata
	}

	if _, err := registrar.BootstrapService(ctx, req); err != nil {
		return err
	}
	fmt.Fprintf(out, "    ✓ Registered service instance: %s/%s (%s:%d)\n", task.Host, serviceType, externalIP, port)
	return nil
}

func registerIngressDesiredStateWithClient(ctx context.Context, out io.Writer, manifest *inventory.Manifest, task *orchestrator.Task, host inventory.Host, registrar ingressDesiredStateRegistrar) error {
	if manifest.RootDomain == "" {
		return nil
	}

	clusterID := manifest.HostCluster(task.Host)
	if clusterID == "" {
		clusterID = manifest.ResolveCluster(task.Type)
	}
	if clusterID == "" {
		return fmt.Errorf("host %s has no resolved cluster", task.Host)
	}

	defaultEmail := strings.TrimSpace(os.Getenv("FROM_EMAIL"))
	if defaultEmail == "" {
		defaultEmail = "info@frameworks.network"
	}

	for _, bundleRootDomain := range ingressWildcardBundleDomains(manifest, clusterID) {
		wildcardBundleID := tlsBundleID("wildcard", bundleRootDomain)
		if _, err := registrar.UpsertTLSBundle(ctx, &pb.TLSBundle{
			BundleId:  wildcardBundleID,
			ClusterId: clusterID,
			Domains:   []string{"*." + bundleRootDomain},
			Issuer:    "navigator",
			Email:     defaultEmail,
		}); err != nil {
			return fmt.Errorf("upsert wildcard bundle: %w", err)
		}
	}

	localSvcs := make(map[string]interface{})
	addLocalProxyRoutes(localSvcs, task.Host, manifest.Services, task.Type)
	addLocalProxyRoutes(localSvcs, task.Host, manifest.Interfaces, task.Type)
	addLocalProxyRoutes(localSvcs, task.Host, manifest.Observability, task.Type)

	if _, ok := localSvcs["foredeck"]; ok {
		if _, err := registrar.UpsertTLSBundle(ctx, &pb.TLSBundle{
			BundleId:  tlsBundleID("apex", manifest.RootDomain),
			ClusterId: clusterID,
			Domains:   []string{manifest.RootDomain, "www." + manifest.RootDomain},
			Issuer:    "navigator",
			Email:     defaultEmail,
		}); err != nil {
			return fmt.Errorf("upsert apex bundle: %w", err)
		}
	}

	for name, rawPort := range localSvcs {
		port, ok := rawPort.(int)
		if !ok || port == 0 {
			continue
		}

		domains, bundleID := autoIngressDomains(name, manifest, clusterID)
		if len(domains) == 0 || bundleID == "" {
			continue
		}

		metadata := map[string]interface{}{}
		switch name {
		case "bridge":
			metadata["websocket_path"] = "/graphql/ws"
		case "chartroom":
			metadata["upgrade_all"] = true
		case "chatwoot":
			metadata["websocket_path"] = "/cable"
		case "foghorn":
			metadata["geo_proxy_headers"] = true
			metadata["geoip_db_path"] = "/var/lib/GeoIP/GeoLite2-City.mmdb"
		}
		siteMetadata, err := structpb.NewStruct(metadata)
		if err != nil {
			return fmt.Errorf("encode ingress metadata: %w", err)
		}

		if _, err := registrar.UpsertIngressSite(ctx, &pb.IngressSite{
			SiteId:      fmt.Sprintf("%s-%s", name, task.Host),
			ClusterId:   clusterID,
			NodeId:      task.Host,
			Domains:     domains,
			TlsBundleId: bundleID,
			Kind:        "reverse_proxy_tcp",
			Upstream:    fmt.Sprintf("localhost:%d", port),
			Metadata:    siteMetadata,
		}); err != nil {
			return fmt.Errorf("upsert ingress site for %s: %w", name, err)
		}
	}

	for bundleID, cfg := range manifest.TLSBundles {
		if cfg.Cluster != "" && cfg.Cluster != clusterID {
			continue
		}
		email := strings.TrimSpace(cfg.Email)
		if email == "" {
			email = defaultEmail
		}
		issuer := strings.TrimSpace(cfg.Issuer)
		if issuer == "" {
			issuer = "navigator"
		}
		var metadata *structpb.Struct
		if len(cfg.Metadata) > 0 {
			var err error
			metadata, err = structpb.NewStruct(stringMapToInterfaceMap(cfg.Metadata))
			if err != nil {
				return fmt.Errorf("encode tls bundle metadata for %s: %w", bundleID, err)
			}
		}
		if _, err := registrar.UpsertTLSBundle(ctx, &pb.TLSBundle{
			BundleId:  bundleID,
			ClusterId: clusterID,
			Domains:   cfg.Domains,
			Issuer:    issuer,
			Email:     email,
			Metadata:  metadata,
		}); err != nil {
			return fmt.Errorf("upsert explicit tls bundle %s: %w", bundleID, err)
		}
	}

	for siteID, cfg := range manifest.IngressSites {
		if cfg.Node != task.Host {
			continue
		}
		siteClusterID := clusterID
		if cfg.Cluster != "" {
			siteClusterID = cfg.Cluster
		}
		var metadata *structpb.Struct
		if len(cfg.Metadata) > 0 {
			var err error
			metadata, err = structpb.NewStruct(stringMapToInterfaceMap(cfg.Metadata))
			if err != nil {
				return fmt.Errorf("encode ingress site metadata for %s: %w", siteID, err)
			}
		}
		if _, err := registrar.UpsertIngressSite(ctx, &pb.IngressSite{
			SiteId:      siteID,
			ClusterId:   siteClusterID,
			NodeId:      cfg.Node,
			Domains:     cfg.Domains,
			TlsBundleId: cfg.TLSBundleID,
			Kind:        cfg.Kind,
			Upstream:    cfg.Upstream,
			Metadata:    metadata,
		}); err != nil {
			return fmt.Errorf("upsert explicit ingress site %s: %w", siteID, err)
		}
	}

	fmt.Fprintf(out, "    ✓ Registered ingress desired state for %s\n", task.Host)
	return nil
}

func stringMapToInterfaceMap(values map[string]string) map[string]interface{} {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func tlsBundleID(kind, rootDomain string) string {
	replacer := strings.NewReplacer(".", "-", "*", "wildcard-", " ", "-")
	return kind + "-" + replacer.Replace(rootDomain)
}

func clusterScopedRootDomain(manifest *inventory.Manifest, clusterID string) string {
	if manifest == nil || manifest.RootDomain == "" || clusterID == "" {
		return ""
	}

	clusterName := ""
	if cfg, ok := manifest.Clusters[clusterID]; ok {
		clusterName = cfg.Name
	}
	clusterSlug := pkgdns.ClusterSlug(clusterID, clusterName)
	if clusterSlug == "" {
		return ""
	}
	return clusterSlug + "." + manifest.RootDomain
}

func publicServiceRootDomain(serviceType string, manifest *inventory.Manifest, clusterID string) string {
	if manifest == nil {
		return ""
	}
	if pkgdns.IsClusterScopedServiceType(serviceType) {
		return clusterScopedRootDomain(manifest, clusterID)
	}
	return strings.TrimSpace(manifest.RootDomain)
}

func ingressWildcardBundleDomains(manifest *inventory.Manifest, clusterID string) []string {
	if manifest == nil || manifest.RootDomain == "" {
		return nil
	}

	domains := []string{manifest.RootDomain}
	if clusterRoot := clusterScopedRootDomain(manifest, clusterID); clusterRoot != "" {
		domains = append(domains, clusterRoot)
	}
	return domains
}

func autoIngressDomains(serviceName string, manifest *inventory.Manifest, clusterID string) ([]string, string) {
	if serviceName == "foredeck" {
		if manifest == nil || manifest.RootDomain == "" {
			return nil, ""
		}
		return []string{manifest.RootDomain, "www." + manifest.RootDomain}, tlsBundleID("apex", manifest.RootDomain)
	}

	serviceType, ok := publicServiceType(serviceName)
	if !ok {
		return nil, ""
	}
	rootDomain := publicServiceRootDomain(serviceType, manifest, clusterID)
	if rootDomain == "" {
		return nil, ""
	}
	fqdn, ok := pkgdns.ServiceFQDN(serviceType, rootDomain)
	if !ok || fqdn == "" {
		return nil, ""
	}
	return []string{fqdn}, tlsBundleID("wildcard", rootDomain)
}

func reconcileFoghornClusterAssignments(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, runtimeData map[string]interface{}) error {
	grpcAddr, ok := runtimeData["quartermaster_grpc_addr"].(string)
	if !ok {
		grpcAddr = ""
	}
	serviceToken, ok := runtimeData["service_token"].(string)
	if !ok {
		serviceToken = ""
	}
	if grpcAddr == "" || serviceToken == "" {
		return fmt.Errorf("missing Quartermaster connection info for foghorn reconciliation")
	}

	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return fmt.Errorf("connect Quartermaster for foghorn reconciliation: %w", err)
	}
	defer client.Close()

	var lastErr error
	for attempt := 1; attempt <= 6; attempt++ {
		lastErr = reconcileFoghornClusterAssignmentsWithClient(ctx, cmd.OutOrStdout(), manifest, client)
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

func reconcileFoghornClusterAssignmentsWithClient(ctx context.Context, out io.Writer, manifest *inventory.Manifest, assigner foghornClusterAssigner) error {
	fmt.Fprintln(out, "  Reconciling Foghorn cluster assignments...")

	for _, clusterID := range manifest.AllClusterIDs() {
		assignCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := assigner.AssignFoghornToCluster(assignCtx, &pb.AssignFoghornToClusterRequest{
			ClusterId: clusterID,
			Count:     1,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("assign foghorn to cluster %s: %w", clusterID, err)
		}

		fmt.Fprintf(out, "    ✓ Foghorn assigned to cluster %s\n", clusterID)
	}

	return nil
}

// buildTaskConfig creates a ServiceConfig for a task
func buildTaskConfig(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]interface{}, force bool, manifestDir string) (provisioner.ServiceConfig, error) {
	config := provisioner.ServiceConfig{
		Mode:     "docker",
		Version:  "stable",
		Port:     provisioner.ServicePorts[task.Type],
		Metadata: make(map[string]interface{}),
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

	// Copy runtime data
	for k, v := range runtimeData {
		config.Metadata[k] = v
	}

	// Use base service name for manifest lookups (handles "bridge@host" → "bridge")
	baseName := task.ServiceID

	if manifest != nil {
		// Service overrides
		if svc, ok := manifest.Services[baseName]; ok {
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
		if iface, ok := manifest.Interfaces[baseName]; ok {
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
		if obs, ok := manifest.Observability[baseName]; ok {
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
					databases := make([]map[string]string, 0, len(manifest.Infrastructure.Postgres.Databases))
					for _, db := range manifest.Infrastructure.Postgres.Databases {
						databases = append(databases, map[string]string{
							"name":  db.Name,
							"owner": db.Owner,
						})
					}
					config.Metadata["databases"] = databases
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
				if manifest.Infrastructure.Kafka.Mode != "" {
					config.Mode = manifest.Infrastructure.Kafka.Mode
				}
				if manifest.Infrastructure.Kafka.Version != "" {
					config.Version = manifest.Infrastructure.Kafka.Version
				}
				if task.InstanceID != "" {
					if brokerID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["broker_id"] = brokerID
					}
				}
				config.Metadata["cluster_id"] = manifest.Infrastructure.Kafka.ClusterID

				if len(manifest.Infrastructure.Kafka.Controllers) > 0 {
					// Dedicated mode: broker-only
					config.Metadata["role"] = "broker"
					config.Metadata["bootstrap_servers"] = buildBootstrapServers(manifest)
				} else {
					// Combined mode
					config.Metadata["controller_quorum_voters"] = buildControllerQuorum(manifest)
					controllerPort := manifest.Infrastructure.Kafka.ControllerPort
					if controllerPort == 0 {
						controllerPort = 9093
					}
					config.Metadata["controller_port"] = controllerPort
				}

				if len(manifest.Infrastructure.Kafka.Topics) > 0 {
					config.Metadata["topics"] = kafkaTopicsToMetadata(manifest.Infrastructure.Kafka.Topics)
				}
				brokerCount := len(manifest.Infrastructure.Kafka.Brokers)
				if brokerCount > 0 {
					config.Metadata["broker_count"] = brokerCount
				}
				if manifest.Infrastructure.Kafka.DeleteTopicEnable != nil {
					config.Metadata["delete_topic_enable"] = *manifest.Infrastructure.Kafka.DeleteTopicEnable
				}
				if manifest.Infrastructure.Kafka.MinInSyncReplicas > 0 {
					config.Metadata["min_insync_replicas"] = manifest.Infrastructure.Kafka.MinInSyncReplicas
				}
				if manifest.Infrastructure.Kafka.OffsetsTopicReplicationFactor > 0 {
					config.Metadata["offsets_topic_replication_factor"] = manifest.Infrastructure.Kafka.OffsetsTopicReplicationFactor
				}
				if manifest.Infrastructure.Kafka.TransactionStateLogReplicationFactor > 0 {
					config.Metadata["transaction_state_log_replication_factor"] = manifest.Infrastructure.Kafka.TransactionStateLogReplicationFactor
				}
				if manifest.Infrastructure.Kafka.TransactionStateLogMinISR > 0 {
					config.Metadata["transaction_state_log_min_isr"] = manifest.Infrastructure.Kafka.TransactionStateLogMinISR
				}
			}
		case "kafka-controller":
			if manifest.Infrastructure.Kafka != nil {
				if manifest.Infrastructure.Kafka.Mode != "" {
					config.Mode = manifest.Infrastructure.Kafka.Mode
				}
				if manifest.Infrastructure.Kafka.Version != "" {
					config.Version = manifest.Infrastructure.Kafka.Version
				}
				config.Metadata["role"] = "controller"
				config.Metadata["cluster_id"] = manifest.Infrastructure.Kafka.ClusterID
				config.Metadata["bootstrap_servers"] = buildBootstrapServers(manifest)
				config.Metadata["initial_controllers"] = buildInitialControllers(manifest)
				if task.InstanceID != "" {
					if ctrlID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["broker_id"] = ctrlID
						// Look up port from manifest controller with matching ID
						for _, ctrl := range manifest.Infrastructure.Kafka.Controllers {
							if ctrl.ID == ctrlID {
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
				if inst := resolveRedisInstanceByID(task.InstanceID, manifest); inst != nil {
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
					config.Metadata["instance_name"] = inst.Name
					if inst.Password != "" {
						config.Metadata["password"] = inst.Password
					}
					for k, v := range inst.Config {
						config.Metadata["redis_"+k] = v
					}
				}
			}
		case "yugabyte":
			if pg := manifest.Infrastructure.Postgres; pg != nil {
				config.Port = pg.EffectivePort()
				config.Version = pg.Version
				config.Metadata["master_addresses"] = pg.MasterAddresses(manifest.Hosts)
				config.Metadata["replication_factor"] = pg.EffectiveReplicationFactor()
				if task.InstanceID != "" {
					if nodeID, err := strconv.Atoi(task.InstanceID); err == nil {
						config.Metadata["node_id"] = nodeID
					}
				}
				if len(pg.Databases) > 0 {
					databases := make([]map[string]string, 0, len(pg.Databases))
					for _, db := range pg.Databases {
						databases = append(databases, map[string]string{
							"name":  db.Name,
							"owner": db.Owner,
						})
					}
					config.Metadata["databases"] = databases
				}
			}
		}
	}

	// Override for infrastructure (Redis uses manifest mode, not forced native)
	if task.Phase == orchestrator.PhaseInfrastructure && task.Type != "zookeeper" && task.Type != "redis" {
		config.Mode = "native"
		// Keep manifest-specified version for yugabyte, kafka, kafka-controller; default to "latest" for others
		keepVersion := task.Type == "yugabyte" || task.Type == "kafka" || task.Type == "kafka-controller"
		if !keepVersion || config.Version == "" {
			config.Version = "latest"
		}
	}

	// Native override for Privateer + inject mesh node identity
	if task.Type == "privateer" {
		config.Mode = "native"
		config.Metadata["mesh_node_name"] = task.Host
		// Derive node type from the host's roles (default: core)
		nodeType := infra.NodeTypeCore
		if hostInfo, ok := manifest.GetHost(task.Host); ok {
			for _, role := range hostInfo.Roles {
				if role == infra.NodeTypeEdge {
					nodeType = infra.NodeTypeEdge
					break
				}
			}
		}
		config.Metadata["mesh_node_type"] = nodeType
		// Resolve per-cluster enrollment token for this specific privateer instance
		if tokens, ok := runtimeData["enrollment_tokens"].(map[string]string); ok && task.ClusterID != "" {
			if token, ok := tokens[task.ClusterID]; ok {
				config.Metadata["enrollment_token"] = token
			}
		}
		if tokens, ok := runtimeData["cert_issue_tokens"].(map[string]string); ok && task.Host != "" {
			if token, ok := tokens[task.Host]; ok {
				config.Metadata["cert_issue_token"] = token
			}
		}
		if services := internalGRPCLeafServicesForHost(manifest, task.Host); len(services) > 0 {
			config.Metadata["expected_internal_grpc_services"] = services
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
		localSvcs := make(map[string]interface{})
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Services, task.Type)
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Interfaces, task.Type)
		addLocalProxyRoutes(localSvcs, task.Host, manifest.Observability, task.Type)
		if len(localSvcs) > 0 {
			config.Metadata["local_services"] = localSvcs
		}
		if routes := buildExtraProxyRoutesForHost(manifest, task.Host, clusterID); len(routes) > 0 {
			config.Metadata["extra_proxy_routes"] = routes
		}
		if grpcAddr, ok := runtimeData["quartermaster_grpc_addr"].(string); ok && grpcAddr != "" {
			config.Metadata["quartermaster_http_url"] = quartermasterHTTPURL(grpcAddr)
		}
		config.Metadata["navigator_http_url"] = "http://navigator:18010"
		if serviceToken, ok := runtimeData["service_token"].(string); ok && serviceToken != "" {
			config.Metadata["service_token"] = serviceToken
		}
	}

	// Generate merged env vars for application/interface services.
	// Infrastructure services (postgres, kafka, etc.) manage their own config.
	if task.Phase != orchestrator.PhaseInfrastructure && manifest != nil {
		envVars, err := buildServiceEnvVars(task, manifest, runtimeData, config.EnvFile, manifestDir)
		if err != nil {
			return config, fmt.Errorf("service %s: %w", task.Name, err)
		}
		config.EnvVars = envVars
	}

	return config, nil
}

func containsHost(hosts []string, target string) bool {
	for _, h := range hosts {
		if h == target {
			return true
		}
	}
	return false
}

func ensureProvisionGeoIP(ctx context.Context, out io.Writer, manifest *inventory.Manifest, manifestDir string, pool *ssh.Pool) error {
	if manifest == nil || manifest.GeoIP == nil || !manifest.GeoIP.Enabled {
		return nil
	}

	services := effectiveGeoIPServices(manifest, nil)
	if len(services) == 0 {
		return nil
	}

	source := effectiveGeoIPSource(manifest, "")
	filePath := effectiveGeoIPFilePath(manifest, "", manifestDir)
	licenseKey := effectiveGeoIPLicenseKey(manifest, "")
	remotePath := effectiveGeoIPRemotePath(manifest, "")

	mmdbPath, cleanup, err := resolveGeoIPMMDBPath(ctx, manifest, source, filePath, licenseKey)
	if err != nil {
		return fmt.Errorf("geoip provisioning failed: %w", err)
	}
	defer cleanup()

	if _, err := uploadGeoIPToHosts(ctx, manifest, pool, mmdbPath, remotePath, services, false, out); err != nil {
		return fmt.Errorf("geoip provisioning failed: %w", err)
	}

	return nil
}

func buildVMAgentScrapeTargets(manifest *inventory.Manifest, hostName string) []map[string]interface{} {
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

	result := make([]map[string]interface{}, 0, len(targets))
	for _, tgt := range targets {
		result = append(result, map[string]interface{}{
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
	"bridge":    {},
	"chandler":  {},
	"chartroom": {},
	"chatwoot":  {},
	"foghorn":   {},
	"foredeck":  {},
	"logbook":   {},
	"listmonk":  {},
	"steward":   {},
	"vmauth":    {},
}

func buildExtraProxyRoutesForHost(manifest *inventory.Manifest, hostName, clusterID string) []map[string]interface{} {
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
	return []map[string]interface{}{
		{
			"name":         "telemetry",
			"server_names": []string{fqdn},
			"upstream":     fmt.Sprintf("127.0.0.1:%d", port),
		},
	}
}

func addLocalProxyRoutes(routes map[string]interface{}, hostName string, services map[string]inventory.ServiceConfig, skipName string) {
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
		host, ok := manifest.GetHost(node.Host)
		address := node.Host
		if ok && host.ExternalIP != "" {
			address = host.ExternalIP
		}
		servers = append(servers, fmt.Sprintf("server.%d=%s:2888:3888", node.ID, address))
	}

	return &zookeeperNodeConfig{
		ServerID: targetNode.ID,
		Port:     targetNode.Port,
		Servers:  servers,
	}
}

func resolveRedisInstanceByID(instanceID string, manifest *inventory.Manifest) *inventory.RedisInstance {
	if manifest.Infrastructure.Redis == nil || instanceID == "" {
		return nil
	}
	for i := range manifest.Infrastructure.Redis.Instances {
		if manifest.Infrastructure.Redis.Instances[i].Name == instanceID {
			return &manifest.Infrastructure.Redis.Instances[i]
		}
	}
	return nil
}

func buildControllerQuorum(manifest *inventory.Manifest) string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return ""
	}
	port := manifest.Infrastructure.Kafka.ControllerPort
	if port == 0 {
		port = 9093
	}
	voters := make([]string, 0, len(manifest.Infrastructure.Kafka.Brokers))
	for _, b := range manifest.Infrastructure.Kafka.Brokers {
		host := b.Host
		if hostInfo, ok := manifest.Hosts[b.Host]; ok && hostInfo.ExternalIP != "" {
			host = hostInfo.ExternalIP
		}
		voters = append(voters, fmt.Sprintf("%d@%s:%d", b.ID, host, port))
	}
	return strings.Join(voters, ",")
}

func buildBootstrapServers(manifest *inventory.Manifest) string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return ""
	}
	servers := make([]string, 0, len(manifest.Infrastructure.Kafka.Controllers))
	for _, ctrl := range manifest.Infrastructure.Kafka.Controllers {
		host := ctrl.Host
		if hostInfo, ok := manifest.Hosts[ctrl.Host]; ok && hostInfo.ExternalIP != "" {
			host = hostInfo.ExternalIP
		}
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		servers = append(servers, fmt.Sprintf("%s:%d", host, port))
	}
	return strings.Join(servers, ",")
}

func buildInitialControllers(manifest *inventory.Manifest) string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return ""
	}
	parts := make([]string, 0, len(manifest.Infrastructure.Kafka.Controllers))
	for _, ctrl := range manifest.Infrastructure.Kafka.Controllers {
		host := ctrl.Host
		if hostInfo, ok := manifest.Hosts[ctrl.Host]; ok && hostInfo.ExternalIP != "" {
			host = hostInfo.ExternalIP
		}
		port := ctrl.Port
		if port == 0 {
			port = 9093
		}
		parts = append(parts, fmt.Sprintf("%d@%s:%d:%s", ctrl.ID, host, port, ctrl.DirID))
	}
	return strings.Join(parts, ",")
}

func kafkaTopicsToMetadata(topics []inventory.KafkaTopic) []map[string]interface{} {
	metadata := make([]map[string]interface{}, 0, len(topics))
	for _, topic := range topics {
		metadata = append(metadata, map[string]interface{}{
			"name":               topic.Name,
			"partitions":         topic.Partitions,
			"replication_factor": topic.ReplicationFactor,
			"config":             topic.Config,
		})
	}
	return metadata
}

// loadInfraCredentials reads the manifest env files and extracts database credentials
// needed by infrastructure Initialize/Configure steps.
func loadInfraCredentials(manifest *inventory.Manifest, manifestDir string) map[string]interface{} {
	result := make(map[string]interface{})
	envFiles := manifest.SharedEnvFiles()
	if len(envFiles) == 0 {
		return result
	}

	env := make(map[string]string)
	for _, envFile := range envFiles {
		envPath := envFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		if err := loadEnvFile(envPath, env); err != nil {
			return result
		}
	}

	// Map env vars to metadata keys used by provisioners
	if v := env["DATABASE_USER"]; v != "" {
		result["postgres_user"] = v
	} else {
		result["postgres_user"] = "frameworks"
	}
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

// rollbackProvisionedTasks stops previously provisioned services in reverse order
func rollbackProvisionedTasks(ctx context.Context, cmd *cobra.Command, pool *ssh.Pool, tasks []provisionedTask) {
	if len(tasks) == 0 {
		return
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Stopping %d previously provisioned services...\n", len(tasks))

	// Rollback in reverse order (most recent first)
	for i := len(tasks) - 1; i >= 0; i-- {
		t := tasks[i]
		fmt.Fprintf(cmd.OutOrStdout(), "    Stopping %s on %s...\n", t.task.Name, t.task.Host)

		prov, err := provisioner.GetProvisioner(t.task.Type, pool)
		if err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "      Warning: could not get provisioner: %v\n", err)
			continue
		}

		if err := prov.Cleanup(ctx, t.host, t.config); err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "      Warning: cleanup failed: %v\n", err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "      ✓ Stopped\n")
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "  Rollback complete. Cluster may be in inconsistent state.")
	fmt.Fprintln(cmd.OutOrStdout(), "  Fix the issue and re-run provisioning.")
}

// provisionTask provisions a single task
func provisionTask(ctx context.Context, task *orchestrator.Task, host inventory.Host, pool *ssh.Pool, manifest *inventory.Manifest, force, ignoreValidation bool, runtimeData map[string]interface{}, manifestDir string) (*taskProvisionOutcome, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Get provisioner from registry
	prov, err := provisioner.GetProvisioner(task.Type, pool)
	if err != nil {
		return nil, fmt.Errorf("failed to get provisioner: %w", err)
	}

	beforeState, err := prov.Detect(ctx, host)
	if err != nil {
		beforeState = nil
	}
	if task.Type == "privateer" && (force || !serviceRunning(beforeState)) {
		if err = ensurePrivateerEnrollmentToken(ctx, manifest, runtimeData, task.ClusterID); err != nil {
			return nil, err
		}
		if err = ensurePrivateerCertIssueToken(ctx, manifest, runtimeData, task.ClusterID, task.Host); err != nil {
			return nil, err
		}
	}

	config, err := buildTaskConfig(task, manifest, runtimeData, force, manifestDir)
	if err != nil {
		return nil, err
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
			fmt.Printf("  ✗ %s: missing required config:\n", task.Name)
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

	// Provision
	if err := prov.Provision(ctx, host, config); err != nil {
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

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Validate
	if err := prov.Validate(ctx, host, config); err != nil {
		if ignoreValidation {
			fmt.Printf("    Warning: validation failed (ignored due to --ignore-validation): %v\n", err)
		} else {
			return nil, fmt.Errorf("validation failed for %s: %w (use --ignore-validation to continue anyway)", task.Name, err)
		}
	}

	// Infrastructure tasks: run Initialize + Configure after Provision/Validate.
	// Load credentials from manifest env files so Initialize can create app users.
	if task.Phase == orchestrator.PhaseInfrastructure {
		infraCreds := loadInfraCredentials(manifest, manifestDir)
		for k, v := range infraCreds {
			if config.Metadata == nil {
				config.Metadata = make(map[string]interface{})
			}
			config.Metadata[k] = v
		}

		if err := prov.Initialize(ctx, host, config); err != nil {
			return nil, fmt.Errorf("initialization failed for %s: %w", task.Name, err)
		}

		// Configure deploys auth credentials (e.g. ClickHouse users.xml)
		type configurer interface {
			Configure(ctx context.Context, host inventory.Host, config provisioner.ServiceConfig) error
		}
		if c, ok := prov.(configurer); ok {
			if err := c.Configure(ctx, host, config); err != nil {
				return nil, fmt.Errorf("configuration failed for %s: %w", task.Name, err)
			}
		}
	}

	afterState, err := prov.Detect(ctx, host)
	if err != nil {
		afterState = nil
	}

	return &taskProvisionOutcome{
		config:            config,
		previouslyRunning: serviceRunning(beforeState),
		running:           serviceRunning(afterState),
	}, nil
}

// bootstrapResult holds the output of the cluster bootstrap process
type bootstrapResult struct {
	SystemTenantID string
	ServiceToken   string
	QMGRPCAddr     string
}

// runBootstrap connects to Quartermaster and generates infrastructure tokens
func runBootstrap(ctx context.Context, manifest *inventory.Manifest, manifestDir string) (*bootstrapResult, error) {
	serviceToken, grpcAddr, err := resolveQuartermasterRuntimeData(manifest)
	if err != nil {
		return nil, err
	}
	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	defer client.Close()

	// 1. Ensure "FrameWorks" System Tenant Exists
	var systemTenantID string
	tenantsResp, err := client.ListTenants(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get tenants from Quartermaster: %w", err)
	}

	for _, t := range tenantsResp.Tenants {
		if t.Name == "FrameWorks" {
			systemTenantID = t.Id
			break
		}
	}

	if systemTenantID == "" {
		fmt.Printf("  Creating 'FrameWorks' System Tenant...\n")
		createTenantReq := &pb.CreateTenantRequest{
			Name:           "FrameWorks",
			DeploymentTier: "global",
			PrimaryColor:   "#6366f1",
			SecondaryColor: "#f59e0b",
		}
		createTenantResp, errCreate := client.CreateTenant(ctx, createTenantReq)
		if errCreate != nil {
			return nil, fmt.Errorf("failed to create 'FrameWorks' System Tenant: %w", errCreate)
		}
		systemTenantID = createTenantResp.Tenant.Id
		fmt.Printf("    ✓ Created System Tenant with ID: %s\n", systemTenantID)
	} else {
		fmt.Printf("  ✓ 'FrameWorks' System Tenant already exists: %s\n", systemTenantID)
	}

	// 2. Register all clusters
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
		return nil, fmt.Errorf("quartermaster host not found in manifest")
	}

	clusterIDs := manifest.AllClusterIDs()
	baseURL := desiredClusterBaseURL(manifest, host, qmSvc)

	for _, clusterID := range clusterIDs {
		clusterName := fmt.Sprintf("FrameWorks %s %s Cluster", manifest.Type, manifest.Profile)
		clusterType := infra.ClusterTypeCentral
		cc, hasClusterConfig := manifest.Clusters[clusterID]
		if hasClusterConfig {
			clusterName = cc.Name
			clusterType = cc.Type
		} else if manifest.Type == infra.ClusterTypeEdge {
			clusterType = infra.ClusterTypeEdge
		}
		if !infra.IsValidClusterType(clusterType) {
			return nil, fmt.Errorf("cluster %q has unsupported cluster type %q (allowed: %s)", clusterID, clusterType, strings.Join(infra.ClusterTypeValues(), ", "))
		}

		// Resolve manifest bootstrap metadata
		var ownerTenantID *string
		isPlatformOfficial := false
		isDefaultCluster := false
		if hasClusterConfig {
			isPlatformOfficial = cc.PlatformOfficial
			isDefaultCluster = cc.Default
			if cc.OwnerTenant == "frameworks" {
				ownerTenantID = &systemTenantID
			} else if cc.OwnerTenant != "" {
				ownerTenantID = &cc.OwnerTenant
			}
		}

		clusterResp, err := client.GetCluster(ctx, clusterID)
		if err != nil && status.Code(err) == codes.NotFound {
			fmt.Printf("  Registering Cluster '%s'...\n", clusterID)
			createReq := &pb.CreateClusterRequest{
				ClusterId:          clusterID,
				ClusterName:        clusterName,
				ClusterType:        clusterType,
				BaseUrl:            baseURL,
				IsPlatformOfficial: &isPlatformOfficial,
				IsDefaultCluster:   &isDefaultCluster,
			}
			if ownerTenantID != nil {
				createReq.OwnerTenantId = ownerTenantID
			}
			_, err = client.CreateCluster(ctx, createReq)
			if err != nil {
				return nil, fmt.Errorf("failed to register cluster '%s': %w", clusterID, err)
			}
			fmt.Printf("    ✓ Registered Cluster: %s\n", clusterID)
		} else if err != nil {
			return nil, fmt.Errorf("failed to check cluster '%s': %w", clusterID, err)
		} else {
			updateReq := &pb.UpdateClusterRequest{ClusterId: clusterID}
			needsUpdate := false
			if cluster := clusterResp.GetCluster(); cluster != nil {
				if strings.TrimSpace(cluster.GetClusterName()) != clusterName {
					updateReq.ClusterName = &clusterName
					needsUpdate = true
				}
				if strings.TrimSpace(cluster.GetBaseUrl()) != baseURL {
					updateReq.BaseUrl = &baseURL
					needsUpdate = true
				}
				if cluster.GetIsPlatformOfficial() != isPlatformOfficial {
					updateReq.IsPlatformOfficial = &isPlatformOfficial
					needsUpdate = true
				}
				if cluster.GetIsDefaultCluster() != isDefaultCluster {
					updateReq.IsDefaultCluster = &isDefaultCluster
					needsUpdate = true
				}
				currentOwner := ""
				if cluster.OwnerTenantId != nil {
					currentOwner = *cluster.OwnerTenantId
				}
				desiredOwner := ""
				if ownerTenantID != nil {
					desiredOwner = *ownerTenantID
				}
				if currentOwner != desiredOwner {
					updateReq.OwnerTenantId = &desiredOwner
					needsUpdate = true
				}
			}
			if needsUpdate {
				if _, err := client.UpdateCluster(ctx, updateReq); err != nil {
					return nil, fmt.Errorf("failed to update cluster '%s': %w", clusterID, err)
				}
				fmt.Printf("  ✓ Cluster '%s' already registered; updated metadata.\n", clusterID)
			} else {
				fmt.Printf("  ✓ Cluster '%s' already registered.\n", clusterID)
			}
		}
	}

	// 3. Register infrastructure nodes
	// Resolve each host's cluster via explicit host.cluster or single-cluster shortcut.
	fmt.Printf("  Registering infrastructure nodes...\n")
	hostClusters := make(map[string]string, len(manifest.Hosts))
	for hostName := range manifest.Hosts {
		if cluster := manifest.HostCluster(hostName); cluster != "" {
			hostClusters[hostName] = cluster
		} else if len(clusterIDs) > 1 {
			return nil, fmt.Errorf("host '%s' has no explicit cluster assignment in multi-cluster manifest — set 'cluster: <id>' on the host definition", hostName)
		} else {
			hostClusters[hostName] = clusterIDs[0]
		}
	}

	for hostName, hostInfo := range manifest.Hosts {
		nodeType := infra.NodeTypeCore
		for _, role := range hostInfo.Roles {
			if role == infra.NodeTypeEdge {
				nodeType = infra.NodeTypeEdge
				break
			}
		}

		nodeCluster := hostClusters[hostName]

		externalIP := hostInfo.ExternalIP
		_, errCreate := client.CreateNode(ctx, &pb.CreateNodeRequest{
			NodeId:     hostName,
			ClusterId:  nodeCluster,
			NodeName:   hostName,
			NodeType:   nodeType,
			ExternalIp: &externalIP,
		})
		if errCreate != nil {
			return nil, fmt.Errorf("failed to register node %s: %w", hostName, errCreate)
		} else {
			fmt.Printf("    ✓ Registered node: %s (%s) -> cluster %s\n", hostName, nodeType, nodeCluster)
		}
	}

	return &bootstrapResult{
		SystemTenantID: systemTenantID,
		ServiceToken:   serviceToken,
		QMGRPCAddr:     grpcAddr,
	}, nil
}

func desiredClusterBaseURL(manifest *inventory.Manifest, quartermasterHost inventory.Host, quartermasterSvc inventory.ServiceConfig) string {
	if manifest == nil {
		return ""
	}

	if rootDomain := strings.TrimSpace(manifest.RootDomain); rootDomain != "" {
		return rootDomain
	}

	basePort := 18002
	if quartermasterSvc.Port != 0 {
		basePort = quartermasterSvc.Port
	}
	baseURL := fmt.Sprintf("http://%s:%d", quartermasterHost.ExternalIP, basePort)

	if bridgeSvc, ok := manifest.Services["bridge"]; ok && bridgeSvc.Enabled {
		if bridgeSvc.Port != 0 {
			if bridgeHost, ok := manifest.GetHost(bridgeSvc.Host); ok {
				baseURL = fmt.Sprintf("http://%s:%d", bridgeHost.ExternalIP, bridgeSvc.Port)
			}
		}
	}

	return baseURL
}

// selfRegisters returns true for services that create their own
// service_instance via BootstrapService on startup. The CLI should
// not register instances for these to avoid conflicts.
func selfRegisters(serviceName string) bool {
	switch serviceName {
	case "bridge", "foghorn":
		return true
	}
	return false
}

// publicServiceType maps public-facing services to DNS subdomain names.
func publicServiceType(serviceName string) (string, bool) {
	switch serviceName {
	case "bridge":
		return "bridge", true
	case "chandler":
		return "chandler", true
	case "foghorn":
		return "foghorn", true
	case "chartroom":
		return "chartroom", true
	case "foredeck":
		return "foredeck", true
	case "logbook":
		return "logbook", true
	case "steward":
		return "steward", true
	case "listmonk":
		return "listmonk", true
	case "chatwoot":
		return "chatwoot", true
	case "livepeer-gateway":
		return "livepeer-gateway", true
	case "vmauth":
		return "telemetry", true
	default:
		return "", false
	}
}

func serviceRegistrationMetadata(name, hostName, clusterID string, manifest *inventory.Manifest, runtimeData map[string]interface{}, manifestDir string) (map[string]string, error) {
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

	config, err := buildTaskConfig(task, manifest, runtimeData, false, manifestDir)
	if err != nil {
		return nil, err
	}

	hostInfo, ok := manifest.GetHost(hostName)
	if !ok {
		return nil, fmt.Errorf("host %q not found in manifest", hostName)
	}

	metadata := map[string]string{
		servicedefs.LivepeerGatewayMetadataPublicHost: gatewayPublicHost(config.EnvVars, manifest, clusterID),
		servicedefs.LivepeerGatewayMetadataPublicPort: strconv.Itoa(portFromBindAddr(config.EnvVars["http_addr"], 8935)),
		servicedefs.LivepeerGatewayMetadataAdminHost:  hostInfo.ExternalIP,
		servicedefs.LivepeerGatewayMetadataAdminPort:  strconv.Itoa(portFromBindAddr(config.EnvVars["cli_addr"], 7935)),
	}
	if walletAddr := firstNonEmptyEnv(config.EnvVars, "eth_acct_addr", "LIVEPEER_ETH_ACCT_ADDR"); walletAddr != "" {
		metadata[servicedefs.LivepeerGatewayMetadataWalletAddress] = walletAddr
	}

	return metadata, nil
}

// buildServiceEnvVars generates merged environment variables for a service.
// Merge order (later wins): auto-generated → shared env_files → per-service env_file → inline config.
func buildServiceEnvVars(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]interface{}, perServiceEnvFile string, manifestDir string) (map[string]string, error) {
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
	}

	if kafka := manifest.Infrastructure.Kafka; kafka != nil && kafka.Enabled {
		var brokers []string
		for _, b := range kafka.Brokers {
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
		// Kafka cluster ID for consumer group prefixing (required by signalman, decklog, periscope-ingest)
		if task.ClusterID != "" {
			env["KAFKA_CLUSTER_ID"] = task.ClusterID
		} else if ids := manifest.AllClusterIDs(); len(ids) > 0 {
			env["KAFKA_CLUSTER_ID"] = ids[0]
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
		for _, inst := range redis.Instances {
			if rHost := manifestMeshHostname(manifest, inst.Host); rHost != "" {
				port := inst.Port
				if port == 0 {
					port = 6379
				}
				// REDIS_{NAME}_ADDR for each named instance
				key := fmt.Sprintf("REDIS_%s_ADDR", strings.ToUpper(inst.Name))
				env[key] = fmt.Sprintf("%s:%d", rHost, port)
			}
		}
	}

	// Backend dependencies use mesh-reachable DNS names (resolved by Privateer after mesh is up).
	// Public/external access is handled separately by service registration and edge provisioning.
	for _, grpc := range servicedefs.GRPCServices() {
		svc, ok := manifest.Services[grpc.ServiceID]
		if !ok || !svc.Enabled {
			continue
		}
		port := grpc.Port
		if svc.GRPCPort != 0 {
			port = svc.GRPCPort
		}
		env[grpc.EnvKey] = fmt.Sprintf("%s.internal:%d", grpc.ServiceID, port)
	}

	// Service-specific required env vars
	baseName := task.ServiceID
	if baseName == "foghorn" {
		env["FOGHORN_CONTROL_BIND_ADDR"] = ":18019"
		// Wire REDIS_URL from the foghorn Redis instance for HA state sync
		if addr := env["REDIS_FOGHORN_ADDR"]; addr != "" {
			env["REDIS_URL"] = fmt.Sprintf("redis://%s", addr)
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
		env["NAVIGATOR_PORT"] = "18010"
		env["NAVIGATOR_GRPC_PORT"] = "18011"
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
	if usesInternalGRPCLeaf(task.ServiceID) {
		serviceName := task.ServiceID
		env["GRPC_TLS_CERT_PATH"] = fmt.Sprintf("/etc/frameworks/pki/services/%s/tls.crt", serviceName)
		env["GRPC_TLS_KEY_PATH"] = fmt.Sprintf("/etc/frameworks/pki/services/%s/tls.key", serviceName)
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

	// 2. Shared env files from manifest root
	for _, envFile := range manifest.SharedEnvFiles() {
		envPath := envFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		if err := loadEnvFile(envPath, env); err != nil {
			return nil, fmt.Errorf("manifest env_files: %w", err)
		}
	}

	// 3. Per-service env_file override
	if perServiceEnvFile != "" {
		envPath := perServiceEnvFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		if err := loadEnvFile(envPath, env); err != nil {
			return nil, fmt.Errorf("service env_file: %w", err)
		}
	}

	// 4. Inline config map from manifest service definition
	if svc, ok := manifest.Services[baseName]; ok {
		for k, v := range svc.Config {
			env[k] = v
		}
	}
	if iface, ok := manifest.Interfaces[baseName]; ok {
		for k, v := range iface.Config {
			env[k] = v
		}
	}
	if obs, ok := manifest.Observability[baseName]; ok {
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

	if manifest.GeoIP != nil && manifest.GeoIP.Enabled && (baseName == "foghorn" || baseName == "quartermaster") {
		if env["GEOIP_MMDB_PATH"] == "" {
			env["GEOIP_MMDB_PATH"] = effectiveGeoIPRemotePath(manifest, "")
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

	applyProductionRuntimeDefaults(manifest, baseName, env)
	if err := validateProductionServiceEnv(manifest, baseName, env); err != nil {
		return nil, err
	}

	normalizeServiceEnvVars(baseName, env)
	if baseName == "livepeer-gateway" {
		applyDefaultLivepeerGatewayHost(env, manifest, task.ClusterID)
	}

	// 5. Auto-generate missing secrets (SERVICE_TOKEN, JWT_SECRET, etc.)
	if _, err := credentials.GenerateIfMissing(env); err != nil {
		return nil, fmt.Errorf("auto-generate secrets: %w", err)
	}

	// 6. Derive COOKIE_DOMAIN from manifest root_domain
	if manifest.RootDomain != "" && env["COOKIE_DOMAIN"] == "" {
		env["COOKIE_DOMAIN"] = manifest.RootDomain
	}
	if manifest.RootDomain != "" && env["BRAND_DOMAIN"] == "" {
		env["BRAND_DOMAIN"] = manifest.RootDomain
	}
	if env["DATABASE_USER"] == "" {
		env["DATABASE_USER"] = "frameworks"
	}

	// Construct DATABASE_URL from merged credentials (operator may have set
	// DATABASE_USER / DATABASE_PASSWORD in their env_file).
	// Skip if operator explicitly provided DATABASE_URL.
	if env["DATABASE_HOST"] != "" && env["DATABASE_URL"] == "" {
		dbUser := env["DATABASE_USER"]
		if dbUser == "" {
			dbUser = "frameworks"
		}
		dbPass := env["DATABASE_PASSWORD"]
		dbHost := env["DATABASE_HOST"]
		dbPort := env["DATABASE_PORT"]
		if dbPort == "" {
			dbPort = "5432"
		}
		userInfo := dbUser
		if dbPass != "" {
			userInfo = dbUser + ":" + dbPass
		}
		env["DATABASE_URL"] = fmt.Sprintf("postgres://%s@%s:%s/postgres?sslmode=disable", userInfo, dbHost, dbPort)
	}

	return env, nil
}

func applyProductionRuntimeDefaults(manifest *inventory.Manifest, serviceID string, env map[string]string) {
	if manifest == nil || !strings.EqualFold(strings.TrimSpace(manifest.Profile), "production") {
		return
	}

	env["BUILD_ENV"] = "production"
	if strings.TrimSpace(env["GIN_MODE"]) == "" || strings.EqualFold(strings.TrimSpace(env["GIN_MODE"]), "debug") {
		env["GIN_MODE"] = "release"
	}

	env["GRPC_ALLOW_INSECURE"] = "false"
}

func validateProductionServiceEnv(manifest *inventory.Manifest, serviceID string, env map[string]string) error {
	if manifest == nil || !strings.EqualFold(strings.TrimSpace(manifest.Profile), "production") {
		return nil
	}

	if serviceID != "navigator" {
		return nil
	}

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
		"service %s: production deploy requires managed internal CA env vars via either files (%s) or base64 PEM envs (%s)",
		serviceID,
		strings.Join(fileKeys, ", "),
		strings.Join(b64Keys, ", "),
	)
}

func normalizeServiceEnvVars(serviceID string, env map[string]string) {
	switch serviceID {
	case "livepeer-gateway":
		normalizeLivepeerEnvVars(env)
		setEnvIfEmpty(env, "auth_webhook_url", "LIVEPEER_AUTH_WEBHOOK_URL")
		if strings.TrimSpace(env["auth_webhook_url"]) == "" {
			env["auth_webhook_url"] = defaultLivepeerGatewayAuthWebhookURL
		}
	case "livepeer-signer":
		normalizeLivepeerEnvVars(env)
	}
}

func manifestTaskRegion(manifest *inventory.Manifest, task *orchestrator.Task) string {
	if manifest == nil || task == nil {
		return ""
	}
	if task.Host != "" {
		if host, ok := manifest.Hosts[task.Host]; ok {
			if region := strings.TrimSpace(host.Labels["region"]); region != "" {
				return region
			}
			clusterID := strings.TrimSpace(host.Cluster)
			if clusterID != "" {
				if cluster, ok := manifest.Clusters[clusterID]; ok {
					if region := strings.TrimSpace(cluster.Region); region != "" {
						return region
					}
				}
			}
		}
	}
	if task.ClusterID != "" {
		if cluster, ok := manifest.Clusters[task.ClusterID]; ok {
			return strings.TrimSpace(cluster.Region)
		}
	}
	return ""
}

const defaultLivepeerGatewayAuthWebhookURL = "http://foghorn.internal:18008/webhooks/livepeer/auth"

func normalizeLivepeerEnvVars(env map[string]string) {
	setEnvIfEmpty(env, "eth_url", livepeerRPCEnvKeys(env)...)
	setEnvIfEmpty(env, "eth_acct_addr", "LIVEPEER_ETH_ACCT_ADDR")
	setEnvIfEmpty(env, "orch_webhook_url", "LIVEPEER_ORCH_WEBHOOK_URL")
	setEnvIfEmpty(env, "remote_signer_url", "LIVEPEER_REMOTE_SIGNER_URL")
	setEnvIfEmpty(env, "auth_webhook_url", "LIVEPEER_AUTH_WEBHOOK_URL")
	setEnvIfEmpty(env, "gateway_host", "LIVEPEER_GATEWAY_HOST")
}

func validateGatewayMeshCoverage(manifest *inventory.Manifest) error {
	gatewaySvc, ok := manifest.Services["livepeer-gateway"]
	if !ok || !gatewaySvc.Enabled {
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

func applyDefaultLivepeerGatewayHost(env map[string]string, manifest *inventory.Manifest, clusterID string) {
	clusterHost := clusterScopedGatewayHost(manifest, clusterID)
	if clusterHost == "" {
		return
	}

	current := strings.TrimSpace(env["gateway_host"])
	if current == "" {
		env["gateway_host"] = clusterHost
		return
	}

	globalHost := globalGatewayHost(manifest.RootDomain)
	if current == globalHost {
		env["gateway_host"] = clusterHost
	}
}

func gatewayPublicHost(env map[string]string, manifest *inventory.Manifest, clusterID string) string {
	if host := strings.TrimSpace(env["gateway_host"]); host != "" {
		return host
	}
	return clusterScopedGatewayHost(manifest, clusterID)
}

func clusterScopedGatewayHost(manifest *inventory.Manifest, clusterID string) string {
	if manifest == nil || manifest.RootDomain == "" || clusterID == "" {
		return ""
	}

	clusterSlug := pkgdns.ClusterSlug(clusterID, manifest.Clusters[clusterID].Name)
	if clusterSlug == "" {
		return ""
	}

	fqdn, ok := pkgdns.ServiceFQDN("livepeer-gateway", clusterSlug+"."+manifest.RootDomain)
	if !ok {
		return ""
	}
	return fqdn
}

func globalGatewayHost(rootDomain string) string {
	if rootDomain == "" {
		return ""
	}
	fqdn, ok := pkgdns.ServiceFQDN("livepeer-gateway", rootDomain)
	if !ok {
		return ""
	}
	return fqdn
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
		target[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return nil
}

// verifyMeshHealth checks that Privateer is running and mesh DNS works on privateer hosts.
// Called as a gate between Privateer provisioning and application service provisioning.
func verifyMeshHealth(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, pool *ssh.Pool, privateerHosts []string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "  Verifying mesh health on %d privateer host(s)...\n", len(privateerHosts))

	base := provisioner.NewBaseProvisioner("mesh-verify", pool)
	var failures []string

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

		// Check mesh DNS can resolve quartermaster
		result, err = base.RunCommand(ctx, hostInfo, "dig @127.0.0.1 quartermaster.internal +short +timeout=3 2>/dev/null")
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: DNS check failed: %v", hostName, err))
			continue
		}
		resolved := strings.TrimSpace(result.Stdout)
		if resolved == "" {
			failures = append(failures, fmt.Sprintf("%s: mesh DNS cannot resolve 'quartermaster'", hostName))
			continue
		}

		fmt.Fprintf(cmd.OutOrStdout(), "      ✓ privateer active, mesh DNS resolves quartermaster → %s\n", resolved)
	}

	if len(failures) > 0 {
		return fmt.Errorf("mesh health check failed on %d host(s):\n  %s", len(failures), strings.Join(failures, "\n  "))
	}

	fmt.Fprintln(cmd.OutOrStdout(), "    ✓ Mesh healthy on all privateer hosts")
	return nil
}

// resolveManifestToRepoPath converts a manifest-relative file path to a
// repo-root-relative path suitable for GitHub API fetch. For example, given
// manifestDir="clusters/production" and relPath="../../secrets/production.env",
// it returns "secrets/production.env".
func resolveManifestToRepoPath(manifestDir, relPath string) (string, error) {
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("absolute path %q is not valid in a repository manifest", relPath)
	}
	resolved := filepath.Clean(filepath.Join(manifestDir, relPath))
	if strings.HasPrefix(resolved, "..") {
		return "", fmt.Errorf("path %q resolves outside repository root (resolved to %q)", relPath, resolved)
	}
	return resolved, nil
}
