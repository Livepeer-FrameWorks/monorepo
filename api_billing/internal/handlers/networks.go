package handlers

import (
	"os"
)

// NetworkConfig holds configuration for a blockchain network
type NetworkConfig struct {
	ChainID        int64
	Name           string // "ethereum", "base", "arbitrum", etc.
	DisplayName    string // "Ethereum Mainnet", "Base", etc.
	RPCEndpointEnv string // Environment variable name for RPC endpoint
	ExplorerAPIURL string // Block explorer API base URL
	ExplorerAPIEnv string // Environment variable name for explorer API key
	USDCContract   string // USDC contract address on this network
	LPTContract    string // LPT contract address (empty if not available)
	// PriceFeeds maps an asset symbol ("ETH", "LPT") to its Chainlink
	// aggregator address on this network. USDC is always 1:1 USD and has
	// no entry. Empty/missing entries mean the asset has no on-chain price
	// feed on this network and prepaid top-ups for it must be gated.
	PriceFeeds    map[string]string
	Confirmations int  // Required confirmations for deposits
	X402Enabled   bool // Whether x402 settlement is supported on this network
	IsTestnet     bool // Whether this is a testnet
}

// TokenDecimals returns the number of decimals for an asset's smallest unit
// (i.e. base units → whole tokens divisor exponent).
//
// USDC is 6-decimal on every supported chain; ETH (native) and LPT are
// 18-decimal.
func TokenDecimals(asset string) (int32, bool) {
	switch asset {
	case "ETH", "LPT":
		return 18, true
	case "USDC":
		return 6, true
	default:
		return 0, false
	}
}

// GetRPCEndpoint returns the RPC endpoint from environment
func (n NetworkConfig) GetRPCEndpoint() string {
	return os.Getenv(n.RPCEndpointEnv)
}

// GetExplorerAPIKey returns the explorer API key from environment
func (n NetworkConfig) GetExplorerAPIKey() string {
	return os.Getenv(n.ExplorerAPIEnv)
}

// Networks is the registry of all supported networks
//
// Chainlink ETH/USD aggregator addresses are the standard EACAggregatorProxy
// addresses verified against https://data.chain.link/feeds and the relevant
// chain's block explorer (Etherscan/Arbiscan/Basescan label them as
// "Chainlink: ETH/USD Price Feed" or equivalent). LPT/USD is intentionally
// absent: Chainlink does not publish an official LPT/USD aggregator, so LPT
// prepaid top-ups stay gated.
var Networks = map[string]NetworkConfig{
	// Mainnets
	"ethereum": {
		ChainID:        1,
		Name:           "ethereum",
		DisplayName:    "Ethereum Mainnet",
		RPCEndpointEnv: "ETH_RPC_ENDPOINT",
		ExplorerAPIURL: "https://api.etherscan.io/api",
		ExplorerAPIEnv: "ETHERSCAN_API_KEY",
		USDCContract:   "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
		LPTContract:    "0x58b6A8A3302369DAEc383334672404Ee733aB239",
		PriceFeeds: map[string]string{
			"ETH": "0x5f4eC3Df9cbd43714FE2740f5E3616155c5b8419",
		},
		Confirmations: 12,
		X402Enabled:   false, // Too expensive for x402
		IsTestnet:     false,
	},
	"base": {
		ChainID:        8453,
		Name:           "base",
		DisplayName:    "Base",
		RPCEndpointEnv: "BASE_RPC_ENDPOINT",
		ExplorerAPIURL: "https://api.basescan.org/api",
		ExplorerAPIEnv: "BASESCAN_API_KEY",
		USDCContract:   "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913",
		LPTContract:    "", // No LPT on Base
		PriceFeeds: map[string]string{
			"ETH": "0x71041dDDad3595F9CEd3DcCFBe3D1F4b0a16Bb70",
		},
		Confirmations: 10,
		X402Enabled:   true,
		IsTestnet:     false,
	},
	"arbitrum": {
		ChainID:        42161,
		Name:           "arbitrum",
		DisplayName:    "Arbitrum One",
		RPCEndpointEnv: "ARBITRUM_RPC_ENDPOINT",
		ExplorerAPIURL: "https://api.arbiscan.io/api",
		ExplorerAPIEnv: "ARBISCAN_API_KEY",
		USDCContract:   "0xaf88d065e77c8cC2239327C5EDb3A432268e5831",
		LPTContract:    "0x289ba1701C2F088cf0faf8B3705246331cB8A839", // Livepeer migrated to Arbitrum
		PriceFeeds: map[string]string{
			"ETH": "0x639Fe6ab55C921f74e7fac1ee960C0B6293ba612",
		},
		Confirmations: 10,
		X402Enabled:   true,
		IsTestnet:     false,
	},

	// Testnets
	"base-sepolia": {
		ChainID:        84532,
		Name:           "base-sepolia",
		DisplayName:    "Base Sepolia",
		RPCEndpointEnv: "BASE_SEPOLIA_RPC_ENDPOINT",
		ExplorerAPIURL: "https://api-sepolia.basescan.org/api",
		ExplorerAPIEnv: "BASESCAN_API_KEY", // Same key works for testnet
		USDCContract:   "0x036CbD53842c5426634e7929541eC2318f3dCF7e",
		LPTContract:    "",
		Confirmations:  5,
		X402Enabled:    true,
		IsTestnet:      true,
	},
	"arbitrum-sepolia": {
		ChainID:        421614,
		Name:           "arbitrum-sepolia",
		DisplayName:    "Arbitrum Sepolia",
		RPCEndpointEnv: "ARBITRUM_SEPOLIA_RPC_ENDPOINT",
		ExplorerAPIURL: "https://api-sepolia.arbiscan.io/api",
		ExplorerAPIEnv: "ARBISCAN_API_KEY", // Same key works for testnet
		USDCContract:   "0x75faf114eafb1BDbe2F0316DF893fd58CE46AA4d",
		LPTContract:    "",
		Confirmations:  5,
		X402Enabled:    true,
		IsTestnet:      true,
	},
}

// NetworkByChainID returns the network config for a given chain ID
func NetworkByChainID(chainID int64) (NetworkConfig, bool) {
	for _, n := range Networks {
		if n.ChainID == chainID {
			return n, true
		}
	}
	return NetworkConfig{}, false
}

// X402Networks returns all networks that support x402
func X402Networks(includeTestnets bool) []NetworkConfig {
	var networks []NetworkConfig
	for _, n := range Networks {
		if n.X402Enabled && (includeTestnets || !n.IsTestnet) {
			networks = append(networks, n)
		}
	}
	return networks
}

// DepositNetworks returns all networks that support deposits
func DepositNetworks(includeTestnets bool) []NetworkConfig {
	var networks []NetworkConfig
	for _, n := range Networks {
		if includeTestnets || !n.IsTestnet {
			networks = append(networks, n)
		}
	}
	return networks
}

// DefaultX402Network returns the default network for x402 (Base mainnet)
func DefaultX402Network() NetworkConfig {
	return Networks["base"]
}

// DefaultRPCEndpoints returns sensible defaults for public RPC endpoints
var DefaultRPCEndpoints = map[string]string{
	"ETH_RPC_ENDPOINT":              "https://eth.publicnode.com",
	"BASE_RPC_ENDPOINT":             "https://base.publicnode.com",
	"ARBITRUM_RPC_ENDPOINT":         "https://arb1.arbitrum.io/rpc",
	"BASE_SEPOLIA_RPC_ENDPOINT":     "https://base-sepolia.publicnode.com",
	"ARBITRUM_SEPOLIA_RPC_ENDPOINT": "https://sepolia-rollup.arbitrum.io/rpc",
}

// GetRPCEndpointWithDefault returns the RPC endpoint, falling back to default
func (n NetworkConfig) GetRPCEndpointWithDefault() string {
	if endpoint := os.Getenv(n.RPCEndpointEnv); endpoint != "" {
		return endpoint
	}
	return DefaultRPCEndpoints[n.RPCEndpointEnv]
}
