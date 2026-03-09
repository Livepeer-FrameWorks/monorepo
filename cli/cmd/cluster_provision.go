package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/credentials"
	"frameworks/cli/pkg/githubapp"
	fwsops "frameworks/cli/pkg/sops"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	infra "frameworks/pkg/models"
	pb "frameworks/pkg/proto"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/servicedefs"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
			return runProvision(cmd, manifestPath, only, dryRun, force, ignoreValidation)
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

	// Validate the manifest parses correctly
	manifest, err := inventory.LoadFromBytes(data)
	if err != nil {
		return fmt.Errorf("failed to parse manifest from %s: %w", repo, err)
	}

	// Write manifest to a temp directory so all fetched files resolve correctly
	tmpDir, err := os.MkdirTemp("", "frameworks-provision-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Collect all referenced env_file paths (top-level + per-service + per-interface)
	filesToFetch := []string{}
	if manifest.EnvFile != "" {
		filesToFetch = append(filesToFetch, manifest.EnvFile)
	}
	for _, svc := range manifest.Services {
		if svc.EnvFile != "" {
			filesToFetch = append(filesToFetch, svc.EnvFile)
		}
	}
	for _, iface := range manifest.Interfaces {
		if iface.EnvFile != "" {
			filesToFetch = append(filesToFetch, iface.EnvFile)
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

		// Decrypt SOPS-encrypted env files transparently
		if fwsops.IsEncrypted(fileData) {
			plain, decErr := fwsops.Decrypt(fileData, ageKeyFile)
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

	// Write fetched manifest to the same temp directory
	tmpManifest := filepath.Join(tmpDir, filepath.Base(manifestFile))
	if err := os.WriteFile(tmpManifest, data, 0o600); err != nil {
		return fmt.Errorf("failed to write temp manifest: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Provisioning cluster from %s/%s\n", repo, manifestFile)
	return runProvision(cmd, tmpManifest, only, dryRun, force, ignoreValidation)
}

// runProvision executes the provision command
func runProvision(cmd *cobra.Command, manifestPath, only string, dryRun, force, ignoreValidation bool) error {
	// Load manifest
	manifest, err := inventory.Load(manifestPath)
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

// executeProvision runs the provisioning tasks
func executeProvision(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, force, ignoreValidation bool, manifestDir string) error {
	// Create SSH pool
	sshPool := ssh.NewPool(30 * time.Second)
	defer sshPool.Close()

	// Track successfully provisioned tasks for rollback
	var completed []provisionedTask

	// Execute each batch sequentially
	runtimeData := make(map[string]interface{})

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

			// Build config for this task
			config, err := buildTaskConfig(task, manifest, runtimeData, force, manifestDir)
			if err != nil {
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return err
			}

			// Provision based on task type
			stopProgress := startTaskProgressLogger(cmd, task, 30*time.Second)
			if err := provisionTask(ctx, task, host, sshPool, manifest, force, ignoreValidation, runtimeData, manifestDir); err != nil {
				stopProgress()
				fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Provisioning failed for %s: %v\n", task.Name, err)
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("failed to provision %s: %w", task.Name, err)
			}
			stopProgress()

			// Track completed task for potential rollback
			completed = append(completed, provisionedTask{task: task, host: host, config: config})

			// Bootstrap Logic: Run after Quartermaster is healthy
			if task.Type == "quartermaster" {
				fmt.Fprintln(cmd.OutOrStdout(), "  Running Cluster Bootstrap (System Tenant)...")
				result, err := runBootstrap(ctx, manifest)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Bootstrap failed: %v\n", err)
					fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("bootstrap failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "    ✓ System Tenant bootstrapped")
				// Store per-cluster tokens and gRPC address for downstream services
				runtimeData["enrollment_tokens"] = result.EnrollmentTokens
				if result.ServiceToken != "" {
					runtimeData["service_token"] = result.ServiceToken
				}
				if result.QMGRPCAddr != "" {
					runtimeData["quartermaster_grpc_addr"] = result.QMGRPCAddr
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s provisioned\n", task.Name)
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

	return nil
}

// batchContainsPrivateer returns true if any task in the batch is a Privateer deployment.
func batchContainsPrivateer(batch []*orchestrator.Task) bool {
	for _, task := range batch {
		name := serviceNameFromTask(task.Name)
		if name == "privateer" {
			return true
		}
	}
	return false
}

// serviceNameFromTask extracts the base service name from a task name.
// Multi-host tasks use "name@host" format; this returns "name".
func serviceNameFromTask(taskName string) string {
	if idx := strings.IndexByte(taskName, '@'); idx != -1 {
		return taskName[:idx]
	}
	return taskName
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
	baseName := serviceNameFromTask(task.Name)

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
			if port, err := resolvePort(baseName, iface); err == nil && port != 0 {
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
				if brokerID, ok := findKafkaBrokerID(task, manifest); ok {
					config.Metadata["broker_id"] = brokerID
				}
				if manifest.Infrastructure.Kafka.ZookeeperConnect != "" {
					config.Metadata["zookeeper_connect"] = manifest.Infrastructure.Kafka.ZookeeperConnect
				} else if zkConnect, ok := buildZookeeperConnect(manifest); ok {
					config.Metadata["zookeeper_connect"] = zkConnect
				}
				if len(manifest.Infrastructure.Kafka.Topics) > 0 {
					config.Metadata["topics"] = kafkaTopicsToMetadata(manifest.Infrastructure.Kafka.Topics)
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
				if nodeConfig := resolveZookeeperNodeConfig(task.Name, manifest); nodeConfig != nil {
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
				if inst := resolveRedisInstance(task.Name, manifest); inst != nil {
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
				// Resolve node ID from task name (yugabyte-node-N)
				if nodeID, ok := resolveYugabyteNodeID(task.Name, pg); ok {
					config.Metadata["node_id"] = nodeID
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
		// Keep manifest-specified version for yugabyte; default to "latest" for others
		if task.Type != "yugabyte" || config.Version == "" {
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
	}

	// Reverse proxy metadata: inject root_domain and colocated services
	if task.Type == "caddy" || task.Type == "nginx" {
		if manifest.RootDomain != "" {
			config.Metadata["root_domain"] = manifest.RootDomain
		}
		localSvcs := make(map[string]interface{})
		for ifaceName, iface := range manifest.Interfaces {
			if !iface.Enabled || ifaceName == task.Type {
				continue
			}
			if iface.Host == task.Host || containsHost(iface.Hosts, task.Host) {
				port := iface.Port
				if port == 0 {
					port = provisioner.ServicePorts[ifaceName]
				}
				localSvcs[ifaceName] = port
			}
		}
		if len(localSvcs) > 0 {
			config.Metadata["local_services"] = localSvcs
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

type zookeeperNodeConfig struct {
	ServerID int
	Port     int
	Servers  []string
}

func resolveZookeeperNodeConfig(taskName string, manifest *inventory.Manifest) *zookeeperNodeConfig {
	if manifest.Infrastructure.Zookeeper == nil {
		return nil
	}

	const prefix = "zookeeper-"
	if !strings.HasPrefix(taskName, prefix) {
		return nil
	}

	id, err := strconv.Atoi(strings.TrimPrefix(taskName, prefix))
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

func resolveRedisInstance(taskName string, manifest *inventory.Manifest) *inventory.RedisInstance {
	if manifest.Infrastructure.Redis == nil {
		return nil
	}
	const prefix = "redis-"
	if !strings.HasPrefix(taskName, prefix) {
		return nil
	}
	name := strings.TrimPrefix(taskName, prefix)
	for i := range manifest.Infrastructure.Redis.Instances {
		if manifest.Infrastructure.Redis.Instances[i].Name == name {
			return &manifest.Infrastructure.Redis.Instances[i]
		}
	}
	return nil
}

func findKafkaBrokerID(task *orchestrator.Task, manifest *inventory.Manifest) (int, bool) {
	if manifest == nil || manifest.Infrastructure.Kafka == nil {
		return 0, false
	}

	// Prefer the task name (kafka-broker-N) since multiple brokers may share a host.
	const prefix = "kafka-broker-"
	if strings.HasPrefix(task.Name, prefix) {
		id, err := strconv.Atoi(strings.TrimPrefix(task.Name, prefix))
		if err == nil {
			return id, true
		}
	}

	var (
		matchedID   int
		matchCount  int
		haveMatched bool
	)
	for _, broker := range manifest.Infrastructure.Kafka.Brokers {
		if broker.Host != task.Host {
			continue
		}
		matchedID = broker.ID
		haveMatched = true
		matchCount++
	}
	if haveMatched && matchCount == 1 {
		return matchedID, true
	}

	// Ambiguous (multiple brokers on same host) or no match.
	return 0, false
}

func buildZookeeperConnect(manifest *inventory.Manifest) (string, bool) {
	if manifest == nil || manifest.Infrastructure.Zookeeper == nil || !manifest.Infrastructure.Zookeeper.Enabled {
		return "", false
	}
	if len(manifest.Infrastructure.Zookeeper.Ensemble) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(manifest.Infrastructure.Zookeeper.Ensemble))
	for _, node := range manifest.Infrastructure.Zookeeper.Ensemble {
		host := node.Host
		if hostInfo, ok := manifest.Hosts[node.Host]; ok && hostInfo.ExternalIP != "" {
			host = hostInfo.ExternalIP
		}
		port := node.Port
		if port == 0 {
			port = 2181
		}
		parts = append(parts, fmt.Sprintf("%s:%d", host, port))
	}
	return strings.Join(parts, ","), true
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

// loadInfraCredentials reads the manifest env_file and extracts database credentials
// needed by infrastructure Initialize/Configure steps.
func loadInfraCredentials(manifest *inventory.Manifest, manifestDir string) map[string]interface{} {
	result := make(map[string]interface{})
	if manifest.EnvFile == "" {
		return result
	}

	envPath := manifest.EnvFile
	if manifestDir != "" && !filepath.IsAbs(envPath) {
		envPath = filepath.Join(manifestDir, envPath)
	}

	env := make(map[string]string)
	if err := loadEnvFile(envPath, env); err != nil {
		return result
	}

	// Map env vars to metadata keys used by provisioners
	if v := env["DATABASE_USER"]; v != "" {
		result["postgres_user"] = v
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

// resolveYugabyteNodeID extracts the node ID from a "yugabyte-node-N" task name
func resolveYugabyteNodeID(taskName string, pg *inventory.PostgresConfig) (int, bool) {
	const prefix = "yugabyte-node-"
	if strings.HasPrefix(taskName, prefix) {
		id, err := strconv.Atoi(strings.TrimPrefix(taskName, prefix))
		if err == nil {
			return id, true
		}
	}
	return 0, false
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
func provisionTask(ctx context.Context, task *orchestrator.Task, host inventory.Host, pool *ssh.Pool, manifest *inventory.Manifest, force, ignoreValidation bool, runtimeData map[string]interface{}, manifestDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Get provisioner from registry
	prov, err := provisioner.GetProvisioner(task.Type, pool)
	if err != nil {
		return fmt.Errorf("failed to get provisioner: %w", err)
	}

	config, err := buildTaskConfig(task, manifest, runtimeData, force, manifestDir)
	if err != nil {
		return err
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
				return fmt.Errorf("%s requires %d missing env var(s) — provide them in env_file or use --ignore-validation to deploy without starting", task.Name, len(missing))
			}
			config.DeferStart = true
			fmt.Printf("  ⏸ %s: deploying without starting (--ignore-validation)\n", task.Name)
		}
	}

	// Provision
	if err := prov.Provision(ctx, host, config); err != nil {
		return err
	}

	// Skip validation for deferred services
	if config.DeferStart {
		fmt.Printf("  ⏸ %s deployed but not started. Add missing config to env_file, then re-run.\n", task.Name)
		return nil
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Validate
	if err := prov.Validate(ctx, host, config); err != nil {
		if ignoreValidation {
			fmt.Printf("    Warning: validation failed (ignored due to --ignore-validation): %v\n", err)
		} else {
			return fmt.Errorf("validation failed for %s: %w (use --ignore-validation to continue anyway)", task.Name, err)
		}
	}

	// Infrastructure tasks: run Initialize + Configure after Provision/Validate.
	// Load credentials from manifest env_file so Initialize can create app users.
	if task.Phase == orchestrator.PhaseInfrastructure {
		infraCreds := loadInfraCredentials(manifest, manifestDir)
		for k, v := range infraCreds {
			if config.Metadata == nil {
				config.Metadata = make(map[string]interface{})
			}
			config.Metadata[k] = v
		}

		if err := prov.Initialize(ctx, host, config); err != nil {
			return fmt.Errorf("initialization failed for %s: %w", task.Name, err)
		}

		// Configure deploys auth credentials (e.g. ClickHouse users.xml)
		type configurer interface {
			Configure(ctx context.Context, host inventory.Host, config provisioner.ServiceConfig) error
		}
		if c, ok := prov.(configurer); ok {
			if err := c.Configure(ctx, host, config); err != nil {
				return fmt.Errorf("configuration failed for %s: %w", task.Name, err)
			}
		}
	}

	return nil
}

// bootstrapResult holds the output of the cluster bootstrap process
type bootstrapResult struct {
	EnrollmentTokens map[string]string // clusterID -> token
	ServiceToken     string
	QMGRPCAddr       string
}

// runBootstrap connects to Quartermaster and generates infrastructure tokens
func runBootstrap(ctx context.Context, manifest *inventory.Manifest) (*bootstrapResult, error) {
	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		if cfg, _, err := fwcfg.Load(); err == nil {
			cliCtx := fwcfg.GetCurrent(cfg)
			cliCtx.Auth = fwcfg.ResolveAuth(cliCtx)
			serviceToken = cliCtx.Auth.ServiceToken
		}
	}
	if serviceToken == "" {
		return nil, fmt.Errorf("service token required for bootstrapping (set SERVICE_TOKEN or run 'frameworks login')")
	}

	var qmHost string
	var qmSvc inventory.ServiceConfig
	for name, svc := range manifest.Services {
		if name == "quartermaster" {
			qmHost = svc.Host
			qmSvc = svc
			if qmHost == "" && len(svc.Hosts) > 0 {
				qmHost = svc.Hosts[0]
			}
			break
		}
	}

	host, ok := manifest.GetHost(qmHost)
	if !ok {
		return nil, fmt.Errorf("quartermaster host not found in manifest")
	}

	// Use gRPC client instead of HTTP
	grpcPort := 19002
	if qmSvc.GRPCPort != 0 {
		grpcPort = qmSvc.GRPCPort
	}
	grpcAddr := fmt.Sprintf("%s:%d", host.ExternalIP, grpcPort)
	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Logger:       logging.NewLogger(),
		ServiceToken: serviceToken,
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
	clusterIDs := manifest.AllClusterIDs()
	basePort := 18002
	if qmSvc.Port != 0 {
		basePort = qmSvc.Port
	}
	baseURL := fmt.Sprintf("http://%s:%d", host.ExternalIP, basePort)
	if bridgeSvc, ok := manifest.Services["bridge"]; ok && bridgeSvc.Enabled {
		if bridgeSvc.Port != 0 {
			if bridgeHost, ok := manifest.GetHost(bridgeSvc.Host); ok {
				baseURL = fmt.Sprintf("http://%s:%d", bridgeHost.ExternalIP, bridgeSvc.Port)
			}
		}
	}

	for _, clusterID := range clusterIDs {
		clusterName := fmt.Sprintf("FrameWorks %s %s Cluster", manifest.Type, manifest.Profile)
		clusterType := infra.ClusterTypeCentral
		if cc, ok := manifest.Clusters[clusterID]; ok {
			clusterName = cc.Name
			clusterType = cc.Type
		} else if manifest.Type == infra.ClusterTypeEdge {
			clusterType = infra.ClusterTypeEdge
		}
		if !infra.IsValidClusterType(clusterType) {
			return nil, fmt.Errorf("cluster %q has unsupported cluster type %q (allowed: %s)", clusterID, clusterType, strings.Join(infra.ClusterTypeValues(), ", "))
		}

		_, err = client.GetCluster(ctx, clusterID)
		if err != nil && status.Code(err) == codes.NotFound {
			fmt.Printf("  Registering Cluster '%s'...\n", clusterID)
			_, err = client.CreateCluster(ctx, &pb.CreateClusterRequest{
				ClusterId:   clusterID,
				ClusterName: clusterName,
				ClusterType: clusterType,
				BaseUrl:     baseURL,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to register cluster '%s': %w", clusterID, err)
			}
			fmt.Printf("    ✓ Registered Cluster: %s\n", clusterID)
		} else if err != nil {
			return nil, fmt.Errorf("failed to check cluster '%s': %w", clusterID, err)
		} else {
			fmt.Printf("  ✓ Cluster '%s' already registered.\n", clusterID)
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

	// 3b. Register service instances for public services on existing host nodes.
	// Infrastructure nodes are already created above (step 3a).
	// Services that self-register (bridge, foghorn) are skipped.
	fmt.Printf("  Registering public service instances...\n")
	registerServiceInstances := func(name string, svc inventory.ServiceConfig) {
		serviceType, ok := publicServiceType(name)
		if !ok {
			return
		}
		if selfRegisters(name) {
			return
		}
		svcDef, hasDef := servicedefs.Lookup(name)
		if !hasDef {
			return
		}
		hosts := svc.Hosts
		if len(hosts) == 0 && svc.Host != "" {
			hosts = []string{svc.Host}
		}
		for _, hostName := range hosts {
			hostInfo, ok := manifest.GetHost(hostName)
			if !ok {
				continue
			}
			hostCluster := manifest.HostCluster(hostName)
			if hostCluster == "" {
				hostCluster = manifest.ResolveCluster(name)
			}
			externalIP := hostInfo.ExternalIP
			healthPath := svcDef.HealthPath
			effectivePort, _ := resolvePort(name, svc)
			if effectivePort == 0 {
				effectivePort = svcDef.DefaultPort
			}
			port := int32(effectivePort)
			_, errBS := client.BootstrapService(ctx, &pb.BootstrapServiceRequest{
				ServiceToken:   &serviceToken,
				Type:           serviceType,
				Version:        "cli-provisioned",
				Protocol:       "http",
				HealthEndpoint: &healthPath,
				Port:           port,
				AdvertiseHost:  &externalIP,
				ClusterId:      &hostCluster,
				NodeId:         &hostName,
			})
			if errBS != nil {
				fmt.Printf("    Warning: failed to register service instance for %s/%s: %v\n", hostName, serviceType, errBS)
			} else {
				fmt.Printf("    ✓ Registered service instance: %s/%s (%s:%d)\n", hostName, serviceType, externalIP, port)
			}
		}
	}
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		registerServiceInstances(name, svc)
	}
	for name, iface := range manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		registerServiceInstances(name, iface)
	}

	// 4. Generate per-cluster enrollment tokens
	enrollmentTokens := make(map[string]string)
	fmt.Printf("  Generating Infrastructure Enrollment Tokens...\n")
	for _, clusterID := range clusterIDs {
		cid := clusterID
		resp, errToken := client.CreateBootstrapToken(ctx, &pb.CreateBootstrapTokenRequest{
			Name:      fmt.Sprintf("Infrastructure Enrollment Token for %s", cid),
			Kind:      "infrastructure_node",
			Ttl:       "720h",
			TenantId:  &systemTenantID,
			ClusterId: &cid,
		})
		if errToken != nil {
			return nil, fmt.Errorf("failed to create bootstrap token for cluster '%s': %w", cid, errToken)
		}
		enrollmentTokens[cid] = resp.Token.Token
		fmt.Printf("    ✓ Generated token for cluster %s: %s\n", cid, resp.Token.Id)
	}

	return &bootstrapResult{
		EnrollmentTokens: enrollmentTokens,
		ServiceToken:     serviceToken,
		QMGRPCAddr:       grpcAddr,
	}, nil
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
	default:
		return "", false
	}
}

// buildServiceEnvVars generates merged environment variables for a service.
// Merge order (later wins): auto-generated → shared env_file → per-service env_file → inline config.
func buildServiceEnvVars(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]interface{}, perServiceEnvFile string, manifestDir string) (map[string]string, error) {
	env := make(map[string]string)

	// 1. Auto-generated infrastructure env vars
	if pg := manifest.Infrastructure.Postgres; pg != nil && pg.Enabled {
		port := pg.EffectivePort()
		var pgIP string
		if pg.IsYugabyte() && len(pg.Nodes) > 0 {
			// Use first node for DATABASE_HOST (services need a single endpoint)
			if h, ok := manifest.GetHost(pg.Nodes[0].Host); ok {
				pgIP = h.ExternalIP
			}
		} else if h, ok := manifest.GetHost(pg.Host); ok {
			pgIP = h.ExternalIP
		}
		if pgIP != "" {
			env["DATABASE_HOST"] = pgIP
			env["DATABASE_PORT"] = strconv.Itoa(port)
		}
	}

	if kafka := manifest.Infrastructure.Kafka; kafka != nil && kafka.Enabled {
		var brokers []string
		for _, b := range kafka.Brokers {
			if bHost, ok := manifest.GetHost(b.Host); ok {
				port := b.Port
				if port == 0 {
					port = 9092
				}
				brokers = append(brokers, fmt.Sprintf("%s:%d", bHost.ExternalIP, port))
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
		if chHost, ok := manifest.GetHost(ch.Host); ok {
			port := ch.Port
			if port == 0 {
				port = 9000
			}
			env["CLICKHOUSE_ADDR"] = fmt.Sprintf("%s:%d", chHost.ExternalIP, port)
			env["CLICKHOUSE_HOST"] = chHost.ExternalIP
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
			if rHost, ok := manifest.GetHost(inst.Host); ok {
				port := inst.Port
				if port == 0 {
					port = 6379
				}
				// REDIS_{NAME}_ADDR for each named instance
				key := fmt.Sprintf("REDIS_%s_ADDR", strings.ToUpper(inst.Name))
				env[key] = fmt.Sprintf("%s:%d", rHost.ExternalIP, port)
			}
		}
	}

	// Service gRPC addresses — use mesh DNS FQDNs (resolved by Privateer after mesh is up).
	// Infrastructure addresses above use external_ip; service-to-service uses mesh DNS.
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
	baseName := serviceNameFromTask(task.Name)
	if baseName == "foghorn" {
		env["FOGHORN_CONTROL_BIND_ADDR"] = ":18019"
		// Wire REDIS_URL from the foghorn Redis instance for HA state sync
		if addr := env["REDIS_FOGHORN_ADDR"]; addr != "" {
			env["REDIS_URL"] = fmt.Sprintf("redis://%s", addr)
		}
		// Chandler host/port for asset management
		if chandler, ok := manifest.Services["chandler"]; ok && chandler.Enabled {
			chHost := chandler.Host
			if chHost == "" && len(chandler.Hosts) > 0 {
				chHost = chandler.Hosts[0]
			}
			if h, ok := manifest.GetHost(chHost); ok {
				chPort := chandler.Port
				if chPort == 0 {
					chPort = 18020
				}
				env["CHANDLER_HOST"] = h.ExternalIP
				env["CHANDLER_PORT"] = strconv.Itoa(chPort)
			}
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
		if manifest.RootDomain != "" && env["NAVIGATOR_ROOT_DOMAIN"] == "" {
			env["NAVIGATOR_ROOT_DOMAIN"] = manifest.RootDomain
		}
		// BRAND_CONTACT_EMAIL comes from env_file (FROM_EMAIL in production.env)
	}

	// Listmonk URL — self-hosted, address from manifest
	if listmonk, ok := manifest.Services["listmonk"]; ok && listmonk.Enabled {
		lmHost := listmonk.Host
		if lmHost == "" && len(listmonk.Hosts) > 0 {
			lmHost = listmonk.Hosts[0]
		}
		if h, ok := manifest.GetHost(lmHost); ok {
			lmPort := listmonk.Port
			if lmPort == 0 {
				lmPort = 9001
			}
			env["LISTMONK_URL"] = fmt.Sprintf("http://%s:%d", h.ExternalIP, lmPort)
		}
	}

	// Chatwoot host/port for deckhand — self-hosted, address from manifest
	if chatwoot, ok := manifest.Services["chatwoot"]; ok && chatwoot.Enabled {
		cwHost := chatwoot.Host
		if cwHost == "" && len(chatwoot.Hosts) > 0 {
			cwHost = chatwoot.Hosts[0]
		}
		if h, ok := manifest.GetHost(cwHost); ok {
			cwPort := chatwoot.Port
			if cwPort == 0 {
				cwPort = 18092
			}
			env["CHATWOOT_HOST"] = h.ExternalIP
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

	// 2. Shared env_file from manifest root
	if manifest.EnvFile != "" {
		envPath := manifest.EnvFile
		if manifestDir != "" && !filepath.IsAbs(envPath) {
			envPath = filepath.Join(manifestDir, envPath)
		}
		if err := loadEnvFile(envPath, env); err != nil {
			return nil, fmt.Errorf("manifest env_file: %w", err)
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

	// 5. Auto-generate missing secrets (SERVICE_TOKEN, JWT_SECRET, etc.)
	if _, err := credentials.GenerateIfMissing(env); err != nil {
		return nil, fmt.Errorf("auto-generate secrets: %w", err)
	}

	// 6. Derive COOKIE_DOMAIN from manifest root_domain
	if manifest.RootDomain != "" && env["COOKIE_DOMAIN"] == "" {
		env["COOKIE_DOMAIN"] = manifest.RootDomain
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
