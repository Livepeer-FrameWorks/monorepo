package cmd

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	livepeerchain "github.com/Livepeer-FrameWorks/monorepo/pkg/livepeer/chain"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"

	"github.com/spf13/cobra"
)

func newLivepeerCmd() *cobra.Command {
	lp := &cobra.Command{Use: "livepeer", Short: "Inspect and maintain the Livepeer gateway wallet"}
	lp.PersistentFlags().String("address", "", "gateway wallet address (overrides discovery)")
	lp.PersistentFlags().String("rpc", "", "Arbitrum JSON-RPC URL (overrides cluster shared env)")
	lp.PersistentFlags().String("host", "", "gateway manifest host for an exceptional mutation")
	lp.PersistentFlags().String("cluster", "", "cluster ID for discovery or manifest selection")
	lp.PersistentFlags().String("manifest", "", "path to a single cluster.yaml")
	lp.PersistentFlags().String("gitops-dir", "", "path to a local gitops repository")
	lp.PersistentFlags().String("github-repo", "", "GitHub repository containing the cluster manifest")
	lp.PersistentFlags().String("github-ref", "", "branch or tag for --github-repo")
	lp.PersistentFlags().String("age-key", "", "age private key for SOPS-encrypted files")
	lp.PersistentFlags().String("ssh-key", "", "SSH private key path")
	lp.PersistentFlags().Int64("github-app-id", 0, "GitHub App ID")
	lp.PersistentFlags().Int64("github-installation-id", 0, "GitHub App installation ID")
	lp.PersistentFlags().String("github-private-key", "", "GitHub App private key PEM")

	lp.AddCommand(newLivepeerStatusCmd(), newLivepeerWalletCmd(), newLivepeerDepositCmd(), newLivepeerSimpleMutationCmd("unlock", "/unlock"), newLivepeerSimpleMutationCmd("withdraw", "/withdraw"))
	return lp
}

func discoverLivepeerWalletAddress(cmd *cobra.Command) (string, error) {
	explicit, err := cmd.Flags().GetString("address")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	clusterID, err := cmd.Flags().GetString("cluster")
	if err != nil {
		return "", err
	}
	qc, _, cleanup, err := newQMGRPCClientFromContext(cmd.Context())
	if err != nil {
		return "", fmt.Errorf("discover Livepeer wallet (or pass --address): %w", err)
	}
	defer cleanup()
	defer func() { _ = qc.Close() }()
	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	resp, err := qc.DiscoverServices(ctx, "livepeer-gateway", clusterID, nil)
	if err != nil {
		return "", fmt.Errorf("discover Livepeer wallet: %w", err)
	}
	return livepeerWalletFromDiscovery(resp)
}

func livepeerWalletFromDiscovery(resp *quartermasterpb.ServiceDiscoveryResponse) (string, error) {
	if resp != nil {
		for _, instance := range resp.GetInstances() {
			if instance.GetStatus() != "running" {
				continue
			}
			if wallet := strings.TrimSpace(instance.GetMetadata()[servicedefs.LivepeerGatewayMetadataWalletAddress]); wallet != "" {
				return wallet, nil
			}
		}
	}
	return "", fmt.Errorf("no running livepeer-gateway with wallet_address metadata found (or pass --address)")
}

func resolveLivepeerRPC(cmd *cobra.Command) (string, error) {
	explicit, err := cmd.Flags().GetString("rpc")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	rc, err := resolveClusterManifest(cmd)
	if err != nil {
		return "", fmt.Errorf("resolve Arbitrum RPC (or pass --rpc): %w", err)
	}
	defer rc.Cleanup()
	env, err := rc.PreparedSharedEnv()
	if err != nil {
		return "", fmt.Errorf("load cluster shared env: %w", err)
	}
	return livepeerRPCFromEnv(env)
}

func livepeerRPCFromEnv(env map[string]string) (string, error) {
	for _, key := range livepeerRPCPoolEnvKeys(env) {
		if urls := splitLivepeerRPCURLs(env[key]); len(urls) > 0 {
			return urls[0], nil
		}
	}
	return "", fmt.Errorf("cluster shared env contains no Livepeer/Arbitrum RPC URL (or pass --rpc)")
}

func newLivepeerStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Read wallet, deposit, and reserve state from Arbitrum", RunE: func(cmd *cobra.Command, _ []string) error {
		address, err := discoverLivepeerWalletAddress(cmd)
		if err != nil {
			return err
		}
		rpc, err := resolveLivepeerRPC(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		client := livepeerchain.NewClient(rpc, nil)
		balance, err := client.ETHBalance(ctx, address)
		if err != nil {
			return fmt.Errorf("read ETH balance: %w", err)
		}
		sender, err := client.GetSenderInfo(ctx, address)
		if err != nil {
			return fmt.Errorf("read TicketBroker sender info: %w", err)
		}
		out := cmd.OutOrStdout()
		ux.Heading(out, "Livepeer gateway wallet")
		fmt.Fprintf(out, "Address:        %s\n", address)
		fmt.Fprintf(out, "ETH Balance:    %s ETH\n", livepeerchain.WeiToETH(balance))
		fmt.Fprintf(out, "Deposit:        %s ETH\n", livepeerchain.WeiToETH(sender.Deposit))
		fmt.Fprintf(out, "Reserve:        %s ETH\n", livepeerchain.WeiToETH(sender.Reserve))
		fmt.Fprintf(out, "Withdraw Round: %s\n", sender.WithdrawRound)
		return nil
	}}
}

func newLivepeerWalletCmd() *cobra.Command {
	wallet := &cobra.Command{Use: "wallet", Short: "Read gateway wallet information from discovery and Arbitrum"}
	wallet.AddCommand(&cobra.Command{Use: "address", Short: "Show the discovered gateway wallet address", RunE: func(cmd *cobra.Command, _ []string) error {
		address, err := discoverLivepeerWalletAddress(cmd)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), address)
		return nil
	}})
	wallet.AddCommand(&cobra.Command{Use: "balance", Short: "Read the gateway wallet ETH balance from Arbitrum", RunE: func(cmd *cobra.Command, _ []string) error {
		address, err := discoverLivepeerWalletAddress(cmd)
		if err != nil {
			return err
		}
		rpc, err := resolveLivepeerRPC(cmd)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		balance, err := livepeerchain.NewClient(rpc, nil).ETHBalance(ctx, address)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s ETH\n", livepeerchain.WeiToETH(balance))
		return nil
	}})
	return wallet
}

func newLivepeerDepositCmd() *cobra.Command {
	deposit := &cobra.Command{Use: "deposit", Short: "Exceptional TicketBroker maintenance (routine funding is Purser-owned)"}
	var reserveAmount string
	reserve := &cobra.Command{Use: "reserve", Short: "Add reserve through a temporarily enabled loopback tx route", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		amount := strings.TrimSpace(reserveAmount)
		parsed, ok := new(big.Int).SetString(amount, 10)
		if !ok || parsed.Sign() < 0 {
			return fmt.Errorf("--amount must be a non-negative integer number of wei")
		}
		return runLivepeerMutation(cmd, "/fundDepositAndReserve", []string{"depositAmount=0", "reserveAmount=" + amount})
	}}
	reserve.Flags().StringVar(&reserveAmount, "amount", "", "reserve amount in wei")
	if err := reserve.MarkFlagRequired("amount"); err != nil {
		panic(err)
	}
	deposit.AddCommand(reserve)
	return deposit
}

func newLivepeerSimpleMutationCmd(use, route string) *cobra.Command {
	return &cobra.Command{Use: use, Short: "Run an exceptional wallet mutation over SSH to the loopback CLI", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runLivepeerMutation(cmd, route, nil)
	}}
}

type livepeerMutationTarget struct {
	ServiceName string
	Host        inventory.Host
	Port        int
	SSHKey      string
}

func resolveLivepeerMutationTarget(cmd *cobra.Command) (livepeerMutationTarget, func(), error) {
	rc, err := resolveClusterManifest(cmd)
	if err != nil {
		return livepeerMutationTarget{}, func() {}, err
	}
	hostFlag, err := cmd.Flags().GetString("host")
	if err != nil {
		rc.Cleanup()
		return livepeerMutationTarget{}, func() {}, err
	}
	clusterFlag, err := cmd.Flags().GetString("cluster")
	if err != nil {
		rc.Cleanup()
		return livepeerMutationTarget{}, func() {}, err
	}
	names := make([]string, 0, len(rc.Manifest.Services))
	for name := range rc.Manifest.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := rc.Manifest.Services[name]
		if !svc.Enabled || !serviceDeployMatches(name, svc, "livepeer-gateway") {
			continue
		}
		if clusterFlag != "" && svc.Cluster != clusterFlag && !slices.Contains(svc.Clusters, clusterFlag) {
			continue
		}
		hosts := serviceHosts(svc)
		selected := ""
		if hostFlag != "" && slices.Contains(hosts, hostFlag) {
			selected = hostFlag
		} else if hostFlag == "" && len(hosts) > 0 {
			selected = hosts[0]
		}
		if selected == "" {
			continue
		}
		host, ok := rc.Manifest.GetHost(selected)
		if !ok {
			continue
		}
		port := 7935
		if cliAddr := strings.TrimSpace(svc.Config["cli_addr"]); cliAddr != "" {
			_, rawPort, splitErr := net.SplitHostPort(cliAddr)
			if splitErr != nil {
				return livepeerMutationTarget{}, rc.Cleanup, fmt.Errorf("service %s has invalid cli_addr %q", name, cliAddr)
			}
			port, err = strconv.Atoi(rawPort)
			if err != nil {
				return livepeerMutationTarget{}, rc.Cleanup, err
			}
		}
		sshKey, err := cmd.Flags().GetString("ssh-key")
		if err != nil {
			rc.Cleanup()
			return livepeerMutationTarget{}, func() {}, err
		}
		return livepeerMutationTarget{ServiceName: name, Host: host, Port: port, SSHKey: sshKey}, rc.Cleanup, nil
	}
	rc.Cleanup()
	return livepeerMutationTarget{}, func() {}, fmt.Errorf("no matching livepeer-gateway host found in the manifest")
}

func livepeerMutationCurlArgs(port int, route string, fields []string) []string {
	args := []string{"-sS", "-X", http.MethodPost, "-w", "\n%{http_code}"}
	for _, field := range fields {
		args = append(args, "--data-urlencode", field)
	}
	return append(args, fmt.Sprintf("http://127.0.0.1:%d%s", port, route))
}

func runLivepeerMutation(cmd *cobra.Command, route string, fields []string) error {
	target, cleanup, err := resolveLivepeerMutationTarget(cmd)
	if err != nil {
		return err
	}
	defer cleanup()
	stdout, err := executeLivepeerCurl(cmd.Context(), target, livepeerMutationCurlArgs(target.Port, route, fields))
	if err != nil {
		return err
	}
	body, status, err := splitCurlStatus(stdout)
	if err != nil {
		return err
	}
	if err := livepeerMutationHTTPError(status, body, target.ServiceName); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), body)
	return nil
}

func livepeerMutationHTTPError(status int, body, serviceName string) error {
	if status == http.StatusNotFound {
		return fmt.Errorf("tx routes disabled; set enable_cli_tx_routes: \"true\" on %s in the manifest, provision, retry, then remove it", serviceName)
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("gateway CLI returned HTTP %d: %s", status, body)
	}
	return nil
}

func executeLivepeerCurl(ctx context.Context, target livepeerMutationTarget, curlArgs []string) (string, error) {
	if target.Host.ExternalIP == "" || target.Host.ExternalIP == "localhost" || target.Host.ExternalIP == "127.0.0.1" {
		out, err := exec.CommandContext(ctx, "curl", curlArgs...).CombinedOutput()
		return string(out), err
	}
	client, err := ssh.NewClient(&ssh.ConnectionConfig{Address: target.Host.ExternalIP, Port: 22, User: target.Host.User, KeyPath: target.SSHKey, HostName: target.Host.Name, Timeout: 30 * time.Second})
	if err != nil {
		return "", err
	}
	defer client.Close()
	parts := []string{"curl"}
	for _, arg := range curlArgs {
		parts = append(parts, ssh.ShellQuote(arg))
	}
	result, err := client.Run(ctx, strings.Join(parts, " "))
	if err != nil {
		return "", err
	}
	return result.Stdout, nil
}

func splitCurlStatus(output string) (string, int, error) {
	output = strings.TrimSpace(output)
	idx := strings.LastIndexByte(output, '\n')
	if idx < 0 {
		return "", 0, fmt.Errorf("gateway CLI response did not include an HTTP status")
	}
	status, err := strconv.Atoi(strings.TrimSpace(output[idx+1:]))
	if err != nil {
		return "", 0, fmt.Errorf("invalid gateway CLI HTTP status: %w", err)
	}
	return strings.TrimSpace(output[:idx]), status, nil
}
