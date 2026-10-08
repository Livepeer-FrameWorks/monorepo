// Package livepeer implements the public Livepeer contract and discovery adapter.
package livepeer

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

const contractABI = `[
 {"type":"function","name":"getContract","inputs":[{"type":"bytes32","name":"id"}],"outputs":[{"type":"address"}],"stateMutability":"view"},
 {"type":"function","name":"getFirstTranscoderInPool","inputs":[],"outputs":[{"type":"address"}],"stateMutability":"view"},
 {"type":"function","name":"getNextTranscoderInPool","inputs":[{"type":"address","name":"addr"}],"outputs":[{"type":"address"}],"stateMutability":"view"},
 {"type":"function","name":"isActiveTranscoder","inputs":[{"type":"address","name":"addr"}],"outputs":[{"type":"bool"}],"stateMutability":"view"},
 {"type":"function","name":"getServiceURI","inputs":[{"type":"address","name":"addr"}],"outputs":[{"type":"string"}],"stateMutability":"view"}
]`

type ChainReader interface {
	ChainID(context.Context) (*big.Int, error)
	BlockNumber(context.Context) (uint64, error)
	CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error)
}
type Catalog struct {
	Client          ChainReader
	Controller      common.Address
	Environment     string
	ExpectedNetwork string
}

func DialCatalog(ctx context.Context, rpc, controller, network, environment string) (*Catalog, func(), error) {
	if !common.IsHexAddress(controller) || common.HexToAddress(controller) == (common.Address{}) {
		return nil, nil, fmt.Errorf("LIVEPEER_CONTROLLER must be a nonzero contract address")
	}
	c, err := ethclient.DialContext(ctx, rpc)
	if err != nil {
		return nil, nil, err
	}
	return &Catalog{Client: c, Controller: common.HexToAddress(controller), Environment: environment, ExpectedNetwork: network}, c.Close, nil
}
func (c *Catalog) List(ctx context.Context) ([]networkobservation.Resource, error) {
	network, err := c.Client.ChainID(ctx)
	if err != nil {
		return nil, err
	}
	if c.ExpectedNetwork != "" && network.String() != c.ExpectedNetwork {
		return nil, fmt.Errorf("livepeer RPC network mismatch")
	}
	block, err := c.Client.BlockNumber(ctx)
	if err != nil {
		return nil, err
	}
	at := new(big.Int).SetUint64(block)
	parsed, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil, err
	}
	call := func(to common.Address, method string, args ...any) (any, error) {
		data, packErr := parsed.Pack(method, args...)
		if packErr != nil {
			return nil, packErr
		}
		result, callErr := c.Client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, at)
		if callErr != nil {
			return nil, fmt.Errorf("%s: %w", method, callErr)
		}
		values, unpackErr := parsed.Unpack(method, result)
		if unpackErr != nil || len(values) != 1 {
			return nil, fmt.Errorf("invalid %s response", method)
		}
		return values[0], nil
	}
	bondValue, err := call(c.Controller, "getContract", crypto.Keccak256Hash([]byte("BondingManager")))
	if err != nil {
		return nil, err
	}
	registryValue, err := call(c.Controller, "getContract", crypto.Keccak256Hash([]byte("ServiceRegistry")))
	if err != nil {
		return nil, err
	}
	bonding, bondOK := bondValue.(common.Address)
	registry, registryOK := registryValue.(common.Address)
	if !bondOK || !registryOK {
		return nil, fmt.Errorf("invalid contract address response")
	}
	if bonding == (common.Address{}) || registry == (common.Address{}) {
		return nil, fmt.Errorf("missing Livepeer contract addresses")
	}
	first, err := call(bonding, "getFirstTranscoderInPool")
	if err != nil {
		return nil, err
	}
	current, firstOK := first.(common.Address)
	if !firstOK {
		return nil, fmt.Errorf("invalid transcoder pool response")
	}
	seen := map[common.Address]bool{}
	out := []networkobservation.Resource{}
	now := time.Now().UTC()
	for current != (common.Address{}) {
		if seen[current] || len(seen) >= 10000 {
			return nil, fmt.Errorf("invalid or oversized transcoder pool")
		}
		seen[current] = true
		active, err := call(bonding, "isActiveTranscoder", current)
		if err != nil {
			return nil, err
		}
		uri, err := call(registry, "getServiceURI", current)
		if err != nil {
			return nil, err
		}
		raw, uriOK := uri.(string)
		isActive, activeOK := active.(bool)
		if !uriOK || !activeOK {
			return nil, fmt.Errorf("invalid transcoder metadata response")
		}
		u, e := url.Parse(raw)
		if e == nil && u.Hostname() != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") {
			out = append(out, networkobservation.Resource{Provider: "livepeer", Network: network.String(), Environment: c.Environment, ID: strings.ToLower(current.Hex()), URI: raw, Active: isActive, CatalogSeen: now})
		}
		next, err := call(bonding, "getNextTranscoderInPool", current)
		if err != nil {
			return nil, err
		}
		var nextOK bool
		current, nextOK = next.(common.Address)
		if !nextOK {
			return nil, fmt.Errorf("invalid next transcoder response")
		}
	}
	return out, nil
}
