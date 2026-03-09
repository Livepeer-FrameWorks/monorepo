package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newLivepeerCmd() *cobra.Command {
	lp := &cobra.Command{
		Use:   "livepeer",
		Short: "Livepeer gateway management (status, deposits, wallet)",
	}

	lp.PersistentFlags().String("gateway", "", "gateway address (host:port) — overrides discovery")
	lp.PersistentFlags().String("cluster", "", "cluster ID for discovery")

	lp.AddCommand(newLivepeerStatusCmd())
	lp.AddCommand(newLivepeerDepositCmd())
	lp.AddCommand(newLivepeerWalletCmd())

	return lp
}

// resolveGatewayAddr returns the gateway CLI address. If --gateway is set, use it directly.
// Otherwise, discover via Quartermaster.
func resolveGatewayAddr(cmd *cobra.Command) (string, error) {
	if addr, _ := cmd.Flags().GetString("gateway"); addr != "" {
		return addr, nil
	}

	clusterID, _ := cmd.Flags().GetString("cluster")
	qc, _, err := newQMGRPCClientFromContext()
	if err != nil {
		return "", fmt.Errorf("cannot discover gateway (use --gateway to specify directly): %w", err)
	}
	defer func() { _ = qc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := qc.DiscoverServices(ctx, "livepeer-gateway", clusterID, nil)
	if err != nil {
		return "", fmt.Errorf("discovery failed: %w", err)
	}

	for _, inst := range resp.Instances {
		if inst.Status == "running" && inst.GetHost() != "" && inst.GetPort() != 0 {
			return fmt.Sprintf("%s:%d", inst.GetHost(), inst.GetPort()), nil
		}
	}

	return "", fmt.Errorf("no running livepeer-gateway found (use --gateway to specify directly)")
}

type livepeerStatus struct {
	Manifests                  interface{} `json:"Manifests"`
	Version                    string      `json:"Version"`
	GolangRuntimeVersion       string      `json:"GolangRuntimeVersion"`
	GOArch                     string      `json:"GOArch"`
	GOOS                       string      `json:"GOOS"`
	OrchestratorPool           interface{} `json:"OrchestratorPool"`
	RegisteredTranscodersCount int         `json:"RegisteredTranscodersCount"`
	EthereumAddr               string      `json:"EthereumAddr"`
	TranscodingConfig          interface{} `json:"TranscodingConfig"`
}

func gatewayGET(addr, path string) ([]byte, error) {
	url := fmt.Sprintf("http://%s%s", addr, path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s returned %d: %s", path, resp.StatusCode, string(b))
	}
	return b, nil
}

func gatewayPOST(addr, path, body string) ([]byte, error) {
	url := fmt.Sprintf("http://%s%s", addr, path)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s returned %d: %s", path, resp.StatusCode, string(b))
	}
	return b, nil
}

// --- fw livepeer status ---

func newLivepeerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show gateway status (version, address, sessions, deposit)",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}

			body, err := gatewayGET(addr, "/status")
			if err != nil {
				return fmt.Errorf("GET /status failed: %w", err)
			}

			var status livepeerStatus
			if err = json.Unmarshal(body, &status); err != nil {
				return fmt.Errorf("invalid /status response: %w", err)
			}

			fmt.Printf("Gateway:    %s\n", addr)
			fmt.Printf("Version:    %s\n", status.Version)
			fmt.Printf("ETH Addr:   %s\n", status.EthereumAddr)
			fmt.Printf("Arch:       %s/%s\n", status.GOOS, status.GOArch)

			// Try to get deposit info from /senderInfo
			depositBody, err := gatewayGET(addr, "/senderInfo")
			if err == nil {
				var senderInfo map[string]interface{}
				if json.Unmarshal(depositBody, &senderInfo) == nil {
					if deposit, ok := senderInfo["Deposit"]; ok {
						fmt.Printf("Deposit:    %v\n", deposit)
					}
					if reserve, ok := senderInfo["Reserve"]; ok {
						fmt.Printf("Reserve:    %v\n", reserve)
					}
				}
			}

			return nil
		},
	}
}

// --- fw livepeer deposit ---

func newLivepeerDepositCmd() *cobra.Command {
	dep := &cobra.Command{
		Use:   "deposit",
		Short: "Manage TicketBroker deposit and reserve",
	}
	dep.AddCommand(newLivepeerDepositFundCmd())
	dep.AddCommand(newLivepeerDepositReserveCmd())
	dep.AddCommand(newLivepeerDepositUnlockCmd())
	dep.AddCommand(newLivepeerDepositWithdrawCmd())
	return dep
}

func newLivepeerDepositFundCmd() *cobra.Command {
	var amount string
	cmd := &cobra.Command{
		Use:   "fund",
		Short: "Fund TicketBroker deposit",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayPOST(addr, "/fundDeposit", "amount="+amount)
			if err != nil {
				return fmt.Errorf("POST /fundDeposit failed: %w", err)
			}
			fmt.Printf("Response: %s\n", string(body))
			return nil
		},
	}
	cmd.Flags().StringVar(&amount, "amount", "", "amount in wei")
	_ = cmd.MarkFlagRequired("amount")
	return cmd
}

func newLivepeerDepositReserveCmd() *cobra.Command {
	var depositAmount, reserveAmount string
	cmd := &cobra.Command{
		Use:   "reserve",
		Short: "Fund both deposit and reserve",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayPOST(addr, "/fundDepositAndReserve",
				fmt.Sprintf("depositAmount=%s&penaltyEscrowAmount=%s", depositAmount, reserveAmount))
			if err != nil {
				return fmt.Errorf("POST /fundDepositAndReserve failed: %w", err)
			}
			fmt.Printf("Response: %s\n", string(body))
			return nil
		},
	}
	cmd.Flags().StringVar(&depositAmount, "deposit", "", "deposit amount in wei")
	cmd.Flags().StringVar(&reserveAmount, "reserve", "", "reserve amount in wei")
	_ = cmd.MarkFlagRequired("deposit")
	_ = cmd.MarkFlagRequired("reserve")
	return cmd
}

func newLivepeerDepositUnlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unlock",
		Short: "Start unlock period for deposit and reserve",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayPOST(addr, "/unlock", "")
			if err != nil {
				return fmt.Errorf("POST /unlock failed: %w", err)
			}
			fmt.Printf("Response: %s\n", string(body))
			return nil
		},
	}
}

func newLivepeerDepositWithdrawCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "withdraw",
		Short: "Withdraw unlocked deposit and reserve",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayPOST(addr, "/withdraw", "")
			if err != nil {
				return fmt.Errorf("POST /withdraw failed: %w", err)
			}
			fmt.Printf("Response: %s\n", string(body))
			return nil
		},
	}
}

// --- fw livepeer wallet ---

func newLivepeerWalletCmd() *cobra.Command {
	wallet := &cobra.Command{
		Use:   "wallet",
		Short: "Gateway wallet info",
	}
	wallet.AddCommand(newLivepeerWalletAddressCmd())
	wallet.AddCommand(newLivepeerWalletBalanceCmd())
	return wallet
}

func newLivepeerWalletAddressCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "address",
		Short: "Show the gateway's ETH address",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayGET(addr, "/status")
			if err != nil {
				return fmt.Errorf("GET /status failed: %w", err)
			}
			var status struct {
				EthereumAddr string `json:"EthereumAddr"`
			}
			if err := json.Unmarshal(body, &status); err != nil {
				return err
			}
			fmt.Println(status.EthereumAddr)
			return nil
		},
	}
}

func newLivepeerWalletBalanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "balance",
		Short: "Show the gateway's ETH balance",
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := resolveGatewayAddr(cmd)
			if err != nil {
				return err
			}
			body, err := gatewayGET(addr, "/ethBalance")
			if err != nil {
				return fmt.Errorf("GET /ethBalance failed: %w", err)
			}
			fmt.Printf("ETH Balance: %s\n", strings.TrimSpace(string(body)))
			return nil
		},
	}
}
