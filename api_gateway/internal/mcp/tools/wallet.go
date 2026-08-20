package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterWalletTools registers wallet bootstrap and linked-wallet management.
// Wallet ownership establishes identity; these tools never settle payment.
func RegisterWalletTools(server *mcp.Server, serviceClients *clients.ServiceClients, logger logging.Logger) {
	addTool(server, &mcp.Tool{
		Name:        "request_wallet_challenge",
		Description: "Request a five-minute, single-use EIP-191 wallet challenge. Sign the returned message verbatim, then reconnect once with X-Wallet-Address, X-Wallet-Message, and X-Wallet-Signature. This is authentication, not x402 payment.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args RequestWalletChallengeInput) (*mcp.CallToolResult, any, error) {
		return handleRequestWalletChallenge(ctx, args, serviceClients, logger)
	})

	addTool(server, &mcp.Tool{
		Name:        "list_linked_wallets",
		Description: "List wallets linked to the authenticated user. Available at zero prepaid balance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ ListLinkedWalletsInput) (*mcp.CallToolResult, any, error) {
		return handleListLinkedWallets(ctx, serviceClients, logger)
	})

	addTool(server, &mcp.Tool{
		Name:        "link_wallet",
		Description: "Link another wallet to the authenticated user using a fresh request_wallet_challenge message and its EIP-191 signature. Available at zero prepaid balance.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args LinkWalletInput) (*mcp.CallToolResult, any, error) {
		return handleLinkWallet(ctx, args, serviceClients, logger)
	})

	addTool(server, &mcp.Tool{
		Name:        "unlink_wallet",
		Description: "Unlink an owned wallet by wallet ID. The final sign-in method cannot be removed unless a verified password sign-in is active.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args UnlinkWalletInput) (*mcp.CallToolResult, any, error) {
		return handleUnlinkWallet(ctx, args, serviceClients, logger)
	})
}

type RequestWalletChallengeInput struct {
	Address string `json:"address" jsonschema:"0x-prefixed Ethereum wallet address"`
	ChainID uint64 `json:"chain_id" jsonschema:"Supported EVM chain ID: 1, 8453, or 42161"`
}

type WalletChallengeResult struct {
	Message   string `json:"message"`
	ExpiresAt string `json:"expires_at"`
	NextStep  string `json:"next_step"`
}

type ListLinkedWalletsInput struct{}

type LinkedWalletResult struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	CreatedAt  string `json:"created_at,omitempty"`
	LastAuthAt string `json:"last_auth_at,omitempty"`
}

type LinkedWalletsResult struct {
	Wallets []LinkedWalletResult `json:"wallets"`
	Message string               `json:"message"`
}

type LinkWalletInput struct {
	Address   string `json:"address" jsonschema:"Wallet address used to request the challenge"`
	Message   string `json:"message" jsonschema:"Exact server-issued challenge message"`
	Signature string `json:"signature" jsonschema:"EIP-191 personal_sign signature over message"`
}

type UnlinkWalletInput struct {
	WalletID string `json:"wallet_id" jsonschema:"Linked wallet UUID returned by list_linked_wallets"`
}

type WalletMutationResult struct {
	Success bool                `json:"success"`
	Wallet  *LinkedWalletResult `json:"wallet,omitempty"`
	Message string              `json:"message"`
}

func handleRequestWalletChallenge(ctx context.Context, args RequestWalletChallengeInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	address := strings.TrimSpace(args.Address)
	if address == "" || args.ChainID == 0 {
		return toolError("address and chain_id are required")
	}
	resp, err := serviceClients.Commodore.IssueWalletChallenge(ctx, address, args.ChainID)
	if err != nil {
		logger.WithError(err).Warn("Failed to issue MCP wallet challenge")
		return toolError("Failed to issue wallet challenge; verify the address and supported chain")
	}
	if resp == nil || resp.GetMessage() == "" || resp.GetExpiresAt() == nil {
		return toolError("Authentication service returned an incomplete wallet challenge")
	}
	return toolSuccess(WalletChallengeResult{
		Message:   resp.GetMessage(),
		ExpiresAt: resp.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
		NextStep:  "Sign message verbatim with EIP-191 personal_sign, then reconnect once using the X-Wallet-* headers. Save the returned X-Access-Token and use it as a bearer token for subsequent MCP requests.",
	})
}

func handleListLinkedWallets(ctx context.Context, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetUserID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	resp, err := serviceClients.Commodore.ListWallets(ctx)
	if err != nil {
		logger.WithError(err).Warn("Failed to list linked wallets")
		return toolError("Failed to list linked wallets")
	}
	wallets := make([]LinkedWalletResult, 0)
	if resp != nil {
		wallets = make([]LinkedWalletResult, 0, len(resp.GetWallets()))
		for _, wallet := range resp.GetWallets() {
			wallets = append(wallets, linkedWalletResult(wallet))
		}
	}
	return toolSuccess(LinkedWalletsResult{Wallets: wallets, Message: fmt.Sprintf("Loaded %d linked wallet(s).", len(wallets))})
}

func handleLinkWallet(ctx context.Context, args LinkWalletInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetUserID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	address := strings.TrimSpace(args.Address)
	message := strings.TrimSpace(args.Message)
	signature := strings.TrimSpace(args.Signature)
	if address == "" || message == "" || signature == "" {
		return toolError("address, message, and signature are required")
	}
	wallet, err := serviceClients.Commodore.LinkWallet(ctx, address, message, signature)
	if err != nil {
		logger.WithError(err).Warn("Failed to link wallet")
		return toolError("Failed to link wallet; use a fresh challenge and verify the signature or existing ownership")
	}
	result := linkedWalletResult(wallet)
	return toolSuccess(WalletMutationResult{Success: true, Wallet: &result, Message: "Wallet linked."})
}

func handleUnlinkWallet(ctx context.Context, args UnlinkWalletInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetUserID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	walletID := strings.TrimSpace(args.WalletID)
	if walletID == "" {
		return toolError("wallet_id is required")
	}
	resp, err := serviceClients.Commodore.UnlinkWallet(ctx, walletID)
	if err != nil {
		logger.WithError(err).Warn("Failed to unlink wallet")
		return toolError("Failed to unlink wallet; verify ownership and retain another wallet or a verified password sign-in")
	}
	if resp == nil || !resp.GetSuccess() {
		return toolError("Wallet was not unlinked")
	}
	return toolSuccess(WalletMutationResult{Success: true, Message: "Wallet unlinked."})
}

func linkedWalletResult(wallet *commodorepb.WalletIdentity) LinkedWalletResult {
	if wallet == nil {
		return LinkedWalletResult{}
	}
	result := LinkedWalletResult{ID: wallet.GetId(), Address: wallet.GetWalletAddress()}
	if wallet.GetCreatedAt() != nil {
		result.CreatedAt = wallet.GetCreatedAt().AsTime().UTC().Format(time.RFC3339)
	}
	if wallet.GetLastAuthAt() != nil {
		result.LastAuthAt = wallet.GetLastAuthAt().AsTime().UTC().Format(time.RFC3339)
	}
	return result
}
