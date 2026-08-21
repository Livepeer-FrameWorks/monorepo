//nolint:govet,errcheck // Sweep state transitions keep short-lived error scopes and best-effort audit updates local.
package grpc

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

const sweepCeremonyPrefix = "I_UNDERSTAND:"

type sweepSource struct {
	typeName string
	id       string
	amount   *big.Int
}

type sweepCandidate struct {
	custodyID      string
	walletID       sql.NullString
	asset          string
	address        string
	derivation     uint32
	derivationXpub string
	eligibleAmount *big.Int
	sources        []sweepSource
}

type sweepRPCBlock struct {
	Number        string `json:"number"`
	Hash          string `json:"hash"`
	BaseFeePerGas string `json:"baseFeePerGas"`
}

type sweepRPCReceipt struct {
	TransactionHash string `json:"transactionHash"`
	BlockNumber     string `json:"blockNumber"`
	BlockHash       string `json:"blockHash"`
	Status          string `json:"status"`
}

func requireCryptoSweepOperator(ctx context.Context) error {
	if middleware.IsServiceCall(ctx) || ctxkeys.IsPlatformOperator(ctx) {
		return nil
	}
	return status.Error(codes.PermissionDenied, "platform operator role required for crypto sweep operations")
}

func xpubFingerprint(xpub string) string {
	if strings.TrimSpace(xpub) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(xpub)))
	return hex.EncodeToString(digest[:8])
}

func (s *PurserServer) RotateCryptoDepositKey(ctx context.Context, req *purserpb.RotateCryptoDepositKeyRequest) (*purserpb.RotateCryptoDepositKeyResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	if req == nil || strings.TrimSpace(req.GetXpub()) == "" {
		return nil, status.Error(codes.InvalidArgument, "xpub is required")
	}
	previous, nextIndex, changed, err := s.hdwallet.RotateHDWallet(ctx, strings.TrimSpace(req.GetXpub()), strings.TrimSpace(req.GetNetwork()))
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "rotate crypto deposit key: %v", err)
	}
	s.logger.WithFields(map[string]any{
		"actor_id": ctxkeys.GetUserID(ctx), "previous_xpub_fingerprint": xpubFingerprint(previous),
		"active_xpub_fingerprint": xpubFingerprint(req.GetXpub()), "next_derivation_index": nextIndex, "changed": changed,
	}).Warn("Crypto deposit derivation key rotated; retired offline key must be retained until all historical addresses are drained")
	return &purserpb.RotateCryptoDepositKeyResponse{
		PreviousXpubFingerprint: xpubFingerprint(previous), ActiveXpubFingerprint: xpubFingerprint(req.GetXpub()),
		NextDerivationIndex: nextIndex, Changed: changed,
	}, nil
}

func (s *PurserServer) GetCryptoReadiness(ctx context.Context, _ *emptypb.Empty) (*purserpb.CryptoReadinessResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	response := &purserpb.CryptoReadinessResponse{Ready: true, ProductionEligible: true}
	add := func(component string, err error, success string) {
		check := &purserpb.CryptoReadinessCheck{Component: component, Ready: err == nil, Detail: success}
		if err != nil {
			check.Detail = err.Error()
			response.Ready = false
		}
		response.Checks = append(response.Checks, check)
	}
	if config.CryptoDepositsEnabled() {
		add("direct_deposits.circuit_breaker", nil, "enabled")
	} else {
		add("direct_deposits.circuit_breaker", fmt.Errorf("CRYPTO_DEPOSITS_ENABLED=false"), "")
	}
	if config.X402PaymentsEnabled() {
		add("x402.circuit_breaker", nil, "enabled")
	} else {
		add("x402.circuit_breaker", fmt.Errorf("X402_PAYMENTS_ENABLED=false"), "")
	}
	if s.hasHDWalletXpub(ctx) {
		add("custody.xpub", nil, "initialized")
	} else {
		add("custody.xpub", fmt.Errorf("HD wallet xpub is not initialized"), "")
	}
	add("tax.topup_policy", nil, handlers.CryptoTopupTaxPolicyRef)
	if s.x402handler == nil {
		add("x402.facilitator", fmt.Errorf("x402 handler is unavailable"), "")
	} else {
		add("x402.facilitator", s.x402handler.Readiness(ctx), "official v2 facilitator reachable and supported")
	}
	queries := purserdb.New(s.db)
	currentVATRates, vatRateErr := queries.CountCurrentEUVATRates(ctx)
	if vatRateErr != nil {
		add("tax.vat_rate_catalog", fmt.Errorf("read effective VAT rates: %w", vatRateErr), "")
	} else if currentVATRates != 27 {
		add("tax.vat_rate_catalog", fmt.Errorf("effective VAT rate catalog has %d of 27 EU countries", currentVATRates), "")
	} else {
		add("tax.vat_rate_catalog", nil, "27 effective-dated EU standard rates")
	}
	openAnomalies, anomalyErr := queries.CountOpenCryptoAccountingAnomalies(ctx)
	if anomalyErr != nil {
		add("accounting.crypto_anomaly_queue", fmt.Errorf("read anomaly queue: %w", anomalyErr), "")
	} else if openAnomalies > 0 {
		add("accounting.crypto_anomaly_queue", fmt.Errorf("%d unresolved crypto accounting anomalies", openAnomalies), "")
	} else {
		add("accounting.crypto_anomaly_queue", nil, "no unresolved anomalies")
	}

	for _, network := range handlers.DepositNetworks(config.X402IncludeTestnetsEnabled()) {
		prefix := "network." + network.Name + "."
		_, finalityErr := handlers.GetFinalityHead(ctx, s.rpcClient, network)
		add(prefix+"finality", finalityErr, "consensus finalized head available")
		treasuryErr := handlers.ValidateCryptoCustodyNetwork(ctx, s.rpcClient, network, "ETH")
		add(prefix+"treasury", treasuryErr, "non-zero treasury address configured")
		relayerKey := "CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_" + strings.ToUpper(strings.ReplaceAll(network.Name, "-", "_"))
		_, relayerErr := ethcrypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv(relayerKey)), "0x"))
		if relayerErr != nil {
			relayerErr = fmt.Errorf("%s is missing or invalid", relayerKey)
		} else {
			privateKey, _ := ethcrypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv(relayerKey)), "0x"))
			relayerAddress := ethcrypto.PubkeyToAddress(privateKey.PublicKey).Hex()
			relayerErr = handlers.ValidateGasRunway(ctx, s.rpcClient, network, relayerAddress, 150_000)
		}
		add(prefix+"usdc_relayer", relayerErr, "dedicated relayer key valid and funded for one transaction")

		health, scannerErr := queries.GetCryptoScannerHealth(ctx, network.Name)
		scannedAt, lastError, lag := health.ScannedAt, health.LastError, health.LagBlocks
		if scannerErr == nil && (!scannedAt.Valid || time.Since(scannedAt.Time) > time.Minute) {
			scannerErr = fmt.Errorf("scanner has not committed a batch in the last minute")
		}
		if scannerErr == nil && lastError.Valid && strings.TrimSpace(lastError.String) != "" {
			scannerErr = fmt.Errorf("scanner error: %s", lastError.String)
		}
		if scannerErr == nil && lag.Valid && lag.Int64 > 500 {
			scannerErr = fmt.Errorf("scanner is %d blocks behind", lag.Int64)
		}
		add(prefix+"scanner", scannerErr, fmt.Sprintf("healthy; lag=%d", lag.Int64))
	}
	if !response.Ready {
		response.ProductionEligible = false
	}
	return response, nil
}

func sweepNetwork(name string) (handlers.NetworkConfig, error) {
	network, ok := handlers.Networks[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return handlers.NetworkConfig{}, fmt.Errorf("unsupported network %q", name)
	}
	if network.IsTestnet && !config.X402IncludeTestnetsEnabled() {
		return handlers.NetworkConfig{}, fmt.Errorf("testnet sweep requires explicit testnet enablement")
	}
	return network, nil
}

func parseSweepHex(value string) (*big.Int, error) {
	if !strings.HasPrefix(value, "0x") || len(value) <= 2 {
		return nil, fmt.Errorf("invalid hex quantity %q", value)
	}
	parsed := new(big.Int)
	if _, ok := parsed.SetString(value[2:], 16); !ok || parsed.Sign() < 0 {
		return nil, fmt.Errorf("invalid hex quantity %q", value)
	}
	return parsed, nil
}

func sweepTreasury(network string) (string, error) {
	key := "CRYPTO_TREASURY_" + strings.ToUpper(strings.ReplaceAll(network, "-", "_"))
	address := strings.TrimSpace(os.Getenv(key))
	if !common.IsHexAddress(address) || common.HexToAddress(address) == (common.Address{}) {
		return "", fmt.Errorf("%s must contain a valid non-zero EVM address", key)
	}
	return strings.ToLower(common.HexToAddress(address).Hex()), nil
}

func caip2ForSweep(network handlers.NetworkConfig) string {
	return fmt.Sprintf("eip155:%d", network.ChainID)
}

func (s *PurserServer) sweepSnapshot(ctx context.Context, network handlers.NetworkConfig) (int64, sweepRPCBlock, error) {
	finality, err := handlers.GetFinalityHead(ctx, s.rpcClient, network)
	if err != nil {
		return 0, sweepRPCBlock{}, err
	}
	snapshot := finality.Number
	var block sweepRPCBlock
	if err := s.rpcClient.Call(ctx, network, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", snapshot), false}, &block); err != nil {
		return 0, sweepRPCBlock{}, err
	}
	if len(block.Hash) != 66 || !strings.EqualFold(block.Hash, finality.Hash) {
		return 0, sweepRPCBlock{}, fmt.Errorf("snapshot block does not match %s head", finality.Tag)
	}
	return snapshot, block, nil
}

func (s *PurserServer) loadSweepCandidates(ctx context.Context, network handlers.NetworkConfig) ([]sweepCandidate, error) {
	rows, err := purserdb.New(s.db).ListCryptoSweepCandidates(ctx, purserdb.ListCryptoSweepCandidatesParams{
		Network: network.Name, Caip2Network: caip2ForSweep(network),
	})
	if err != nil {
		return nil, err
	}
	byAddress := map[string]*sweepCandidate{}
	for _, row := range rows {
		walletID := sql.NullString{String: row.WalletID, Valid: row.WalletID != ""}
		amount, ok := new(big.Int).SetString(row.AmountBaseUnits, 10)
		if !ok || amount.Sign() <= 0 {
			return nil, fmt.Errorf("invalid eligible amount for source %s", row.SourceID)
		}
		candidate := byAddress[row.CustodyID]
		if candidate == nil {
			candidate = &sweepCandidate{
				custodyID: row.CustodyID, walletID: walletID, asset: row.Asset, address: row.Address,
				derivation: uint32(row.DerivationIndex), derivationXpub: row.DerivationXpub, eligibleAmount: new(big.Int),
			}
			byAddress[row.CustodyID] = candidate
		}
		if candidate.derivationXpub != row.DerivationXpub {
			return nil, fmt.Errorf("custody address %s has conflicting derivation keys", row.CustodyID)
		}
		candidate.eligibleAmount.Add(candidate.eligibleAmount, amount)
		candidate.sources = append(candidate.sources, sweepSource{typeName: row.SourceType, id: row.SourceID, amount: amount})
	}
	result := make([]sweepCandidate, 0, len(byAddress))
	for _, candidate := range byAddress {
		result = append(result, *candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].custodyID < result[j].custodyID })
	return result, nil
}

func (s *PurserServer) sweepBalance(ctx context.Context, network handlers.NetworkConfig, asset, address string, snapshot int64) (*big.Int, error) {
	return s.sweepBalanceAt(ctx, network, asset, address, fmt.Sprintf("0x%x", snapshot))
}

func (s *PurserServer) sweepBalanceAt(ctx context.Context, network handlers.NetworkConfig, asset, address, blockTag string) (*big.Int, error) {
	var value string
	if asset == "ETH" {
		if err := s.rpcClient.Call(ctx, network, "eth_getBalance", []any{address, blockTag}, &value); err != nil {
			return nil, err
		}
	} else {
		data := "0x70a08231" + strings.Repeat("0", 24) + strings.TrimPrefix(strings.ToLower(address), "0x")
		if err := s.rpcClient.Call(ctx, network, "eth_call", []any{map[string]any{"to": network.USDCContract, "data": data}, blockTag}, &value); err != nil {
			return nil, err
		}
	}
	return parseSweepHex(value)
}

func (s *PurserServer) recheckSweepItemBeforeBroadcast(ctx context.Context, network handlers.NetworkConfig, item cryptosweep.ManifestItem) error {
	amount, ok := new(big.Int).SetString(item.AmountBaseUnits, 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("invalid sweep amount")
	}
	balance, err := s.sweepBalanceAt(ctx, network, item.Asset, item.SourceAddress, "latest")
	if err != nil {
		return fmt.Errorf("read current source balance: %w", err)
	}
	if balance.Cmp(amount) < 0 {
		return fmt.Errorf("current source balance is below signed sweep amount")
	}
	if item.Asset == "ETH" {
		var nonceHex string
		if err := s.rpcClient.Call(ctx, network, "eth_getTransactionCount", []any{item.SourceAddress, "pending"}, &nonceHex); err != nil {
			return fmt.Errorf("read current source nonce: %w", err)
		}
		nonce, err := parseSweepHex(nonceHex)
		if err != nil || !nonce.IsUint64() || nonce.Uint64() != item.SourceNonce {
			return fmt.Errorf("current source nonce does not match signed sweep nonce")
		}
		return nil
	}
	if !strings.EqualFold(item.AssetContract, network.USDCContract) {
		return fmt.Errorf("signed USDC contract does not match configured network token")
	}
	selector := ethcrypto.Keccak256([]byte("authorizationState(address,bytes32)"))[:4]
	addressWord := common.LeftPadBytes(common.HexToAddress(item.SourceAddress).Bytes(), 32)
	nonceWord := common.HexToHash(item.AuthorizationNonce).Bytes()
	data := "0x" + hex.EncodeToString(append(append(selector, addressWord...), nonceWord...))
	var encodedState string
	if err := s.rpcClient.Call(ctx, network, "eth_call", []any{
		map[string]any{"to": network.USDCContract, "data": data}, "latest",
	}, &encodedState); err != nil {
		return fmt.Errorf("read current USDC authorization state: %w", err)
	}
	used, err := parseSweepHex(encodedState)
	if err != nil {
		return fmt.Errorf("invalid USDC authorization state")
	}
	if used.Sign() != 0 {
		return fmt.Errorf("USDC sweep authorization is already used")
	}
	finality, err := handlers.GetFinalityHead(ctx, s.rpcClient, network)
	if err != nil {
		return err
	}
	name, version, err := s.sweepTokenDomain(ctx, network, finality.Number)
	if err != nil {
		return err
	}
	if name != item.TokenDomainName || version != item.TokenDomainVersion {
		return fmt.Errorf("USDC signing domain changed after planning")
	}
	return nil
}

func (s *PurserServer) sweepFees(ctx context.Context, network handlers.NetworkConfig, block sweepRPCBlock) (tip, maxFee *big.Int, err error) {
	var tipHex string
	if err = s.rpcClient.Call(ctx, network, "eth_maxPriorityFeePerGas", []any{}, &tipHex); err != nil {
		tip = big.NewInt(2_000_000_000)
	} else if tip, err = parseSweepHex(tipHex); err != nil {
		return nil, nil, err
	}
	base, err := parseSweepHex(block.BaseFeePerGas)
	if err != nil {
		return nil, nil, err
	}
	maxFee = new(big.Int).Add(new(big.Int).Mul(base, big.NewInt(2)), tip)
	return tip, maxFee, nil
}

func randomAuthorizationNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := crand.Read(value); err != nil {
		return "", err
	}
	return "0x" + hex.EncodeToString(value), nil
}

func (s *PurserServer) sweepTokenDomain(ctx context.Context, network handlers.NetworkConfig, snapshot int64) (string, string, error) {
	stringType, err := abi.NewType("string", "", nil)
	if err != nil {
		return "", "", err
	}
	outputs := abi.Arguments{{Type: stringType}}
	read := func(selector string) (string, error) {
		var encoded string
		if err := s.rpcClient.Call(ctx, network, "eth_call", []any{
			map[string]any{"to": network.USDCContract, "data": selector}, fmt.Sprintf("0x%x", snapshot),
		}, &encoded); err != nil {
			return "", err
		}
		payload, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
		if err != nil {
			return "", err
		}
		values, err := outputs.Unpack(payload)
		if err != nil {
			return "", fmt.Errorf("decode token domain string: %w", err)
		}
		if len(values) != 1 {
			return "", fmt.Errorf("decode token domain string: expected one value")
		}
		value, ok := values[0].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("token domain string is empty")
		}
		return value, nil
	}
	name, err := read("0x06fdde03") // name()
	if err != nil {
		return "", "", fmt.Errorf("read USDC EIP-712 name: %w", err)
	}
	version, err := read("0x54fd4d50") // version()
	if err != nil {
		return "", "", fmt.Errorf("read USDC EIP-712 version: %w", err)
	}
	if network.USDCDomainName != "" && name != network.USDCDomainName {
		return "", "", fmt.Errorf("USDC domain name %q does not match configured %q", name, network.USDCDomainName)
	}
	return name, version, nil
}

func (s *PurserServer) PlanCryptoSweep(ctx context.Context, req *purserpb.PlanCryptoSweepRequest) (*purserpb.PlanCryptoSweepResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	network, err := sweepNetwork(req.GetNetwork())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	treasury, err := sweepTreasury(network.Name)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if err := handlers.ValidateCryptoCustodyNetwork(ctx, s.rpcClient, network, "ETH"); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "treasury custody path: %v", err)
	}
	if err := s.releaseExpiredUnsignedSweepClaims(ctx, network); err != nil {
		return nil, status.Errorf(codes.Internal, "release expired unsigned sweep claims: %v", err)
	}
	snapshot, block, err := s.sweepSnapshot(ctx, network)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "load canonical sweep snapshot: %v", err)
	}
	candidates, err := s.loadSweepCandidates(ctx, network)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load sweep candidates: %v", err)
	}
	if len(candidates) == 0 {
		return nil, status.Error(codes.NotFound, "no confirmed unswept balances found")
	}
	// One offline signing ceremony uses exactly one xprv. If historical
	// addresses span rotated keys, plan one deterministic key group and
	// let the operator repeat the command for the remaining groups.
	xpub := candidates[0].derivationXpub
	keyCandidates := candidates[:0]
	for _, candidate := range candidates {
		if candidate.derivationXpub == xpub {
			keyCandidates = append(keyCandidates, candidate)
		}
	}
	candidates = keyCandidates
	now := time.Now().UTC()
	manifest := cryptosweep.Manifest{
		Version: cryptosweep.ManifestVersion, BatchID: uuid.NewString(), Network: network.Name,
		ChainID: network.ChainID, TreasuryAddress: treasury, XPub: xpub,
		SnapshotBlock: snapshot, SnapshotBlockHash: block.Hash,
		CreatedAt: now, ExpiresAt: now.Add(30 * time.Minute),
	}
	tip, maxFee, err := s.sweepFees(ctx, network, block)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "load sweep fees: %v", err)
	}
	itemsByID := map[string]sweepCandidate{}
	var tokenDomainName, tokenDomainVersion string
	for _, candidate := range candidates {
		balance, balanceErr := s.sweepBalance(ctx, network, candidate.asset, candidate.address, snapshot)
		if balanceErr != nil {
			return nil, status.Errorf(codes.Unavailable, "read %s balance for %s: %v", candidate.asset, candidate.address, balanceErr)
		}
		amount := new(big.Int).Set(candidate.eligibleAmount)
		if amount.Cmp(balance) > 0 {
			if candidate.asset == "USDC" {
				return nil, status.Errorf(codes.FailedPrecondition, "USDC custody balance below unswept ledger amount for %s", candidate.address)
			}
			amount.Set(balance)
		}
		item := cryptosweep.ManifestItem{
			ItemID: uuid.NewString(), CustodyAddressID: candidate.custodyID,
			WalletID: candidate.walletID.String,
			Asset:    candidate.asset, SourceAddress: candidate.address,
			DerivationIndex: candidate.derivation, DestinationAddress: treasury,
			AmountBaseUnits: amount.String(), MaxFeePerGas: maxFee.String(),
			MaxPriorityFeePerGas: tip.String(),
		}
		var nonceHex string
		if err := s.rpcClient.Call(ctx, network, "eth_getTransactionCount", []any{candidate.address, fmt.Sprintf("0x%x", snapshot)}, &nonceHex); err != nil {
			return nil, status.Errorf(codes.Unavailable, "read source nonce: %v", err)
		}
		nonce, err := parseSweepHex(nonceHex)
		if err != nil || !nonce.IsUint64() {
			return nil, status.Error(codes.Unavailable, "invalid source nonce")
		}
		item.SourceNonce = nonce.Uint64()
		if candidate.asset == "ETH" {
			item.GasLimit = 21_000
			fee := new(big.Int).Mul(maxFee, new(big.Int).SetUint64(item.GasLimit))
			dust := big.NewInt(100_000_000_000_000)
			if configured := strings.TrimSpace(os.Getenv("CRYPTO_SWEEP_ETH_DUST_WEI")); configured != "" {
				if parsed, ok := new(big.Int).SetString(configured, 10); ok && parsed.Sign() >= 0 {
					dust = parsed
				}
			}
			maxTransfer := new(big.Int).Sub(balance, new(big.Int).Add(fee, dust))
			if maxTransfer.Sign() <= 0 {
				continue
			}
			if amount.Cmp(maxTransfer) > 0 {
				amount.Set(maxTransfer)
				item.AmountBaseUnits = amount.String()
			}
		} else {
			item.AssetContract = strings.ToLower(network.USDCContract)
			item.AuthorizationNonce, err = randomAuthorizationNonce()
			if err != nil {
				return nil, status.Errorf(codes.Internal, "generate authorization nonce: %v", err)
			}
			item.AuthorizationAfter = now.Add(-time.Minute).Unix()
			item.AuthorizationBefore = manifest.ExpiresAt.Unix()
			if tokenDomainName == "" {
				tokenDomainName, tokenDomainVersion, err = s.sweepTokenDomain(ctx, network, snapshot)
				if err != nil {
					return nil, status.Errorf(codes.FailedPrecondition, "load USDC signing domain: %v", err)
				}
			}
			item.TokenDomainName = tokenDomainName
			item.TokenDomainVersion = tokenDomainVersion
			item.GasLimit = 150_000
		}
		manifest.Items = append(manifest.Items, item)
		itemsByID[item.ItemID] = candidate
	}
	if len(manifest.Items) == 0 {
		return nil, status.Error(codes.NotFound, "no confirmed unswept balances found")
	}
	if err := manifest.Finalize(); err != nil {
		return nil, status.Errorf(codes.Internal, "finalize sweep manifest: %v", err)
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode sweep manifest: %v", err)
	}
	if !req.GetDryRun() {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "begin sweep plan: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck
		queries := purserdb.New(tx)
		if err := queries.InsertCryptoSweepBatch(ctx, purserdb.InsertCryptoSweepBatchParams{
			ID: manifest.BatchID, ManifestVersion: int32(manifest.Version), Network: manifest.Network,
			TreasuryAddress: manifest.TreasuryAddress, SnapshotBlock: manifest.SnapshotBlock,
			SnapshotBlockHash: manifest.SnapshotBlockHash, ManifestChecksum: manifest.Checksum,
			ExpiresAt: manifest.ExpiresAt, CreatedBy: ctxkeys.GetUserID(ctx),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "insert sweep batch: %v", err)
		}
		for _, item := range manifest.Items {
			candidate := itemsByID[item.ItemID]
			wallet := ""
			if candidate.walletID.Valid {
				wallet = candidate.walletID.String
			}
			if err := queries.InsertCryptoSweepItem(ctx, purserdb.InsertCryptoSweepItemParams{
				ID: item.ItemID, BatchID: manifest.BatchID, CustodyAddressID: candidate.custodyID, WalletID: wallet,
				Network: manifest.Network, Asset: item.Asset, SourceAddress: item.SourceAddress,
				DerivationIndex: int32(item.DerivationIndex), DestinationAddress: item.DestinationAddress,
				AmountBaseUnits: item.AmountBaseUnits, ChainID: manifest.ChainID, AssetContract: item.AssetContract,
				SourceNonce:          sql.NullInt64{Int64: int64(item.SourceNonce), Valid: item.Asset == "ETH"},
				MaxFeePerGas:         sql.NullString{String: item.MaxFeePerGas, Valid: item.MaxFeePerGas != ""},
				MaxPriorityFeePerGas: sql.NullString{String: item.MaxPriorityFeePerGas, Valid: item.MaxPriorityFeePerGas != ""},
				GasLimit:             sql.NullInt64{Int64: int64(item.GasLimit), Valid: item.GasLimit != 0},
				AuthorizationNonce:   item.AuthorizationNonce, AuthorizationAfter: item.AuthorizationAfter,
				AuthorizationBefore: item.AuthorizationBefore,
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "insert sweep item: %v", err)
			}
			for _, source := range candidate.sources {
				if err := queries.InsertCryptoSweepSource(ctx, purserdb.InsertCryptoSweepSourceParams{
					ItemID: item.ItemID, SourceType: source.typeName, SourceID: source.id,
					AmountBaseUnits: source.amount.String(), ClaimedBy: ctxkeys.GetUserID(ctx),
				}); err != nil {
					return nil, status.Errorf(codes.Aborted, "claim sweep source: %v", err)
				}
			}
		}
		if err := queries.InsertCryptoSweepPlannedEvent(ctx, purserdb.InsertCryptoSweepPlannedEventParams{
			BatchID: manifest.BatchID, ActorID: ctxkeys.GetUserID(ctx), Checksum: manifest.Checksum, ItemCount: int32(len(manifest.Items)),
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "record sweep plan event: %v", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, status.Errorf(codes.Internal, "commit sweep plan: %v", err)
		}
	}
	return &purserpb.PlanCryptoSweepResponse{
		ManifestJson: payload, BatchId: manifest.BatchID,
		ItemCount: int32(len(manifest.Items)), Persisted: !req.GetDryRun(),
	}, nil
}

var transferWithAuthorizationABI = mustSweepABI(`[{"type":"function","name":"transferWithAuthorization","stateMutability":"nonpayable","inputs":[{"name":"from","type":"address"},{"name":"to","type":"address"},{"name":"value","type":"uint256"},{"name":"validAfter","type":"uint256"},{"name":"validBefore","type":"uint256"},{"name":"nonce","type":"bytes32"},{"name":"v","type":"uint8"},{"name":"r","type":"bytes32"},{"name":"s","type":"bytes32"}],"outputs":[]}]`)
var authorizationStateABI = mustSweepABI(`[{"type":"function","name":"authorizationState","stateMutability":"view","inputs":[{"name":"authorizer","type":"address"},{"name":"nonce","type":"bytes32"}],"outputs":[{"name":"","type":"bool"}]}]`)

func mustSweepABI(value string) abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(value))
	if err != nil {
		panic(err)
	}
	return parsed
}

func verifySweepBundleItem(manifest cryptosweep.Manifest, item cryptosweep.ManifestItem, signed cryptosweep.SignedBundleItem) error {
	if item.Asset == "ETH" {
		raw, err := hex.DecodeString(strings.TrimPrefix(signed.RawTransaction, "0x"))
		if err != nil {
			return err
		}
		var transaction types.Transaction
		if err := transaction.UnmarshalBinary(raw); err != nil {
			return err
		}
		if transaction.ChainId().Int64() != manifest.ChainID || transaction.To() == nil ||
			!strings.EqualFold(transaction.To().Hex(), item.DestinationAddress) ||
			transaction.Value().String() != item.AmountBaseUnits || transaction.Nonce() != item.SourceNonce ||
			transaction.Gas() != item.GasLimit || transaction.GasFeeCap().String() != item.MaxFeePerGas ||
			transaction.GasTipCap().String() != item.MaxPriorityFeePerGas {
			return fmt.Errorf("signed ETH transaction does not match manifest item %s", item.ItemID)
		}
		signer := types.LatestSignerForChainID(big.NewInt(manifest.ChainID))
		from, err := types.Sender(signer, &transaction)
		if err != nil || !strings.EqualFold(from.Hex(), item.SourceAddress) {
			return fmt.Errorf("signed ETH transaction source does not match manifest")
		}
		return nil
	}
	signature, err := hex.DecodeString(strings.TrimPrefix(signed.AuthorizationSignature, "0x"))
	if err != nil || len(signature) != 65 {
		return fmt.Errorf("invalid EIP-3009 signature")
	}
	if signature[64] >= 27 {
		signature[64] -= 27
	}
	digest, err := cryptosweep.EIP3009Digest(manifest, item)
	if err != nil {
		return err
	}
	pubkey, err := ethcrypto.SigToPub(digest, signature)
	if err != nil || !strings.EqualFold(ethcrypto.PubkeyToAddress(*pubkey).Hex(), item.SourceAddress) {
		return fmt.Errorf("EIP-3009 signer does not match manifest source")
	}
	return nil
}

func bundleItemsByID(bundle cryptosweep.SignedBundle) map[string]cryptosweep.SignedBundleItem {
	result := make(map[string]cryptosweep.SignedBundleItem, len(bundle.Items))
	for _, item := range bundle.Items {
		result[item.ItemID] = item
	}
	return result
}

func (s *PurserServer) validatePersistedSweepBundle(ctx context.Context, bundle cryptosweep.SignedBundle) error {
	row, err := purserdb.New(s.db).GetPersistedCryptoSweepBatch(ctx, bundle.Manifest.BatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return status.Error(codes.NotFound, "sweep batch not found")
	}
	if err != nil {
		return err
	}
	if row.ManifestChecksum != bundle.Manifest.Checksum || row.Network != bundle.Manifest.Network ||
		!strings.EqualFold(row.TreasuryAddress, bundle.Manifest.TreasuryAddress) || row.SnapshotBlock != bundle.Manifest.SnapshotBlock ||
		row.SnapshotBlockHash != bundle.Manifest.SnapshotBlockHash || row.ExpiresAt.UnixMicro() != bundle.Manifest.ExpiresAt.UnixMicro() {
		return status.Error(codes.FailedPrecondition, "signed bundle does not match persisted sweep batch")
	}
	if row.Status != "planned" && row.Status != "signed" && row.Status != "broadcast" && row.Status != "partially_confirmed" {
		return status.Errorf(codes.FailedPrecondition, "sweep batch is %s", row.Status)
	}
	return nil
}

func (s *PurserServer) reserveRelayerTransaction(ctx context.Context, network handlers.NetworkConfig, item cryptosweep.ManifestItem, signatureHex string) (rawHex, txHash string, err error) {
	keyName := "CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_" + strings.ToUpper(strings.ReplaceAll(network.Name, "-", "_"))
	privateKey, err := ethcrypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(os.Getenv(keyName)), "0x"))
	if err != nil {
		return "", "", fmt.Errorf("%s must contain the dedicated gas-relayer key", keyName)
	}
	relayer := ethcrypto.PubkeyToAddress(privateKey.PublicKey)
	sig, _ := hex.DecodeString(strings.TrimPrefix(signatureHex, "0x"))
	v := sig[64]
	if v < 27 {
		v += 27
	}
	var rValue, sValue [32]byte
	copy(rValue[:], sig[:32])
	copy(sValue[:], sig[32:64])
	amount, _ := new(big.Int).SetString(item.AmountBaseUnits, 10)
	data, err := transferWithAuthorizationABI.Pack("transferWithAuthorization",
		common.HexToAddress(item.SourceAddress), common.HexToAddress(item.DestinationAddress), amount,
		big.NewInt(item.AuthorizationAfter), big.NewInt(item.AuthorizationBefore), common.HexToHash(item.AuthorizationNonce),
		v, rValue, sValue)
	if err != nil {
		return "", "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback() //nolint:errcheck
	queries := purserdb.New(tx)
	row, err := queries.LockCryptoSweepRelayItem(ctx, item.ItemID)
	if err != nil {
		return "", "", err
	}
	if row.RelayTransaction.Valid && row.TxHash.Valid {
		if err := tx.Commit(); err != nil {
			return "", "", err
		}
		return row.RelayTransaction.String, row.TxHash.String, nil
	}
	if row.Status != "planned" && row.Status != "signed" {
		return "", "", fmt.Errorf("sweep item is %s without a replayable relay transaction", row.Status)
	}
	var chainNonceHex string
	if err := s.rpcClient.Call(ctx, network, "eth_getTransactionCount", []any{relayer.Hex(), "pending"}, &chainNonceHex); err != nil {
		return "", "", err
	}
	chainNonce, err := parseSweepHex(chainNonceHex)
	if err != nil || !chainNonce.IsUint64() {
		return "", "", fmt.Errorf("invalid relayer nonce")
	}
	if err := queries.EnsureCryptoSweepRelayerNonce(ctx, purserdb.EnsureCryptoSweepRelayerNonceParams{
		Network: network.Name, ChainNonce: chainNonce.Int64(),
	}); err != nil {
		return "", "", err
	}
	storedNonce, err := queries.ReserveCryptoSweepRelayerNonce(ctx, purserdb.ReserveCryptoSweepRelayerNonceParams{
		ChainNonce: chainNonce.Int64(), Network: network.Name,
	})
	if err != nil {
		return "", "", err
	}
	var latest sweepRPCBlock
	if err := s.rpcClient.Call(ctx, network, "eth_getBlockByNumber", []any{"latest", false}, &latest); err != nil {
		return "", "", err
	}
	tip, maxFee, err := s.sweepFees(ctx, network, latest)
	if err != nil {
		return "", "", err
	}
	approvedTip, okTip := new(big.Int).SetString(item.MaxPriorityFeePerGas, 10)
	approvedMaxFee, okMax := new(big.Int).SetString(item.MaxFeePerGas, 10)
	if !okTip || !okMax || tip.Cmp(approvedTip) > 0 || maxFee.Cmp(approvedMaxFee) > 0 {
		return "", "", fmt.Errorf("current relayer fee exceeds signed manifest ceiling")
	}
	gasLimit := item.GasLimit
	if gasLimit < 100_000 {
		gasLimit = 150_000
	}
	transaction := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(network.ChainID), Nonce: uint64(storedNonce),
		GasTipCap: tip, GasFeeCap: maxFee, Gas: gasLimit,
		To: ptrAddress(common.HexToAddress(item.AssetContract)), Data: data,
	})
	signed, err := types.SignTx(transaction, types.LatestSignerForChainID(big.NewInt(network.ChainID)), privateKey)
	if err != nil {
		return "", "", err
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		return "", "", err
	}
	rawHex = "0x" + hex.EncodeToString(raw)
	txHash = signed.Hash().Hex()
	if err := queries.MarkUSDCryptoSweepItemBroadcast(ctx, purserdb.MarkUSDCryptoSweepItemBroadcastParams{
		SignedPayload: sql.NullString{String: signatureHex, Valid: true}, RelayTransaction: sql.NullString{String: rawHex, Valid: true},
		TxHash: sql.NullString{String: txHash, Valid: true}, ItemID: item.ItemID,
	}); err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return rawHex, txHash, nil
}

func ptrAddress(value common.Address) *common.Address { return &value }

func (s *PurserServer) BroadcastCryptoSweep(ctx context.Context, req *purserpb.BroadcastCryptoSweepRequest) (*purserpb.BroadcastCryptoSweepResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	var bundle cryptosweep.SignedBundle
	if err := json.Unmarshal(req.GetSignedBundleJson(), &bundle); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "decode signed sweep bundle: %v", err)
	}
	if err := bundle.Validate(time.Now().UTC()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validate signed sweep bundle: %v", err)
	}
	if err := s.validatePersistedSweepBundle(ctx, bundle); err != nil {
		return nil, err
	}
	if !req.GetDryRun() && req.GetCeremonyAck() != sweepCeremonyPrefix+bundle.Checksum {
		return nil, status.Error(codes.FailedPrecondition, "ceremony acknowledgement must bind the signed bundle checksum")
	}
	network, err := sweepNetwork(bundle.Manifest.Network)
	if err != nil || network.ChainID != bundle.Manifest.ChainID {
		return nil, status.Error(codes.InvalidArgument, "bundle network/chain mismatch")
	}
	currentTreasury, err := sweepTreasury(network.Name)
	if err != nil || !strings.EqualFold(currentTreasury, bundle.Manifest.TreasuryAddress) {
		return nil, status.Error(codes.FailedPrecondition, "configured treasury changed after planning")
	}
	signedByID := bundleItemsByID(bundle)
	response := &purserpb.BroadcastCryptoSweepResponse{BatchId: bundle.Manifest.BatchID, DryRun: req.GetDryRun()}
	for _, item := range bundle.Manifest.Items {
		signed := signedByID[item.ItemID]
		result := &purserpb.SweepBroadcastItem{ItemId: item.ItemID}
		if err := verifySweepBundleItem(bundle.Manifest, item, signed); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "verify signed item: %v", err)
		}
		if req.GetDryRun() {
			result.Status = "validated"
			response.Items = append(response.Items, result)
			continue
		}
		persisted, err := purserdb.New(s.db).GetPersistedCryptoSweepItem(ctx, purserdb.GetPersistedCryptoSweepItemParams{
			ItemID: item.ItemID, BatchID: bundle.Manifest.BatchID,
		})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load persisted sweep item: %v", err)
		}
		persistedStatus, persistedHash, persistedRelay := persisted.Status, persisted.TxHash, persisted.RelayTransaction
		if persistedStatus == "confirmed" {
			result.Status = "confirmed"
			result.TxHash = persistedHash.String
			response.Items = append(response.Items, result)
			continue
		}
		if persistedStatus == "failed" || persistedStatus == "expired" {
			return nil, status.Errorf(codes.FailedPrecondition, "sweep item %s is %s", item.ItemID, persistedStatus)
		}
		if persistedStatus == "planned" || persistedStatus == "signed" {
			if err := s.recheckSweepItemBeforeBroadcast(ctx, network, item); err != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "recheck sweep item %s: %v", item.ItemID, err)
			}
		}
		rawTransaction := signed.RawTransaction
		if item.Asset == "ETH" {
			raw, _ := hex.DecodeString(strings.TrimPrefix(rawTransaction, "0x"))
			var transaction types.Transaction
			_ = transaction.UnmarshalBinary(raw)
			result.TxHash = transaction.Hash().Hex()
			if err := purserdb.New(s.db).MarkETHCryptoSweepItemBroadcast(ctx, purserdb.MarkETHCryptoSweepItemBroadcastParams{
				SignedPayload: sql.NullString{String: rawTransaction, Valid: true}, TxHash: sql.NullString{String: result.TxHash, Valid: true}, ItemID: item.ItemID,
			}); err != nil {
				return nil, status.Errorf(codes.Internal, "persist ETH sweep intent: %v", err)
			}
		} else if persistedRelay.Valid && persistedHash.Valid {
			rawTransaction = persistedRelay.String
			result.TxHash = persistedHash.String
		} else {
			rawTransaction, result.TxHash, err = s.reserveRelayerTransaction(ctx, network, item, signed.AuthorizationSignature)
			if err != nil {
				return nil, status.Errorf(codes.FailedPrecondition, "prepare USDC relay: %v", err)
			}
		}
		var providerHash string
		sendErr := s.rpcClient.Call(ctx, network, "eth_sendRawTransaction", []any{rawTransaction}, &providerHash)
		result.Status = "broadcast_unknown"
		if sendErr == nil || strings.Contains(strings.ToLower(sendErr.Error()), "already known") {
			result.Status = "broadcast"
		} else {
			result.Error = "RPC submission outcome unknown; reconcile before retrying"
		}
		response.Items = append(response.Items, result)
	}
	if err := purserdb.New(s.db).RecordCryptoSweepBroadcast(ctx, purserdb.RecordCryptoSweepBroadcastParams{
		ActorID: ctxkeys.GetUserID(ctx), BundleChecksum: bundle.Checksum, BatchID: bundle.Manifest.BatchID,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "record sweep broadcast: %v", err)
	}
	return response, nil
}

func (s *PurserServer) ReconcileCryptoSweep(ctx context.Context, req *purserpb.ReconcileCryptoSweepRequest) (*purserpb.ReconcileCryptoSweepResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	batchID := strings.TrimSpace(req.GetBatchId())
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid batch_id required")
	}
	networkName, err := purserdb.New(s.db).GetCryptoSweepBatchNetwork(ctx, batchID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "sweep batch not found: %v", err)
	}
	network, err := sweepNetwork(networkName)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	finality, err := handlers.GetFinalityHead(ctx, s.rpcClient, network)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "read finalized head: %v", err)
	}
	rows, err := purserdb.New(s.db).ListCryptoSweepReconcileItems(ctx, batchID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load sweep items: %v", err)
	}
	response := &purserpb.ReconcileCryptoSweepResponse{BatchId: batchID, DryRun: req.GetDryRun()}
	for _, row := range rows {
		itemID, walletID, txHash, itemStatus := row.ItemID, row.WalletID, row.TxHash, row.Status
		if itemStatus == "confirmed" {
			response.ConfirmedItems++
			continue
		}
		if itemStatus == "failed" {
			response.FailedItems++
			continue
		}
		if !txHash.Valid || txHash.String == "" {
			response.PendingItems++
			continue
		}
		var receipt *sweepRPCReceipt
		if err := s.rpcClient.Call(ctx, network, "eth_getTransactionReceipt", []any{txHash.String}, &receipt); err != nil || receipt == nil {
			response.PendingItems++
			continue
		}
		if receipt.Status == "0x0" {
			response.FailedItems++
			if !req.GetDryRun() {
				_ = purserdb.New(s.db).MarkCryptoSweepItemFailed(ctx, itemID)
			}
			continue
		}
		blockNumber, err := parseSweepHex(receipt.BlockNumber)
		if err != nil || !blockNumber.IsInt64() || blockNumber.Int64() > finality.Number {
			response.PendingItems++
			continue
		}
		var canonical sweepRPCBlock
		if err := s.rpcClient.Call(ctx, network, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &canonical); err != nil || !strings.EqualFold(canonical.Hash, receipt.BlockHash) {
			response.PendingItems++
			continue
		}
		response.ConfirmedItems++
		if !req.GetDryRun() {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return nil, err
			}
			queries := purserdb.New(tx)
			if err = queries.MarkCryptoSweepItemConfirmed(ctx, itemID); err == nil {
				err = queries.ConsumeCryptoSweepSources(ctx, itemID)
			}
			if err == nil && walletID != "" {
				err = queries.MarkSweptCryptoWallet(ctx, walletID)
			}
			if err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
		}
	}
	response.Status = "partially_confirmed"
	if response.PendingItems == 0 && response.FailedItems == 0 {
		response.Status = "confirmed"
	} else if response.PendingItems == 0 && response.FailedItems > 0 {
		response.Status = "failed"
	}
	if !req.GetDryRun() {
		if err := purserdb.New(s.db).RecordCryptoSweepReconciliation(ctx, purserdb.RecordCryptoSweepReconciliationParams{
			ActorID: ctxkeys.GetUserID(ctx), Status: response.Status, ConfirmedItems: response.ConfirmedItems,
			PendingItems: response.PendingItems, FailedItems: response.FailedItems, BatchID: batchID,
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "record sweep reconciliation: %v", err)
		}
	}
	return response, nil
}

type sweepReleaseItem struct {
	id                  string
	asset               string
	sourceAddress       string
	amount              *big.Int
	sourceNonce         sql.NullInt64
	authorizationNonce  sql.NullString
	authorizationBefore sql.NullInt64
	signedPayload       sql.NullString
	relayTransaction    sql.NullString
	txHash              sql.NullString
	status              string
	broadcastAt         sql.NullTime
	claimedSources      int64
}

type sweepReleaseDecision struct {
	action string
	reason string
}

func (s *PurserServer) canonicalSweepReceipt(ctx context.Context, network handlers.NetworkConfig, txHash string) (*sweepRPCReceipt, bool, error) {
	var receipt *sweepRPCReceipt
	if err := s.rpcClient.Call(ctx, network, "eth_getTransactionReceipt", []any{txHash}, &receipt); err != nil {
		return nil, false, err
	}
	if receipt == nil {
		return nil, false, nil
	}
	finality, err := handlers.GetFinalityHead(ctx, s.rpcClient, network)
	if err != nil {
		return nil, false, err
	}
	blockNumber, err := parseSweepHex(receipt.BlockNumber)
	if err != nil {
		return nil, false, fmt.Errorf("parse sweep receipt block number: %w", err)
	}
	if !blockNumber.IsInt64() || blockNumber.Int64() > finality.Number {
		return receipt, false, nil
	}
	var canonical sweepRPCBlock
	if err := s.rpcClient.Call(ctx, network, "eth_getBlockByNumber", []any{receipt.BlockNumber, false}, &canonical); err != nil {
		return nil, false, err
	}
	return receipt, strings.EqualFold(canonical.Hash, receipt.BlockHash), nil
}

func (s *PurserServer) sweepAuthorizationUsed(ctx context.Context, network handlers.NetworkConfig, item sweepReleaseItem, blockTag string) (bool, error) {
	if !item.authorizationNonce.Valid {
		return false, fmt.Errorf("USDC authorization nonce is missing")
	}
	data, err := authorizationStateABI.Pack(
		"authorizationState",
		common.HexToAddress(item.sourceAddress),
		common.HexToHash(item.authorizationNonce.String),
	)
	if err != nil {
		return false, err
	}
	var result string
	if err := s.rpcClient.Call(ctx, network, "eth_call", []any{
		map[string]any{"to": network.USDCContract, "data": "0x" + hex.EncodeToString(data)}, blockTag,
	}, &result); err != nil {
		return false, err
	}
	parsed, err := parseSweepHex(result)
	if err != nil {
		return false, err
	}
	return parsed.Sign() != 0, nil
}

func (s *PurserServer) evaluateSweepRelease(ctx context.Context, network handlers.NetworkConfig, expiresAt time.Time, item sweepReleaseItem) sweepReleaseDecision {
	if item.claimedSources == 0 || item.status == "expired" {
		return sweepReleaseDecision{action: "blocked", reason: "source claim is not active"}
	}
	if item.status == "confirmed" {
		return sweepReleaseDecision{action: "blocked", reason: "confirmed sweep claims are consumed"}
	}
	expired := !time.Now().UTC().Before(expiresAt)
	if item.txHash.Valid && item.txHash.String != "" {
		receipt, canonical, err := s.canonicalSweepReceipt(ctx, network, item.txHash.String)
		if err != nil || receipt == nil || !canonical {
			return sweepReleaseDecision{action: "quarantined", reason: "broadcast transaction outcome is not canonically finalized"}
		}
		if receipt.Status != "0x0" {
			return sweepReleaseDecision{action: "blocked", reason: "broadcast transaction succeeded; reconcile the batch"}
		}
		if item.asset == "USDC" && !expired {
			return sweepReleaseDecision{action: "quarantined", reason: "reverted USDC relay authorization has not expired"}
		}
		return sweepReleaseDecision{action: "released", reason: "broadcast transaction canonically reverted"}
	}
	if item.relayTransaction.Valid || item.broadcastAt.Valid || item.status == "broadcast" {
		return sweepReleaseDecision{action: "quarantined", reason: "broadcast intent has no resolvable transaction hash"}
	}
	if !expired {
		return sweepReleaseDecision{action: "blocked", reason: "sweep manifest has not expired"}
	}
	finality, err := handlers.GetFinalityHead(ctx, s.rpcClient, network)
	if err != nil {
		return sweepReleaseDecision{action: "quarantined", reason: "cannot read canonical finality head"}
	}
	blockTag := fmt.Sprintf("0x%x", finality.Number)
	if item.signedPayload.Valid && item.signedPayload.String != "" {
		if item.asset == "USDC" {
			if !item.authorizationBefore.Valid || time.Now().UTC().Unix() <= item.authorizationBefore.Int64 {
				return sweepReleaseDecision{action: "quarantined", reason: "signed USDC authorization has not expired"}
			}
			used, usedErr := s.sweepAuthorizationUsed(ctx, network, item, blockTag)
			if usedErr != nil || used {
				return sweepReleaseDecision{action: "quarantined", reason: "signed USDC authorization is used or cannot be proven unused"}
			}
			return sweepReleaseDecision{action: "released", reason: "expired USDC authorization is canonically unused"}
		}
		if !item.sourceNonce.Valid {
			return sweepReleaseDecision{action: "quarantined", reason: "signed ETH transaction source nonce is missing"}
		}
		var nonceHex string
		if err := s.rpcClient.Call(ctx, network, "eth_getTransactionCount", []any{item.sourceAddress, "pending"}, &nonceHex); err != nil {
			return sweepReleaseDecision{action: "quarantined", reason: "cannot prove signed ETH transaction nonce was consumed or replaced"}
		}
		nonce, err := parseSweepHex(nonceHex)
		if err != nil || !nonce.IsInt64() || nonce.Int64() <= item.sourceNonce.Int64 {
			return sweepReleaseDecision{action: "quarantined", reason: "signed ETH transaction can still be broadcast"}
		}
		return sweepReleaseDecision{action: "released", reason: "signed ETH transaction nonce was consumed or replaced"}
	}
	balance, err := s.sweepBalanceAt(ctx, network, item.asset, item.sourceAddress, blockTag)
	if err != nil || balance.Cmp(item.amount) < 0 {
		return sweepReleaseDecision{action: "quarantined", reason: "canonical source balance no longer covers the unsigned sweep claim"}
	}
	return sweepReleaseDecision{action: "released", reason: "expired unsigned manifest passed canonical balance recheck"}
}

func (s *PurserServer) loadSweepReleaseBatch(ctx context.Context, batchID string) (string, handlers.NetworkConfig, time.Time, []sweepReleaseItem, error) {
	batch, err := purserdb.New(s.db).GetCryptoSweepReleaseBatch(ctx, batchID)
	if err != nil {
		return "", handlers.NetworkConfig{}, time.Time{}, nil, err
	}
	network, err := sweepNetwork(batch.Network)
	if err != nil {
		return "", handlers.NetworkConfig{}, time.Time{}, nil, err
	}
	rows, err := purserdb.New(s.db).ListCryptoSweepReleaseItems(ctx, batchID)
	if err != nil {
		return "", handlers.NetworkConfig{}, time.Time{}, nil, err
	}
	items := make([]sweepReleaseItem, 0, len(rows))
	for _, row := range rows {
		item := sweepReleaseItem{id: row.ItemID, asset: row.Asset, sourceAddress: row.SourceAddress,
			sourceNonce: row.SourceNonce, authorizationNonce: row.AuthorizationNonce,
			authorizationBefore: row.AuthorizationBefore, signedPayload: row.SignedPayload,
			relayTransaction: row.RelayTransaction, txHash: row.TxHash, status: row.Status,
			broadcastAt: row.BroadcastAt, claimedSources: row.ClaimedSources}
		item.amount, _ = new(big.Int).SetString(row.AmountBaseUnits, 10)
		if item.amount == nil {
			return "", handlers.NetworkConfig{}, time.Time{}, nil, fmt.Errorf("invalid amount for sweep item %s", item.id)
		}
		items = append(items, item)
	}
	return batch.ManifestChecksum, network, batch.ExpiresAt, items, nil
}

func (s *PurserServer) releaseSweepBatch(ctx context.Context, batchID, reason string, dryRun bool) (*purserpb.ReleaseCryptoSweepResponse, error) {
	checksum, network, expiresAt, items, err := s.loadSweepReleaseBatch(ctx, batchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "sweep batch not found")
	}
	if err != nil {
		return nil, err
	}
	snapshot, err := purserdb.New(s.db).GetCryptoSweepSnapshot(ctx, batchID)
	if err != nil {
		return nil, err
	}
	var snapshotCanonical sweepRPCBlock
	lineageErr := s.rpcClient.Call(ctx, network, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", snapshot.SnapshotBlock), false}, &snapshotCanonical)
	lineageValid := lineageErr == nil && strings.EqualFold(snapshotCanonical.Hash, snapshot.SnapshotBlockHash)
	response := &purserpb.ReleaseCryptoSweepResponse{
		BatchId: batchID, ManifestChecksum: checksum, DryRun: dryRun, Status: "unchanged",
	}
	for _, item := range items {
		decision := s.evaluateSweepRelease(ctx, network, expiresAt, item)
		if !lineageValid && decision.action == "released" {
			decision = sweepReleaseDecision{action: "quarantined", reason: "persisted sweep snapshot lineage is not canonical"}
		}
		switch decision.action {
		case "released":
			response.ReleasedItems++
		case "quarantined":
			response.QuarantinedItems++
		default:
			response.BlockedItems++
		}
		if dryRun || decision.action == "blocked" {
			continue
		}
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return nil, txErr
		}
		queries := purserdb.New(tx)
		locked, txErr := queries.LockCryptoSweepReleaseItem(ctx, item.id)
		unchanged := txErr == nil && locked.Status == item.status &&
			locked.SignedPayload.Valid == item.signedPayload.Valid && locked.SignedPayload.String == item.signedPayload.String &&
			locked.RelayTransaction.Valid == item.relayTransaction.Valid && locked.RelayTransaction.String == item.relayTransaction.String &&
			locked.TxHash.Valid == item.txHash.Valid && locked.TxHash.String == item.txHash.String &&
			locked.BroadcastAt.Valid == item.broadcastAt.Valid
		if unchanged && locked.BroadcastAt.Valid {
			unchanged = locked.BroadcastAt.Time.Equal(item.broadcastAt.Time)
		}
		if txErr != nil || !unchanged {
			_ = tx.Rollback()
			if txErr != nil {
				return nil, txErr
			}
			return nil, status.Errorf(codes.Aborted, "sweep item %s changed during release evaluation; retry the dry run", item.id)
		}
		itemStatus := "expired"
		sourceStatus := "released"
		eventType := "claim_released"
		if decision.action == "quarantined" {
			itemStatus = "quarantined"
			sourceStatus = "quarantined"
			eventType = "claim_quarantined"
		}
		affected, txErr := queries.ReleaseCryptoSweepSources(ctx, purserdb.ReleaseCryptoSweepSourcesParams{
			SourceStatus: sourceStatus, ReleasedBy: ctxkeys.GetUserID(ctx),
			ReleaseReason: sql.NullString{String: reason + ": " + decision.reason, Valid: true}, ItemID: item.id,
		})
		if txErr == nil {
			if affected == 0 {
				txErr = fmt.Errorf("sweep item %s no longer has active source claims", item.id)
			}
		}
		if txErr == nil {
			txErr = queries.UpdateReleasedCryptoSweepItem(ctx, purserdb.UpdateReleasedCryptoSweepItemParams{
				ItemStatus: itemStatus, FailureReason: sql.NullString{String: decision.reason, Valid: true}, ItemID: item.id,
			})
		}
		if txErr == nil {
			txErr = queries.InsertCryptoSweepReleaseEvent(ctx, purserdb.InsertCryptoSweepReleaseEventParams{
				BatchID: batchID, ItemID: item.id, EventType: eventType, ActorID: ctxkeys.GetUserID(ctx),
				Reason: reason, Evidence: decision.reason,
			})
		}
		if txErr != nil {
			_ = tx.Rollback()
			return nil, txErr
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	if response.QuarantinedItems > 0 {
		response.Status = "quarantined"
	} else if response.ReleasedItems > 0 && response.BlockedItems == 0 {
		response.Status = "expired"
	} else if response.ReleasedItems > 0 {
		response.Status = "partially_confirmed"
	}
	if !dryRun && response.Status != "unchanged" {
		if err := purserdb.New(s.db).RecordCryptoSweepReleaseCompleted(ctx, purserdb.RecordCryptoSweepReleaseCompletedParams{
			ActorID: ctxkeys.GetUserID(ctx), Reason: reason, Status: response.Status,
			ReleasedItems: response.ReleasedItems, QuarantinedItems: response.QuarantinedItems,
			BlockedItems: response.BlockedItems, BatchID: batchID,
		}); err != nil {
			return nil, err
		}
	}
	return response, nil
}

func (s *PurserServer) releaseExpiredUnsignedSweepClaims(ctx context.Context, network handlers.NetworkConfig) error {
	batchIDs, err := purserdb.New(s.db).ListExpiredUnsignedCryptoSweepBatches(ctx, network.Name)
	if err != nil {
		return err
	}
	for _, batchID := range batchIDs {
		if _, err := s.releaseSweepBatch(ctx, batchID, "automatic expiry recovery", false); err != nil {
			return err
		}
	}
	return nil
}

func (s *PurserServer) ReleaseCryptoSweep(ctx context.Context, req *purserpb.ReleaseCryptoSweepRequest) (*purserpb.ReleaseCryptoSweepResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	batchID := strings.TrimSpace(req.GetBatchId())
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid batch_id required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "release reason is required")
	}
	checksum, err := purserdb.New(s.db).GetCryptoSweepManifestChecksum(ctx, batchID)
	if err != nil {
		return nil, status.Error(codes.NotFound, "sweep batch not found")
	}
	if !req.GetDryRun() && req.GetCeremonyAck() != sweepCeremonyPrefix+checksum {
		return nil, status.Error(codes.FailedPrecondition, "ceremony acknowledgement must bind the sweep manifest checksum")
	}
	response, err := s.releaseSweepBatch(ctx, batchID, reason, req.GetDryRun())
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, status.Errorf(codes.Internal, "release sweep claims: %v", err)
	}
	return response, nil
}

func (s *PurserServer) ResolveX402MutationResult(ctx context.Context, req *purserpb.ResolveX402MutationResultRequest) (*purserpb.ResolveX402MutationResultResponse, error) {
	if err := requireCryptoSweepOperator(ctx); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	key := strings.TrimSpace(req.GetIdempotencyKey())
	reason := strings.TrimSpace(req.GetReason())
	if len(key) < 8 || reason == "" {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key and resolution reason are required")
	}
	attachResult := !req.GetMarkReview()
	if attachResult && (len(req.GetResult()) == 0 || len(req.GetResult()) > maxX402MutationResultBytes || req.GetStatusCode() < 100 || req.GetStatusCode() > 599) {
		return nil, status.Error(codes.InvalidArgument, "known result and valid status_code are required when not marking review")
	}
	queries := purserdb.New(s.db)
	mutation, err := queries.GetX402MutationResolutionState(ctx, purserdb.GetX402MutationResolutionStateParams{
		TenantID: tenantID, IdempotencyKey: key,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "paid mutation result not found")
		}
		return nil, status.Errorf(codes.Internal, "load paid mutation result: %v", err)
	}
	quoteID, operation, currentState := mutation.QuoteID, mutation.Operation, mutation.Status
	targetState := "operator_review"
	if attachResult {
		targetState = "completed"
	}
	response := &purserpb.ResolveX402MutationResultResponse{
		TenantId: tenantID, IdempotencyKey: key, QuoteId: quoteID, Operation: operation,
		PreviousState: currentState, State: targetState, DryRun: req.GetDryRun(),
	}
	if currentState == "completed" {
		response.State = currentState
		return response, nil
	}
	if req.GetDryRun() {
		return response, nil
	}
	var affected int64
	if attachResult {
		affected, err = queries.CompleteX402MutationResult(ctx, purserdb.CompleteX402MutationResultParams{
			Result: req.GetResult(), ContentType: req.GetContentType(), StatusCode: sql.NullInt32{Int32: req.GetStatusCode(), Valid: true},
			ResolvedBy: ctxkeys.GetUserID(ctx), ReviewReason: sql.NullString{String: reason, Valid: true},
			TenantID: tenantID, IdempotencyKey: key,
		})
	} else {
		affected, err = queries.ReviewX402MutationResult(ctx, purserdb.ReviewX402MutationResultParams{
			ReviewReason: sql.NullString{String: reason, Valid: true}, ResolvedBy: ctxkeys.GetUserID(ctx),
			TenantID: tenantID, IdempotencyKey: key,
		})
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve paid mutation result: %v", err)
	}
	response.Changed = affected > 0
	return response, nil
}
