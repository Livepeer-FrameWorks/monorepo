package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	fwcfg "frameworks/cli/internal/config"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/clients/bridge"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	fwssh "frameworks/cli/pkg/ssh"

	pb "frameworks/pkg/proto"

	"github.com/spf13/cobra"
)

func newEdgeDeployCmd() *cobra.Command {
	var (
		clusterID       string
		clusterName     string
		region          string
		nodeName        string
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
		Long: `Deploy an edge node end-to-end via Bridge.

Mode A — Logged-in tenant (requires 'frameworks login'):
  frameworks edge deploy --ssh ubuntu@edge-1 --email ops@example.com

  Bridge creates a private cluster (if needed), issues an enrollment
  token, and the CLI runs the full provision pipeline. The operator
  never has to know cluster topology.

Mode B — Pre-existing token (no login needed):
  frameworks edge deploy --enrollment-token <token> --ssh ubuntu@edge-1

  The token IS the credential. Bridge resolves the cluster's Foghorn
  on the operator's behalf via bootstrapEdge.

--foghorn-addr is an explicit override for direct-Foghorn debugging.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			modeA := enrollmentToken == ""

			target := sshTarget
			if target == "" {
				target = "local"
			}
			heading := "Deploying edge node"
			if modeA {
				heading = "Deploying edge node (via Bridge — creates cluster + token)"
			}
			ux.Heading(out, fmt.Sprintf("%s on %s", heading, target))

			var (
				err           error
				nodeID        string
				domain        string
				clusterSlug   string
				bridgeCreated bool
				provisioned   = false
			)

			deployCfg := deployConfig{
				clusterID:       clusterID,
				clusterName:     clusterName,
				region:          region,
				nodeName:        nodeName,
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
			}

			if modeA {
				bridgeCreated, err = deployAutomatic(ctx, cmd, &deployCfg, &nodeID, &domain, &clusterSlug)
			} else {
				err = deployWithToken(ctx, cmd, deployCfg, &nodeID, &domain, &clusterSlug)
			}
			if err == nil {
				provisioned = true
			}

			renderEdgeDeployResult(cmd, edgeDeployResultFields{
				modeA:         modeA,
				bridgeCreated: bridgeCreated,
				nodeID:        nodeID,
				domain:        domain,
				clusterSlug:   clusterSlug,
				provisioned:   provisioned,
				failed:        err,
			})
			if err != nil {
				return err
			}
			ux.PrintNextSteps(out, []ux.NextStep{
				{Cmd: "frameworks edge status", Why: "Verify services are up and HTTPS is healthy."},
				{Cmd: "frameworks edge doctor", Why: "Run diagnostics and get adaptive remediation hints."},
			})
			return nil
		},
	}

	cmd.Flags().StringVar(&clusterID, "cluster-id", "", "cluster to deploy to (auto-detected if omitted)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "name for new private cluster if one needs to be created")
	cmd.Flags().StringVar(&region, "region", "", "region for new private cluster")
	cmd.Flags().StringVar(&nodeName, "node-name", "", "preferred node name/id for enrollment and DNS")
	cmd.Flags().StringVar(&enrollmentToken, "enrollment-token", "", "pre-existing enrollment token (skips login/VPC setup)")
	cmd.Flags().StringVar(&foghornAddr, "foghorn-addr", "", "explicit Foghorn gRPC override (debug only; normally Bridge resolves it)")
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
	nodeName        string
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

// deployWithToken runs Mode B and populates nodeID / domain /
// clusterSlug as the pipeline progresses so renderEdgeDeployResult can
// show partial state on failure.
func deployWithToken(ctx context.Context, cmd *cobra.Command, cfg deployConfig, nodeID, domain, clusterSlug *string) error {
	cliCtx, err := loadActiveContextLax()
	if err != nil {
		return err
	}
	return runEdgeDeploy(ctx, cmd, cliCtx, cfg, nodeID, domain, clusterSlug)
}

// deployAutomatic runs Mode A (Bridge auto-setup) and populates
// the result out-vars. Returns whether a cluster was freshly created (for
// the result block's "vpc" field).
func deployAutomatic(ctx context.Context, cmd *cobra.Command, cfg *deployConfig, nodeID, domain, clusterSlug *string) (bool, error) {
	cliCtx, err := loadActiveContextLax()
	if err != nil {
		return false, err
	}
	if cliCtx.Endpoints.BridgeURL == "" {
		return false, fmt.Errorf("automatic deployment requires a Bridge URL on the active context (run 'frameworks setup' first)")
	}
	ux.ContextNotice(cmd.OutOrStdout(), "bridge", cliCtx.Endpoints.BridgeURL)

	jwt, err := fwcredentials.Resolve(fwcredentials.AccountUserSession)
	if err != nil {
		return false, fmt.Errorf("resolve user session: %w", err)
	}
	if jwt == "" {
		return false, fmt.Errorf("automatic deployment requires user authentication; run 'frameworks login' first")
	}

	bc := bridge.New(cliCtx.Endpoints.BridgeURL)
	token, created, err := resolveEnrollmentToken(ctx, cmd, bc, jwt, *cfg)
	if err != nil {
		return false, err
	}
	cfg.enrollmentToken = token

	return created, runEdgeDeploy(ctx, cmd, cliCtx, *cfg, nodeID, domain, clusterSlug)
}

// resolveEnrollmentToken finds or creates a private cluster via Bridge and
// returns the issued bootstrap token plus whether a NEW cluster was created
// (vs. reusing an existing one — used by the result block's "vpc" field).
func resolveEnrollmentToken(ctx context.Context, cmd *cobra.Command, bc *bridge.Client, jwt string, cfg deployConfig) (string, bool, error) {
	if cfg.clusterID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Creating enrollment token for cluster %s...\n", cfg.clusterID)
		tok, err := bc.CreateEnrollmentToken(ctx, jwt, cfg.clusterID, nil, nil)
		if err != nil {
			return "", false, err
		}
		return tok.Token, false, nil
	}

	name := cfg.clusterName
	if name == "" {
		name = "My Edge Network"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Creating private cluster %q via Bridge...\n", name)
	in := bridge.CreateEdgeClusterInput{ClusterName: name}
	if cfg.region != "" {
		in.ShortDescription = &cfg.region
	}
	created, err := bc.CreateEdgeCluster(ctx, jwt, in)
	if err != nil {
		return "", false, err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Cluster: %s (%s)\n", created.ClusterName, created.ClusterID)
	return created.BootstrapToken, true, nil
}

// runEdgeDeploy runs the common provision pipeline and populates
// the out-vars for the result block. Pre-registration goes through Bridge
// unless --foghorn-addr is explicitly set (debug override).
func runEdgeDeploy(ctx context.Context, cmd *cobra.Command, cliCtx fwcfg.Context, cfg deployConfig, nodeID, domain, clusterSlug *string) error {
	fmt.Fprintln(cmd.OutOrStdout(), "Pre-registering edge node...")

	preferredNodeID := deriveEdgeNodeName(cfg.nodeName, "", cfg.sshTarget, cfg.sshTarget == "")
	var (
		resp *pb.PreRegisterEdgeResponse
		err  error
	)
	if cfg.foghornAddr != "" {
		resp, err = preRegisterEdge(ctx, cfg.foghornAddr, cfg.enrollmentToken, cfg.sshTarget, cfg.sshKey, preferredNodeID)
	} else {
		resp, err = bootstrapEdgeViaBridge(ctx, cliCtx, cfg.enrollmentToken, cfg.sshTarget, cfg.sshKey, preferredNodeID)
	}
	if err != nil {
		return fmt.Errorf("pre-registration failed: %w", err)
	}

	if nodeID != nil {
		*nodeID = resp.GetNodeId()
	}
	if domain != nil {
		*domain = resp.GetEdgeDomain()
	}
	if clusterSlug != nil {
		*clusterSlug = resp.GetClusterSlug()
	}

	fmt.Fprintf(cmd.OutOrStdout(), "  Node ID:     %s\n", resp.GetNodeId())
	fmt.Fprintf(cmd.OutOrStdout(), "  Edge domain: %s\n", resp.GetEdgeDomain())
	fmt.Fprintf(cmd.OutOrStdout(), "  Pool domain: %s\n", resp.GetPoolDomain())
	fmt.Fprintf(cmd.OutOrStdout(), "  Cluster:     %s\n", resp.GetClusterSlug())

	foghornGRPC := cfg.foghornAddr
	if addr := resp.GetFoghornGrpcAddr(); addr != "" {
		foghornGRPC = addr
	}

	if cfg.sshTarget != "" {
		return deployViaSSH(ctx, cmd, cfg, resp, foghornGRPC)
	}

	return deployLocal(ctx, cmd, cfg, resp, foghornGRPC)
}

type edgeDeployResultFields struct {
	modeA         bool
	bridgeCreated bool
	nodeID        string
	domain        string
	clusterSlug   string
	provisioned   bool
	failed        error
}

func renderEdgeDeployResult(cmd *cobra.Command, f edgeDeployResultFields) {
	out := cmd.OutOrStdout()

	vpcOK := true
	vpcDetail := "using existing cluster"
	if f.modeA {
		if f.bridgeCreated {
			vpcDetail = "created via Bridge"
		}
		vpcOK = f.clusterSlug != ""
		if !vpcOK {
			vpcDetail = "not created"
		}
	} else {
		vpcDetail = "N/A (token mode)"
	}

	enrollmentOK := f.nodeID != ""
	enrollmentDetail := "issued"
	if !enrollmentOK {
		enrollmentDetail = "not issued"
	}

	stackOK := f.provisioned
	stackDetail := "docker compose up"
	if !stackOK {
		stackDetail = "provisioning did not complete"
	}

	httpsOK := f.provisioned
	httpsDetail := "https/health reachable"
	if !httpsOK {
		httpsDetail = "not verified"
	}

	ux.Result(out, []ux.ResultField{
		{Key: "vpc", OK: vpcOK, Detail: vpcDetail},
		{Key: "enrollment", OK: enrollmentOK, Detail: enrollmentDetail},
		{Key: "stack", OK: stackOK, Detail: stackDetail},
		{Key: "https", OK: httpsOK, Detail: httpsDetail},
		{Key: "node", OK: enrollmentOK, Detail: f.nodeID},
		{Key: "domain", OK: enrollmentOK, Detail: f.domain},
	})
}

func loadActiveContextLax() (fwcfg.Context, error) {
	loaded, err := fwcfg.Load()
	if err != nil {
		return fwcfg.Context{}, err
	}
	rt := fwcfg.GetRuntimeOverrides()
	return fwcfg.MaybeActiveContext(rt, fwcfg.OSEnv{}, loaded)
}

func deployViaSSH(ctx context.Context, cmd *cobra.Command, cfg deployConfig, resp *pb.PreRegisterEdgeResponse, foghornGRPC string) error {
	host := sshTargetToHost(cfg.sshTarget)
	pool := fwssh.NewPool(30*time.Second, cfg.sshKey)

	epConfig := provisioner.EdgeProvisionConfig{
		Mode:            cfg.mode,
		NodeName:        resp.GetNodeId(),
		NodeDomain:      resp.GetEdgeDomain(),
		PoolDomain:      resp.GetPoolDomain(),
		EnrollmentToken: cfg.enrollmentToken,
		FoghornGRPCAddr: foghornGRPC,
		NodeID:          resp.GetNodeId(),
		CertPEM:         resp.GetCertPem(),
		KeyPEM:          resp.GetKeyPem(),
		CABundlePEM:     string(resp.GetInternalCaBundle()),
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

func deployLocal(ctx context.Context, cmd *cobra.Command, cfg deployConfig, resp *pb.PreRegisterEdgeResponse, foghornGRPC string) error {
	host := inventory.Host{
		ExternalIP: "localhost",
		User:       os.Getenv("USER"),
	}

	epConfig := provisioner.EdgeProvisionConfig{
		Mode:            "native",
		NodeName:        resp.GetNodeId(),
		NodeDomain:      resp.GetEdgeDomain(),
		PoolDomain:      resp.GetPoolDomain(),
		EnrollmentToken: cfg.enrollmentToken,
		FoghornGRPCAddr: foghornGRPC,
		NodeID:          resp.GetNodeId(),
		CertPEM:         resp.GetCertPem(),
		KeyPEM:          resp.GetKeyPem(),
		CABundlePEM:     string(resp.GetInternalCaBundle()),
		Email:           cfg.email,
		SkipPreflight:   cfg.skipPreflight,
		ApplyTuning:     cfg.applyTuning,
		Timeout:         cfg.timeout,
		Version:         cfg.version,
		DarwinDomain:    provisioner.DomainUser,
	}

	pool := fwssh.NewPool(30*time.Second, "")
	ep := provisioner.NewEdgeProvisioner(pool)

	fmt.Fprintln(cmd.OutOrStdout(), "Provisioning edge locally (user LaunchAgent, no admin required)...")
	return ep.Provision(ctx, host, epConfig)
}
