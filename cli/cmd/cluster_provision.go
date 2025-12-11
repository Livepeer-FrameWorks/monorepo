package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// newClusterProvisionCmd creates the provision command
func newClusterProvisionCmd() *cobra.Command {
	var manifestPath string
	var only string
	var dryRun bool
	var force bool
	var ignoreValidation bool

	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Provision cluster infrastructure and services",
		Long: `Provision cluster infrastructure and services from manifest:

Phase Options (--only):
  infrastructure  - Provision Postgres, Kafka, Zookeeper, ClickHouse
  applications    - Provision FrameWorks services
  interfaces      - Provision Nginx, webapp, website
  all             - Provision everything (default)

Provisioning is idempotent - safe to run multiple times.
Existing services will be detected and skipped unless --force is used.`,
		Example: `  # Provision all infrastructure
  frameworks cluster provision --only infrastructure --manifest cluster.yaml

  # Dry-run to see what would be provisioned
  frameworks cluster provision --manifest cluster.yaml --dry-run

  # Force re-provision even if services exist
  frameworks cluster provision --force --manifest cluster.yaml

  # Continue even if health validation fails (not recommended)
  frameworks cluster provision --ignore-validation --manifest cluster.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvision(cmd, manifestPath, only, dryRun, force, ignoreValidation)
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", "cluster.yaml", "Path to cluster manifest file")
	cmd.Flags().StringVar(&only, "only", "all", "Phase to provision (infrastructure|applications|interfaces|all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show plan without executing")
	cmd.Flags().BoolVar(&force, "force", false, "Force re-provision even if exists")
	cmd.Flags().BoolVar(&ignoreValidation, "ignore-validation", false, "Continue even if health validation fails (DANGEROUS)")

	return cmd
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
		fmt.Fprintln(cmd.OutOrStdout(), "[DRY-RUN MODE - No changes will be made]\n")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Convert only flag to phase
	phase := orchestrator.PhaseAll
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
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s (%s) on %s\n", task.Name, task.Type, task.Host)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nTotal tasks: %d\n\n", len(plan.AllTasks))

	if dryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Dry-run complete. Use without --dry-run to execute.")
		return nil
	}

	// Execute provisioning
	if err := executeProvision(ctx, cmd, manifest, plan, force, ignoreValidation); err != nil {
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
func executeProvision(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, plan *orchestrator.ExecutionPlan, force, ignoreValidation bool) error {
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
			fmt.Fprintf(cmd.OutOrStdout(), "  Provisioning %s on %s...\n", task.Name, task.Host)

			// Get host config
			host, ok := manifest.GetHost(task.Host)
			if !ok {
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("host %s not found in manifest", task.Host)
			}

			// Build config for this task
			config := buildTaskConfig(task, runtimeData)

			// Provision based on task type
			if err := provisionTask(ctx, task, host, sshPool, force, ignoreValidation, runtimeData); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Provisioning failed for %s: %v\n", task.Name, err)
				fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
				rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
				return fmt.Errorf("failed to provision %s: %w", task.Name, err)
			}

			// Track completed task for potential rollback
			completed = append(completed, provisionedTask{task: task, host: host, config: config})

			// Bootstrap Logic: Run after Quartermaster is healthy
			if task.Type == "quartermaster" {
				fmt.Fprintln(cmd.OutOrStdout(), "  Running Cluster Bootstrap (System Tenant)...")
				token, err := runBootstrap(ctx, manifest)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Bootstrap failed: %v\n", err)
					fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("bootstrap failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "    ✓ System Tenant bootstrapped")
				// Store token and gRPC address for Privateer
				runtimeData["enrollment_token"] = token
				// Compute Quartermaster gRPC address for Privateer
				qmGRPCAddr := fmt.Sprintf("%s:19002", host.Address)
				runtimeData["quartermaster_grpc_addr"] = qmGRPCAddr
			}

			fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s provisioned\n", task.Name)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

// buildTaskConfig creates a ServiceConfig for a task
func buildTaskConfig(task *orchestrator.Task, runtimeData map[string]interface{}) provisioner.ServiceConfig {
	config := provisioner.ServiceConfig{
		Mode:     "docker",
		Version:  "stable",
		Port:     provisioner.ServicePorts[task.Type],
		Metadata: make(map[string]interface{}),
	}

	// Copy runtime data
	for k, v := range runtimeData {
		config.Metadata[k] = v
	}

	// Override for infrastructure
	if task.Phase == orchestrator.PhaseInfrastructure {
		config.Mode = "native"
		config.Version = "latest"
	}

	// Native override for Privateer
	if task.Type == "privateer" {
		config.Mode = "native"
	}

	return config
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
func provisionTask(ctx context.Context, task *orchestrator.Task, host inventory.Host, pool *ssh.Pool, force, ignoreValidation bool, runtimeData map[string]interface{}) error {
	// Get provisioner from registry
	prov, err := provisioner.GetProvisioner(task.Type, pool)
	if err != nil {
		return fmt.Errorf("failed to get provisioner: %w", err)
	}

	// Build service config
	config := provisioner.ServiceConfig{
		Mode:     "docker", // Default to docker for applications
		Version:  "stable", // Use stable channel from gitops
		Port:     provisioner.ServicePorts[task.Type],
		Metadata: make(map[string]interface{}),
	}

	// Inject runtime data (e.g. enrollment token)
	for k, v := range runtimeData {
		config.Metadata[k] = v
	}

	// Override for infrastructure (stay native)
	if task.Phase == orchestrator.PhaseInfrastructure {
		config.Mode = "native"
		config.Version = "latest"
	}

	// Native override for Privateer
	if task.Type == "privateer" {
		config.Mode = "native"
	}

	// Provision
	if err := prov.Provision(ctx, host, config); err != nil {
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

	return nil
}

// runBootstrap connects to Quartermaster and generates an infrastructure token
func runBootstrap(ctx context.Context, manifest *inventory.Manifest) (string, error) {
	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		return "", fmt.Errorf("SERVICE_TOKEN env var required for bootstrapping")
	}

	var qmHost string
	for name, svc := range manifest.Services {
		if name == "quartermaster" {
			qmHost = svc.Host
			if qmHost == "" && len(svc.Hosts) > 0 {
				qmHost = svc.Hosts[0]
			}
			break
		}
	}

	host, ok := manifest.GetHost(qmHost)
	if !ok {
		return "", fmt.Errorf("quartermaster host not found in manifest")
	}

	// Use gRPC client instead of HTTP
	grpcAddr := fmt.Sprintf("%s:19002", host.Address)
	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr: grpcAddr,
		Logger:   logging.NewLogger(),
	})
	if err != nil {
		return "", fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	defer client.Close()

	// 1. Ensure "FrameWorks" System Tenant Exists
	var systemTenantID string
	tenantsResp, err := client.ListTenants(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get tenants from Quartermaster: %w", err)
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
		createTenantResp, err := client.CreateTenant(ctx, createTenantReq)
		if err != nil {
			return "", fmt.Errorf("failed to create 'FrameWorks' System Tenant: %w", err)
		}
		systemTenantID = createTenantResp.Tenant.Id
		fmt.Printf("    ✓ Created System Tenant with ID: %s\n", systemTenantID)
	} else {
		fmt.Printf("  ✓ 'FrameWorks' System Tenant already exists: %s\n", systemTenantID)
	}

	// 2. Ensure Cluster is Registered
	clusterID := fmt.Sprintf("%s-%s", manifest.Type, manifest.Profile)
	baseURL := fmt.Sprintf("http://%s:18002", host.Address)

	// Check if cluster exists
	_, err = client.GetCluster(ctx, clusterID)
	if err != nil && strings.Contains(err.Error(), "cluster not found") { // Check specific error for not found
		fmt.Printf("  Registering Cluster '%s'...\n", clusterID)
		createClusterReq := &pb.CreateClusterRequest{
			ClusterId:   clusterID,
			ClusterName: fmt.Sprintf("FrameWorks %s %s Cluster", manifest.Type, manifest.Profile),
			ClusterType: manifest.Type,
			BaseUrl:     baseURL,
		}
		_, err = client.CreateCluster(ctx, createClusterReq)
		if err != nil {
			return "", fmt.Errorf("failed to register cluster '%s': %w", clusterID, err)
		}
		fmt.Printf("    ✓ Registered Cluster: %s\n", clusterID)
	} else if err != nil {
		return "", fmt.Errorf("failed to check cluster '%s': %w", clusterID, err)
	} else {
		fmt.Printf("  ✓ Cluster '%s' already registered.\n", clusterID)
	}

	// 3. Register infrastructure nodes from manifest
	fmt.Printf("  Registering infrastructure nodes...\n")
	for hostName, hostInfo := range manifest.Hosts {
		// Determine node type from roles
		nodeType := "core"
		for _, role := range hostInfo.Roles {
			if role == "edge" {
				nodeType = "edge"
				break
			}
		}

		externalIP := hostInfo.Address
		_, err := client.CreateNode(ctx, &pb.CreateNodeRequest{
			NodeId:     hostName,
			ClusterId:  clusterID,
			NodeName:   hostName,
			NodeType:   nodeType,
			ExternalIp: &externalIP,
		})
		if err != nil {
			// Ignore duplicate errors (node already exists)
			if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "already exists") {
				return "", fmt.Errorf("failed to register node %s: %w", hostName, err)
			}
			fmt.Printf("    ✓ Node already exists: %s\n", hostName)
		} else {
			fmt.Printf("    ✓ Registered node: %s (%s)\n", hostName, nodeType)
		}
	}

	// 4. Generate Infrastructure Token
	fmt.Printf("  Generating Infrastructure Enrollment Token...\n")
	infrastructureTokenReq := &pb.CreateBootstrapTokenRequest{
		Name:      fmt.Sprintf("Infrastructure Enrollment Token for %s", clusterID),
		Kind:      "infrastructure_node", // Using the new kind
		Ttl:       "720h",                // Valid for 30 days
		TenantId:  &systemTenantID,
		ClusterId: &clusterID,
	}

	resp, err := client.CreateBootstrapToken(ctx, infrastructureTokenReq)
	if err != nil {
		return "", fmt.Errorf("failed to create infrastructure bootstrap token: %w", err)
	}

	fmt.Printf("    ✓ Generated Infrastructure Token: %s\n", resp.Token.Id)
	return resp.Token.Token, nil
}

// getDefaultPort returns default port for a service type
func getDefaultPort(serviceType string) int {
	ports := map[string]int{
		"postgres":   5432,
		"kafka":      9092,
		"zookeeper":  2181,
		"clickhouse": 9000,
	}

	if port, ok := ports[serviceType]; ok {
		return port
	}

	return 8080 // Default
}
