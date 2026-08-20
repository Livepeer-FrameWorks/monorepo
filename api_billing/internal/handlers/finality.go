package handlers

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

type FinalityHead struct {
	Number int64
	Hash   string
	Tag    string
}

// rpcBlockHeader is used with eth_getBlockByNumber(..., false). A compliant
// node returns transaction hashes in that response, so decoding it into the
// scanner's full-transaction rpcBlock shape would reject healthy RPC data.
type rpcBlockHeader struct {
	Number string `json:"number"`
	Hash   string `json:"hash"`
}

func cryptoNetworkEnvKey(prefix, network string) string {
	return prefix + "_" + strings.ToUpper(strings.ReplaceAll(network, "-", "_"))
}

// ValidateCryptoCustodyNetwork proves that received funds on one network have
// a configured exit path before a new deposit or x402 quote is advertised.
func ValidateCryptoCustodyNetwork(ctx context.Context, rpc *RPCClient, network NetworkConfig, asset string) error {
	if _, err := GetFinalityHead(ctx, rpc, network); err != nil {
		return err
	}
	treasuryKey := cryptoNetworkEnvKey("CRYPTO_TREASURY", network.Name)
	if treasury := strings.TrimSpace(os.Getenv(treasuryKey)); !common.IsHexAddress(treasury) || common.HexToAddress(treasury) == (common.Address{}) {
		return fmt.Errorf("%s must contain a valid non-zero EVM address", treasuryKey)
	}
	if strings.EqualFold(asset, "USDC") {
		relayerKey := cryptoNetworkEnvKey("CRYPTO_SWEEP_RELAYER_PRIVATE_KEY", network.Name)
		if _, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv(relayerKey)), "0x")); err != nil {
			return fmt.Errorf("%s must contain the dedicated gas-relayer key", relayerKey)
		}
	}
	return nil
}

// GetFinalityHead resolves the chain's consensus-labelled finalized head.
// Credit-releasing operations never accept a configurable weaker head or a
// latest-minus-confirmations fallback.
func GetFinalityHead(ctx context.Context, rpc *RPCClient, network NetworkConfig) (FinalityHead, error) {
	if rpc == nil {
		return FinalityHead{}, fmt.Errorf("RPC client unavailable")
	}
	const tag = "finalized"
	var block rpcBlockHeader
	if err := rpc.Call(ctx, network, "eth_getBlockByNumber", []any{tag, false}, &block); err != nil {
		return FinalityHead{}, fmt.Errorf("resolve %s head: %w", tag, err)
	}
	number := parseHexInt64(block.Number)
	if number <= 0 || len(block.Hash) != 66 || !strings.HasPrefix(block.Hash, "0x") {
		return FinalityHead{}, fmt.Errorf("RPC returned no usable %s head", tag)
	}
	return FinalityHead{Number: number, Hash: block.Hash, Tag: tag}, nil
}
