package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
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
  interfaces      - Provision Nginx/Caddy, Chartroom, Foredeck, Logbook
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
			config := buildTaskConfig(task, manifest, runtimeData, force)

			// Provision based on task type
			if err := provisionTask(ctx, task, host, sshPool, manifest, force, ignoreValidation, runtimeData); err != nil {
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
				token, serviceToken, qmGRPCAddr, err := runBootstrap(ctx, manifest)
				if err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "\n  ✗ Bootstrap failed: %v\n", err)
					fmt.Fprintln(cmd.OutOrStdout(), "\n  Rolling back previously provisioned services...")
					rollbackProvisionedTasks(ctx, cmd, sshPool, completed)
					return fmt.Errorf("bootstrap failed: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "    ✓ System Tenant bootstrapped")
				// Store token and gRPC address for Privateer
				runtimeData["enrollment_token"] = token
				if serviceToken != "" {
					runtimeData["service_token"] = serviceToken
				}
				if qmGRPCAddr != "" {
					runtimeData["quartermaster_grpc_addr"] = qmGRPCAddr
				}
			}

			fmt.Fprintf(cmd.OutOrStdout(), "    ✓ %s provisioned\n", task.Name)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "")
	}

	return nil
}

// buildTaskConfig creates a ServiceConfig for a task
func buildTaskConfig(task *orchestrator.Task, manifest *inventory.Manifest, runtimeData map[string]interface{}, force bool) provisioner.ServiceConfig {
	config := provisioner.ServiceConfig{
		Mode:     "docker",
		Version:  "stable",
		Port:     provisioner.ServicePorts[task.Type],
		Metadata: make(map[string]interface{}),
		Force:    force,
	}

	config.DeployName = task.Type

	// Copy runtime data
	for k, v := range runtimeData {
		config.Metadata[k] = v
	}

	if manifest != nil {
		// Service overrides
		if svc, ok := manifest.Services[task.Name]; ok {
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
			if port, err := resolvePort(task.Name, svc); err == nil && port != 0 {
				config.Port = port
			}
		}
		// Interface overrides
		if iface, ok := manifest.Interfaces[task.Name]; ok {
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
			if port, err := resolvePort(task.Name, iface); err == nil && port != 0 {
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
		}
	}

	// Override for infrastructure
	if task.Phase == orchestrator.PhaseInfrastructure && task.Type != "zookeeper" {
		config.Mode = "native"
		config.Version = "latest"
	}

	// Native override for Privateer
	if task.Type == "privateer" {
		config.Mode = "native"
	}

	return config
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
		if ok && host.Address != "" {
			address = host.Address
		}
		servers = append(servers, fmt.Sprintf("server.%d=%s:2888:3888", node.ID, address))
	}

	return &zookeeperNodeConfig{
		ServerID: targetNode.ID,
		Port:     targetNode.Port,
		Servers:  servers,
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
func provisionTask(ctx context.Context, task *orchestrator.Task, host inventory.Host, pool *ssh.Pool, manifest *inventory.Manifest, force, ignoreValidation bool, runtimeData map[string]interface{}) error {
	// Get provisioner from registry
	prov, err := provisioner.GetProvisioner(task.Type, pool)
	if err != nil {
		return fmt.Errorf("failed to get provisioner: %w", err)
	}

	config := buildTaskConfig(task, manifest, runtimeData, force)

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
func runBootstrap(ctx context.Context, manifest *inventory.Manifest) (string, string, string, error) {
	serviceToken := os.Getenv("SERVICE_TOKEN")
	if serviceToken == "" {
		if cfg, _, err := fwcfg.Load(); err == nil {
			cliCtx := fwcfg.GetCurrent(cfg)
			cliCtx.Auth = fwcfg.ResolveAuth(cliCtx)
			serviceToken = cliCtx.Auth.ServiceToken
		}
	}
	if serviceToken == "" {
		return "", "", "", fmt.Errorf("service token required for bootstrapping (set SERVICE_TOKEN or run 'frameworks login')")
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
		return "", "", "", fmt.Errorf("quartermaster host not found in manifest")
	}

	// Use gRPC client instead of HTTP
	grpcPort := 19002
	if qmSvc.GRPCPort != 0 {
		grpcPort = qmSvc.GRPCPort
	}
	grpcAddr := fmt.Sprintf("%s:%d", host.Address, grpcPort)
	client, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:     grpcAddr,
		Logger:       logging.NewLogger(),
		ServiceToken: serviceToken,
	})
	if err != nil {
		return "", "", "", fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}
	defer client.Close()

	// 1. Ensure "FrameWorks" System Tenant Exists
	var systemTenantID string
	tenantsResp, err := client.ListTenants(ctx, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to get tenants from Quartermaster: %w", err)
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
			return "", "", "", fmt.Errorf("failed to create 'FrameWorks' System Tenant: %w", errCreate)
		}
		systemTenantID = createTenantResp.Tenant.Id
		fmt.Printf("    ✓ Created System Tenant with ID: %s\n", systemTenantID)
	} else {
		fmt.Printf("  ✓ 'FrameWorks' System Tenant already exists: %s\n", systemTenantID)
	}

	// 2. Ensure Cluster is Registered
	clusterID := fmt.Sprintf("%s-%s", manifest.Type, manifest.Profile)
	basePort := 18002
	if qmSvc.Port != 0 {
		basePort = qmSvc.Port
	}
	baseURL := fmt.Sprintf("http://%s:%d", host.Address, basePort)
	// Prefer Bridge (Gateway) public URL if present in manifest
	if bridgeSvc, ok := manifest.Services["bridge"]; ok && bridgeSvc.Enabled {
		if bridgeSvc.Port != 0 {
			if bridgeHost, ok := manifest.GetHost(bridgeSvc.Host); ok {
				baseURL = fmt.Sprintf("http://%s:%d", bridgeHost.Address, bridgeSvc.Port)
			}
		}
	}

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
			return "", "", "", fmt.Errorf("failed to register cluster '%s': %w", clusterID, err)
		}
		fmt.Printf("    ✓ Registered Cluster: %s\n", clusterID)
	} else if err != nil {
		return "", "", "", fmt.Errorf("failed to check cluster '%s': %w", clusterID, err)
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
		if hostInfo.ExternalIP != "" {
			externalIP = hostInfo.ExternalIP
		}
		_, errCreate := client.CreateNode(ctx, &pb.CreateNodeRequest{
			NodeId:     hostName,
			ClusterId:  clusterID,
			NodeName:   hostName,
			NodeType:   nodeType,
			ExternalIp: &externalIP,
		})
		if errCreate != nil {
			// Ignore duplicate errors (node already exists)
			if !strings.Contains(errCreate.Error(), "duplicate") && !strings.Contains(errCreate.Error(), "already exists") {
				return "", "", "", fmt.Errorf("failed to register node %s: %w", hostName, errCreate)
			}
			fmt.Printf("    ✓ Node already exists: %s\n", hostName)
		} else {
			fmt.Printf("    ✓ Registered node: %s (%s)\n", hostName, nodeType)
		}
	}

	// 3b. Register public service endpoints for DNS (Bridge + interfaces)
	fmt.Printf("  Registering public service endpoints...\n")
	for name, svc := range manifest.Services {
		if !svc.Enabled {
			continue
		}
		serviceType, ok := publicServiceType(name)
		if !ok {
			continue
		}
		hostName := svc.Host
		if hostName == "" && len(svc.Hosts) > 0 {
			hostName = svc.Hosts[0]
		}
		if hostName == "" {
			continue
		}
		hostInfo, ok := manifest.GetHost(hostName)
		if !ok {
			continue
		}
		externalIP := hostInfo.Address
		if hostInfo.ExternalIP != "" {
			externalIP = hostInfo.ExternalIP
		}
		nodeID := fmt.Sprintf("%s-%s", hostName, serviceType)
		nodeName := fmt.Sprintf("%s-%s", hostName, serviceType)
		_, errCreate := client.CreateNode(ctx, &pb.CreateNodeRequest{
			NodeId:     nodeID,
			ClusterId:  clusterID,
			NodeName:   nodeName,
			NodeType:   serviceType,
			ExternalIp: &externalIP,
		})
		if errCreate != nil {
			if !strings.Contains(errCreate.Error(), "duplicate") && !strings.Contains(errCreate.Error(), "already exists") {
				return "", "", "", fmt.Errorf("failed to register public node %s: %w", nodeID, errCreate)
			}
			fmt.Printf("    ✓ Public node already exists: %s\n", nodeID)
		} else {
			fmt.Printf("    ✓ Registered public node: %s (%s)\n", nodeID, serviceType)
		}
	}
	for name, iface := range manifest.Interfaces {
		if !iface.Enabled {
			continue
		}
		serviceType, ok := publicServiceType(name)
		if !ok {
			continue
		}
		hostName := iface.Host
		if hostName == "" && len(iface.Hosts) > 0 {
			hostName = iface.Hosts[0]
		}
		if hostName == "" {
			continue
		}
		hostInfo, ok := manifest.GetHost(hostName)
		if !ok {
			continue
		}
		externalIP := hostInfo.Address
		if hostInfo.ExternalIP != "" {
			externalIP = hostInfo.ExternalIP
		}
		nodeID := fmt.Sprintf("%s-%s", hostName, serviceType)
		nodeName := fmt.Sprintf("%s-%s", hostName, serviceType)
		_, errCreate := client.CreateNode(ctx, &pb.CreateNodeRequest{
			NodeId:     nodeID,
			ClusterId:  clusterID,
			NodeName:   nodeName,
			NodeType:   serviceType,
			ExternalIp: &externalIP,
		})
		if errCreate != nil {
			if !strings.Contains(errCreate.Error(), "duplicate") && !strings.Contains(errCreate.Error(), "already exists") {
				return "", "", "", fmt.Errorf("failed to register public node %s: %w", nodeID, errCreate)
			}
			fmt.Printf("    ✓ Public node already exists: %s\n", nodeID)
		} else {
			fmt.Printf("    ✓ Registered public node: %s (%s)\n", nodeID, serviceType)
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

	resp, errToken := client.CreateBootstrapToken(ctx, infrastructureTokenReq)
	if errToken != nil {
		return "", "", "", fmt.Errorf("failed to create infrastructure bootstrap token: %w", errToken)
	}

	fmt.Printf("    ✓ Generated Infrastructure Token: %s\n", resp.Token.Id)
	return resp.Token.Token, serviceToken, grpcAddr, nil
}

// publicServiceType maps public-facing services to DNS node types.
func publicServiceType(serviceName string) (string, bool) {
	switch serviceName {
	case "bridge":
		return "api", true
	case "chartroom":
		return "app", true
	case "foredeck":
		return "website", true
	case "logbook":
		return "docs", true
	case "steward":
		return "forms", true
	default:
		return "", false
	}
}
