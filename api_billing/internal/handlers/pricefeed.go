package handlers

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	"github.com/shopspring/decimal"
)

const (
	// priceFeedCacheTTL is how long a successful Chainlink read is reused
	// before we re-query the aggregator. Short enough that quotes track
	// market within a minute; long enough to avoid hammering RPC on every
	// CreateCryptoTopup call.
	priceFeedCacheTTL = 60 * time.Second

	// priceFeedStaleness is the maximum age of `latestRoundData.updatedAt`
	// we'll accept. Chainlink ETH/USD heartbeats are well under an hour;
	// anything older means the aggregator stopped updating and the price
	// is not safe to credit against.
	priceFeedStaleness = 1 * time.Hour
)

// AssetPrice is a USD-denominated price for an asset at a point in time.
type AssetPrice struct {
	PriceUSD  decimal.Decimal // USD per 1 whole token
	UpdatedAt time.Time       // round timestamp from the aggregator (or time.Now() for one_to_one)
	Source    string          // "chainlink" | "one_to_one"
}

// PriceFeed reads asset USD prices from on-chain Chainlink aggregators.
// USDC short-circuits to a 1:1 USD price without an RPC call.
type PriceFeed struct {
	rpc    *RPCClient
	logger logging.Logger

	cacheMu sync.RWMutex
	cache   map[string]cachedPrice
}

type cachedPrice struct {
	price     *AssetPrice
	fetchedAt time.Time
}

// NewPriceFeed constructs a PriceFeed sharing the given RPC client.
func NewPriceFeed(rpc *RPCClient, logger logging.Logger) *PriceFeed {
	return &PriceFeed{
		rpc:    rpc,
		logger: logger,
		cache:  make(map[string]cachedPrice),
	}
}

// GetAssetUSDPrice returns the USD price for `asset` on `network`.
//
// USDC always returns 1.0 with source "one_to_one" (no RPC). ETH reads the
// Chainlink aggregator pinned in network.PriceFeeds. Any other asset (LPT)
// returns an error — there is no Chainlink LPT/USD feed today.
//
// Successful reads are cached per (network, asset) for priceFeedCacheTTL.
// Stale or invalid rounds are rejected; cache misses on those propagate as
// errors so the calling code never serves a stale or wrong price.
func (p *PriceFeed) GetAssetUSDPrice(ctx context.Context, network NetworkConfig, asset string) (*AssetPrice, error) {
	if asset == "USDC" {
		return &AssetPrice{
			PriceUSD:  decimal.NewFromInt(1),
			UpdatedAt: time.Now(),
			Source:    "one_to_one",
		}, nil
	}

	cacheKey := network.Name + "/" + asset

	p.cacheMu.RLock()
	cached, ok := p.cache[cacheKey]
	p.cacheMu.RUnlock()
	if ok && time.Since(cached.fetchedAt) < priceFeedCacheTTL {
		return cached.price, nil
	}

	aggregator := network.PriceFeeds[asset]
	if aggregator == "" {
		return nil, fmt.Errorf("no Chainlink price feed for %s on %s", asset, network.Name)
	}

	price, err := p.readChainlinkPrice(ctx, network, aggregator)
	if err != nil {
		p.logger.WithFields(logging.Fields{
			"network":    network.Name,
			"asset":      asset,
			"aggregator": aggregator,
			"error":      err,
		}).Warn("Chainlink price read failed")
		return nil, err
	}

	p.cacheMu.Lock()
	p.cache[cacheKey] = cachedPrice{price: price, fetchedAt: time.Now()}
	p.cacheMu.Unlock()

	return price, nil
}

// readChainlinkPrice does eth_call against the aggregator's decimals() and
// latestRoundData() functions and assembles a validated AssetPrice.
func (p *PriceFeed) readChainlinkPrice(ctx context.Context, network NetworkConfig, aggregator string) (*AssetPrice, error) {
	decimals, err := p.readAggregatorDecimals(ctx, network, aggregator)
	if err != nil {
		return nil, fmt.Errorf("decimals(): %w", err)
	}

	answer, updatedAt, err := p.readLatestRoundData(ctx, network, aggregator)
	if err != nil {
		return nil, fmt.Errorf("latestRoundData(): %w", err)
	}

	if answer.Sign() <= 0 {
		return nil, fmt.Errorf("non-positive price answer: %s", answer.String())
	}
	if updatedAt.IsZero() {
		return nil, fmt.Errorf("price feed updatedAt is zero")
	}
	if age := time.Since(updatedAt); age > priceFeedStaleness {
		return nil, fmt.Errorf("price feed stale: %s old", age)
	}

	priceUSD := decimal.NewFromBigInt(answer, -int32(decimals))

	return &AssetPrice{
		PriceUSD:  priceUSD,
		UpdatedAt: updatedAt,
		Source:    "chainlink",
	}, nil
}

// readAggregatorDecimals calls decimals() (selector 0x313ce567) on a
// Chainlink aggregator and returns its return value as uint8.
func (p *PriceFeed) readAggregatorDecimals(ctx context.Context, network NetworkConfig, aggregator string) (uint8, error) {
	var result string
	err := p.rpc.Call(ctx, network, "eth_call", []any{
		map[string]string{
			"to":   aggregator,
			"data": "0x313ce567",
		},
		"latest",
	}, &result)
	if err != nil {
		return 0, err
	}

	raw, err := hex.DecodeString(strings.TrimPrefix(result, "0x"))
	if err != nil {
		return 0, fmt.Errorf("invalid hex: %w", err)
	}
	if len(raw) != 32 {
		return 0, fmt.Errorf("unexpected decimals response length: got %d, want 32", len(raw))
	}
	// uint8 is right-padded to a 32-byte word; the value lives in the last byte.
	return raw[31], nil
}

// readLatestRoundData calls latestRoundData() (selector 0xfeaf968c) on a
// Chainlink aggregator and returns the signed price answer and the round's
// updatedAt timestamp. Rejects rounds where answeredInRound < roundId
// (incomplete round).
//
// ABI:
//
//	function latestRoundData() returns (
//	    uint80 roundId, int256 answer, uint256 startedAt,
//	    uint256 updatedAt, uint80 answeredInRound)
func (p *PriceFeed) readLatestRoundData(ctx context.Context, network NetworkConfig, aggregator string) (*big.Int, time.Time, error) {
	var result string
	err := p.rpc.Call(ctx, network, "eth_call", []any{
		map[string]string{
			"to":   aggregator,
			"data": "0xfeaf968c",
		},
		"latest",
	}, &result)
	if err != nil {
		return nil, time.Time{}, err
	}

	raw, err := hex.DecodeString(strings.TrimPrefix(result, "0x"))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("invalid hex: %w", err)
	}
	// Five 32-byte words: roundId, answer, startedAt, updatedAt, answeredInRound
	if len(raw) != 160 {
		return nil, time.Time{}, fmt.Errorf("unexpected response length: got %d, want 160", len(raw))
	}

	roundID := new(big.Int).SetBytes(raw[0:32])
	answer := signed256FromBytes(raw[32:64])
	updatedAtBig := new(big.Int).SetBytes(raw[96:128])
	answeredInRound := new(big.Int).SetBytes(raw[128:160])

	if answeredInRound.Cmp(roundID) < 0 {
		return nil, time.Time{}, fmt.Errorf("incomplete round: answeredInRound=%s < roundId=%s",
			answeredInRound.String(), roundID.String())
	}

	if !updatedAtBig.IsInt64() {
		return nil, time.Time{}, fmt.Errorf("updatedAt out of int64 range: %s", updatedAtBig.String())
	}
	return answer, time.Unix(updatedAtBig.Int64(), 0), nil
}

// signed256FromBytes interprets a 32-byte big-endian two's-complement integer.
func signed256FromBytes(b []byte) *big.Int {
	n := new(big.Int).SetBytes(b)
	if len(b) == 32 && b[0]&0x80 != 0 {
		// Top bit set → negative; subtract 2^256.
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), 256))
	}
	return n
}
