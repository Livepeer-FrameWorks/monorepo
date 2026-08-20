package handlers

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ValidateGasRunway proves that an EVM account can fund one transaction at
// the same conservative EIP-1559 fee ceiling used by settlement and sweeping.
func ValidateGasRunway(ctx context.Context, rpc *RPCClient, network NetworkConfig, address string, gasLimit uint64) error {
	if rpc == nil {
		return fmt.Errorf("%s RPC client is unavailable", network.Name)
	}
	if !common.IsHexAddress(address) || common.HexToAddress(address) == (common.Address{}) {
		return fmt.Errorf("%s gas address is invalid", network.Name)
	}
	readQuantity := func(method string, params []any) (*big.Int, error) {
		var encoded string
		if err := rpc.Call(ctx, network, method, params, &encoded); err != nil {
			return nil, err
		}
		value := new(big.Int)
		if _, ok := value.SetString(strings.TrimPrefix(encoded, "0x"), 16); !ok {
			return nil, fmt.Errorf("%s returned an invalid quantity", method)
		}
		return value, nil
	}
	balance, err := readQuantity("eth_getBalance", []any{address, "latest"})
	if err != nil {
		return fmt.Errorf("read %s gas balance: %w", network.Name, err)
	}
	gasPrice, err := readQuantity("eth_gasPrice", nil)
	if err != nil {
		return fmt.Errorf("read %s gas price: %w", network.Name, err)
	}
	priorityFee, err := readQuantity("eth_maxPriorityFeePerGas", nil)
	if err != nil {
		return fmt.Errorf("read %s priority fee: %w", network.Name, err)
	}
	maxFee := new(big.Int).Add(new(big.Int).Mul(gasPrice, big.NewInt(2)), priorityFee)
	required := new(big.Int).Mul(maxFee, new(big.Int).SetUint64(gasLimit))
	if balance.Cmp(required) < 0 {
		return fmt.Errorf("%s gas balance %s wei is below one-transaction requirement %s wei", network.Name, balance, required)
	}
	return nil
}
