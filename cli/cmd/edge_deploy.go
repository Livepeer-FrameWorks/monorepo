package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/internal/templates"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"
	"frameworks/pkg/ctxkeys"

	qmclient "frameworks/pkg/clients/quartermaster"
	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

func newEdgeDeployCmd() *cobra.Command {
	var (
		clusterID       string
		clusterName     string
		region          string
		enrollmentToken string
		foghornAddr     string
		sshTarget       string
		sshKey          string
		mode            string
		email           string
		applyTuning     bool
		skipPreflight   bool
		version         string
		timeout         time.Duration
	)

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy an edge node in one command",
		Long: `Deploy an edge node with automatic VPC setup, enrollment, and provisioning.

Mode A — Logged-in tenant (requires 'frameworks login'):
  frameworks edge deploy --ssh ubuntu@edge-1 --email ops@example.com

  Automatically creates a private cluster (VPC) if needed, generates an
  enrollment token, and runs the full provision pipeline.

Mode B — Pre-existing token (no login needed):
  frameworks edge deploy --enrollment-token <token> --foghorn-addr foghorn.cluster.example.com:18019 --ssh ubuntu@edge-1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if enrollmentToken != "" {
				return deployWithToken(ctx, cmd, deployConfig{
					enrollmentToken: enrollmentToken,
					foghornAddr:     foghornAddr,
					sshTarget:       sshTarget,
					sshKey:          sshKey,
					mode:            mode,
					email:           email,
					applyTuning:     applyTuning,
					skipPreflight:   skipPreflight,
					version:         version,
					timeout:         timeout,
				})
			}

			return deployAutomatic(ctx, cmd, deployConfig{
				clusterID:     clusterID,
				clusterName:   clusterName,
				region:        region,
				foghornAddr:   foghornAddr,
				sshTarget:     sshTarget,
				sshKey:        sshKey,
				mode:          mode,
				email:         email,
				applyTuning:   applyTuning,
				skipPreflight: skipPreflight,
				version:       version,
				timeout:       timeout,
			})
		},
	}

	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to deploy to (auto-detected if omitted)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "name for new private cluster if one needs to be created")
	cmd.Flags().StringVar(&region, "region", "", "region for new private cluster")
	cmd.Flags().StringVar(&enrollmentToken, "enrollment-token", "", "pre-existing enrollment token (skips login/VPC setup)")
	cmd.Flags().StringVar(&foghornAddr, "foghorn-addr", "", "Foghorn gRPC address (required with --enrollment-token)")
	cmd.Flags().StringVar(&sshTarget, "ssh", "", "SSH target (user@host) for remote deployment")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().StringVar(&mode, "mode", "docker", "deployment mode: docker or native")
	cmd.Flags().StringVar(&email, "email", "", "ACME email for TLS certificates")
	cmd.Flags().BoolVar(&applyTuning, "tune", true, "apply sysctl/limits tuning")
	cmd.Flags().BoolVar(&skipPreflight, "skip-preflight", false, "skip preflight checks")
	cmd.Flags().StringVar(&version, "version", "", "platform version for binary resolution")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "HTTPS verification timeout")
	return cmd
}

type deployConfig struct {
	clusterID       string
	clusterName     string
	region          string
	enrollmentToken string
	foghornAddr     string
	sshTarget       string
	sshKey          string
	mode            string
	email           string
	applyTuning     bool
	skipPreflight   bool
	version         string
	timeout         time.Duration
}

// deployWithToken handles Mode B: pre-existing enrollment token.
func deployWithToken(ctx context.Context, cmd *cobra.Command, cfg deployConfig) error {
	if cfg.foghornAddr == "" {
		// Try context
		config, _, err := fwcfg.Load()
		if err == nil {
			ctxCfg := fwcfg.GetCurrent(config)
			cfg.foghornAddr = ctxCfg.Endpoints.FoghornGRPCAddr
		}
		if cfg.foghornAddr == "" {
			return fmt.Errorf("--foghorn-addr is required when using --enrollment-token")
		}
	}

	return runEdgeDeploy(ctx, cmd, cfg)
}

// deployAutomatic handles Mode A: logged-in tenant with automatic VPC setup.
func deployAutomatic(ctx context.Context, cmd *cobra.Command, cfg deployConfig) error {
	qm, ctxCfg, err := qmGRPCClientFromContext()
	if err != nil {
		return fmt.Errorf("login required for automatic deployment (use 'frameworks login' first): %w", err)
	}
	defer func() { _ = qm.Close() }()

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if ctxCfg.Auth.JWT != "" {
		cctx = context.WithValue(cctx, ctxkeys.KeyJWTToken, ctxCfg.Auth.JWT)
	}

	// Resolve or create the private cluster
	token, foghornAddress, err := resolveClusterAndToken(cctx, cmd, qm, cfg)
	if err != nil {
		return err
	}

	cfg.enrollmentToken = token
	if cfg.foghornAddr == "" {
		cfg.foghornAddr = foghornAddress
	}
	if cfg.foghornAddr == "" {
		cfg.foghornAddr = ctxCfg.Endpoints.FoghornGRPCAddr
	}
	if cfg.foghornAddr == "" {
		return fmt.Errorf("could not determine Foghorn address; set via --foghorn-addr or context")
	}

	return runEdgeDeploy(ctx, cmd, cfg)
}

// resolveClusterAndToken finds or creates a private cluster and returns an enrollment token.
func resolveClusterAndToken(ctx context.Context, cmd *cobra.Command, qm *qmclient.GRPCClient, cfg deployConfig) (token, foghornAddr string, err error) {
	if cfg.clusterID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Creating enrollment token for cluster %s...\n", cfg.clusterID)
		return createEnrollmentTokenForCluster(ctx, qm, cfg.clusterID)
	}

	// No cluster specified — try to find existing private clusters
	fmt.Fprintln(cmd.OutOrStdout(), "Looking for private clusters...")
	resp, err := qm.ListMySubscriptions(ctx, &pb.ListMySubscriptionsRequest{})
	if err != nil {
		return "", "", fmt.Errorf("failed to list clusters: %w", err)
	}

	// Filter to private (owner-operated) clusters
	var privateClusters []*pb.InfrastructureCluster
	for _, c := range resp.GetClusters() {
		if c.GetOwnerTenantId() != "" && c.GetClusterType() == "private" {
			privateClusters = append(privateClusters, c)
		}
	}

	switch len(privateClusters) {
	case 0:
		// No private cluster — create one via EnableSelfHosting
		return enableSelfHosting(ctx, cmd, qm, cfg)
	case 1:
		cluster := privateClusters[0]
		fmt.Fprintf(cmd.OutOrStdout(), "Using cluster: %s (%s)\n", cluster.GetClusterName(), cluster.GetClusterId())
		return createEnrollmentTokenForCluster(ctx, qm, cluster.GetClusterId())
	default:
		fmt.Fprintln(cmd.OutOrStdout(), "Multiple private clusters found. Specify one with --cluster-id:")
		for _, c := range privateClusters {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s  %s\n", c.GetClusterId(), c.GetClusterName())
		}
		return "", "", fmt.Errorf("ambiguous: %d private clusters found", len(privateClusters))
	}
}

// enableSelfHosting creates a new private cluster (VPC) for the tenant.
func enableSelfHosting(ctx context.Context, cmd *cobra.Command, qm *qmclient.GRPCClient, cfg deployConfig) (token, foghornAddr string, err error) {
	name := cfg.clusterName
	if name == "" {
		name = "My Edge Network"
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Creating private cluster '%s'...\n", name)

	req := &pb.EnableSelfHostingRequest{
		ClusterName: name,
	}
	if cfg.region != "" {
		req.ShortDescription = &cfg.region
	}

	resp, err := qm.EnableSelfHosting(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("failed to create private cluster: %w", err)
	}

	cluster := resp.GetCluster()
	bt := resp.GetBootstrapToken()

	fmt.Fprintf(cmd.OutOrStdout(), "  Cluster:  %s (%s)\n", cluster.GetClusterName(), cluster.GetClusterId())
	fmt.Fprintf(cmd.OutOrStdout(), "  Base URL: %s\n", cluster.GetBaseUrl())
	fmt.Fprintf(cmd.OutOrStdout(), "  Foghorn:  %s\n", resp.GetFoghornAddr())

	return bt.GetToken(), resp.GetFoghornAddr(), nil
}

// createEnrollmentTokenForCluster creates an enrollment token for an existing cluster.
func createEnrollmentTokenForCluster(ctx context.Context, qm *qmclient.GRPCClient, clusterID string) (token, foghornAddr string, err error) {
	resp, err := qm.CreateEnrollmentToken(ctx, &pb.CreateEnrollmentTokenRequest{
		ClusterId: clusterID,
	})
	if err != nil {
		return "", "", fmt.Errorf("failed to create enrollment token: %w", err)
	}
	bt := resp.GetToken()
	if bt == nil {
		return "", "", fmt.Errorf("no token returned")
	}
	return bt.GetToken(), "", nil
}

// runEdgeDeploy runs the common provision pipeline after token resolution.
func runEdgeDeploy(ctx context.Context, cmd *cobra.Command, cfg deployConfig) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge node...")

	resp, err := preRegisterEdge(ctx, cfg.foghornAddr, cfg.enrollmentToken, cfg.sshTarget, cfg.sshKey)
	if err != nil {
		return fmt.Errorf("pre-registration failed: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Node ID:     %s\n", resp.GetNodeId())
	fmt.Fprintf(cmd.OutOrStdout(), "  Edge domain: %s\n", resp.GetEdgeDomain())
	fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", resp.GetPoolDomain())
	fmt.Fprintf(cmd.OutOrStdout(), "  Cluster:     %s\n", resp.GetClusterSlug())

	foghornGRPC := cfg.foghornAddr
	if addr := resp.GetFoghornGrpcAddr(); addr != "" {
		foghornGRPC = addr
	}

	// Derive HTTP base from gRPC address (same host, HTTP port)
	foghornHTTPBase := deriveFoghornHTTPBase(foghornGRPC)

	if cfg.sshTarget != "" {
		return deployViaSSH(ctx, cmd, cfg, resp, foghornGRPC, foghornHTTPBase)
	}

	return deployLocal(ctx, cmd, cfg, resp, foghornGRPC, foghornHTTPBase)
}

func deployViaSSH(ctx context.Context, cmd *cobra.Command, cfg deployConfig, resp *pb.PreRegisterEdgeResponse, foghornGRPC, foghornHTTPBase string) error {
	host := sshTargetToHost(cfg.sshTarget, cfg.sshKey)
	pool := fwssh.NewPool(30 * time.Second)

	epConfig := provisioner.EdgeProvisionConfig{
		Mode:            cfg.mode,
		NodeName:        "edge-" + resp.GetNodeId(),
		NodeDomain:      resp.GetEdgeDomain(),
		PoolDomain:      resp.GetPoolDomain(),
		EnrollmentToken: cfg.enrollmentToken,
		FoghornGRPCAddr: foghornGRPC,
		FoghornHTTPBase: foghornHTTPBase,
		NodeID:          resp.GetNodeId(),
		CertPEM:         resp.GetCertPem(),
		KeyPEM:          resp.GetKeyPem(),
		Email:           cfg.email,
		SkipPreflight:   cfg.skipPreflight,
		ApplyTuning:     cfg.applyTuning,
		Timeout:         cfg.timeout,
		Version:         cfg.version,
	}

	ep := provisioner.NewEdgeProvisioner(pool)
	fmt.Fprintln(cmd.OutOrStdout(), "Provisioning edge node via SSH...")
	return ep.Provision(ctx, host, epConfig)
}

func deployLocal(ctx context.Context, cmd *cobra.Command, cfg deployConfig, resp *pb.PreRegisterEdgeResponse, foghornGRPC, foghornHTTPBase string) error {
	_ = ctx

	vars := templates.EdgeVars{
		NodeID:          resp.GetNodeId(),
		EdgeDomain:      resp.GetEdgeDomain(),
		AcmeEmail:       cfg.email,
		FoghornHTTPBase: foghornHTTPBase,
		FoghornGRPCAddr: foghornGRPC,
		EnrollmentToken: cfg.enrollmentToken,
		Mode:            cfg.mode,
	}

	// Stage TLS certs if provided
	if cert := resp.GetCertPem(); cert != "" {
		if key := resp.GetKeyPem(); key != "" {
			vars.CertPath = "/etc/frameworks/certs/cert.pem"
			vars.KeyPath = "/etc/frameworks/certs/key.pem"
		}
	}

	target := "."
	fmt.Fprintln(cmd.OutOrStdout(), "Writing edge templates...")
	if err := templates.WriteEdgeTemplates(target, vars, false); err != nil {
		return fmt.Errorf("failed to write templates: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Templates written to %s\n", target)
	fmt.Fprintln(cmd.OutOrStdout(), "Start the edge stack with: frameworks edge enroll")
	return nil
}

// deriveFoghornHTTPBase derives HTTP base URL from gRPC address.
// foghorn.cluster.example.com:18019 → https://foghorn.cluster.example.com:18008
func deriveFoghornHTTPBase(grpcAddr string) string {
	host := grpcAddr
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	return fmt.Sprintf("https://%s:18008", host)
}
