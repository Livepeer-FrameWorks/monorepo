package grpc

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cryptosweep"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sweepServiceCtx() context.Context {
	return serviceTestContext()
}

func TestVerifySweepBundleItemBindsETHTransactionFields(t *testing.T) {
	key, err := ethcrypto.HexToECDSA("4f3edf983ac63ad7c43a0d8a05d3d4a6c67e594e0d56e4b1d72c05b90f3e6f7a")
	if err != nil {
		t.Fatal(err)
	}
	manifest := cryptosweep.Manifest{ChainID: 8453}
	item := cryptosweep.ManifestItem{
		ItemID: "item", Asset: "ETH", SourceAddress: ethcrypto.PubkeyToAddress(key.PublicKey).Hex(),
		DestinationAddress: "0x1111111111111111111111111111111111111111", AmountBaseUnits: "1000000000000000",
		SourceNonce: 7, MaxFeePerGas: "3000000000", MaxPriorityFeePerGas: "1000000000", GasLimit: 21000,
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(manifest.ChainID), Nonce: item.SourceNonce, GasTipCap: big.NewInt(1_000_000_000),
		GasFeeCap: big.NewInt(3_000_000_000), Gas: item.GasLimit,
		To: ptrAddress(common.HexToAddress(item.DestinationAddress)), Value: big.NewInt(1_000_000_000_000_000),
	})
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(manifest.ChainID)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signedTx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	signed := cryptosweep.SignedBundleItem{ItemID: item.ItemID, RawTransaction: "0x" + hex.EncodeToString(raw)}
	if err := verifySweepBundleItem(manifest, item, signed); err != nil {
		t.Fatalf("valid signed ETH item rejected: %v", err)
	}

	tests := map[string]func(*cryptosweep.Manifest, *cryptosweep.ManifestItem){
		"wrong chain": func(m *cryptosweep.Manifest, _ *cryptosweep.ManifestItem) { m.ChainID = 1 },
		"wrong destination": func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) {
			i.DestinationAddress = "0x2222222222222222222222222222222222222222"
		},
		"changed amount": func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) { i.AmountBaseUnits = "2" },
		"changed nonce":  func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) { i.SourceNonce++ },
		"changed fee":    func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) { i.MaxFeePerGas = "4" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidateManifest, candidateItem := manifest, item
			mutate(&candidateManifest, &candidateItem)
			if err := verifySweepBundleItem(candidateManifest, candidateItem, signed); err == nil {
				t.Fatal("altered signed ETH intent unexpectedly verified")
			}
		})
	}
}

func sweepRPCServer(t *testing.T, result func(method string, params []any) any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var payload struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
			ID     int    `json:"id"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode RPC request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": payload.ID, "result": result(payload.Method, payload.Params),
		}); err != nil {
			t.Errorf("encode RPC response: %v", err)
		}
	}))
}

func TestSweepBroadcastRecheckRejectsChangedChainState(t *testing.T) {
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		switch method {
		case "eth_getBalance":
			return "0x1"
		case "eth_getTransactionCount":
			return "0x2"
		case "eth_call":
			return "0x100"
		default:
			return "0x0"
		}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	server := &PurserServer{rpcClient: handlers.NewRPCClient()}
	network := handlers.Networks["base"]

	ethItem := cryptosweep.ManifestItem{
		Asset: "ETH", SourceAddress: "0x2222222222222222222222222222222222222222",
		AmountBaseUnits: "2", SourceNonce: 1,
	}
	if err := server.recheckSweepItemBeforeBroadcast(context.Background(), network, ethItem); err == nil || !strings.Contains(err.Error(), "balance is below") {
		t.Fatalf("changed balance error = %v", err)
	}
	ethItem.AmountBaseUnits = "1"
	if err := server.recheckSweepItemBeforeBroadcast(context.Background(), network, ethItem); err == nil || !strings.Contains(err.Error(), "nonce does not match") {
		t.Fatalf("changed nonce error = %v", err)
	}

	usdcItem := cryptosweep.ManifestItem{
		Asset: "USDC", SourceAddress: ethItem.SourceAddress, AmountBaseUnits: "1",
		AssetContract: "0x4444444444444444444444444444444444444444",
	}
	if err := server.recheckSweepItemBeforeBroadcast(context.Background(), network, usdcItem); err == nil || !strings.Contains(err.Error(), "contract does not match") {
		t.Fatalf("changed token error = %v", err)
	}
}

func TestReserveRelayerTransactionReplaysPersistedIntentAfterRestart(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("CRYPTO_SWEEP_RELAYER_PRIVATE_KEY_BASE", "4f3edf983ac63ad7c43a0d8a05d3d4a6c67e594e0d56e4b1d72c05b90f3e6f7a")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT relay_transaction, tx_hash, status`).WithArgs("item-1").
		WillReturnRows(sqlmock.NewRows([]string{"relay_transaction", "tx_hash", "status"}).
			AddRow("0xpersisted", "0xhash", "broadcast"))
	mock.ExpectCommit()
	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	item := cryptosweep.ManifestItem{
		ItemID: "item-1", Asset: "USDC", SourceAddress: "0x2222222222222222222222222222222222222222",
		DestinationAddress: "0x1111111111111111111111111111111111111111", AmountBaseUnits: "5000000",
		AssetContract:      handlers.Networks["base"].USDCContract,
		AuthorizationNonce: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AuthorizationAfter: 1, AuthorizationBefore: 2, GasLimit: 150000,
	}
	raw, hash, err := server.reserveRelayerTransaction(context.Background(), handlers.Networks["base"], item, "0x"+strings.Repeat("11", 65))
	if err != nil {
		t.Fatal(err)
	}
	if raw != "0xpersisted" || hash != "0xhash" {
		t.Fatalf("replayed intent = %q %q", raw, hash)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileCryptoSweepDoesNotConfirmReorgedReceipt(t *testing.T) {
	canonicalFinalizedHash := "0x" + strings.Repeat("aa", 32)
	receiptBlockHash := "0x" + strings.Repeat("bb", 32)
	canonicalReceiptHeightHash := "0x" + strings.Repeat("cc", 32)
	rpcServer := sweepRPCServer(t, func(method string, params []any) any {
		if method == "eth_getTransactionReceipt" {
			return map[string]any{
				"transactionHash": "0xtx", "blockNumber": "0x10", "blockHash": receiptBlockHash, "status": "0x1",
			}
		}
		if method == "eth_getBlockByNumber" && len(params) > 0 && params[0] == "finalized" {
			return map[string]any{"number": "0x20", "hash": canonicalFinalizedHash, "baseFeePerGas": "0x1"}
		}
		return map[string]any{"number": "0x10", "hash": canonicalReceiptHeightHash, "baseFeePerGas": "0x1"}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	batchID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery(`SELECT network FROM purser.crypto_sweep_batches`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"network"}).AddRow("base"))
	mock.ExpectQuery(`SELECT id::text AS item_id, COALESCE\(wallet_id::text, ''\)::text AS wallet_id`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "wallet_id", "tx_hash", "status"}).
			AddRow("22222222-2222-2222-2222-222222222222", "", "0xtx", "broadcast"))
	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	response, err := server.ReconcileCryptoSweep(sweepServiceCtx(), &purserpb.ReconcileCryptoSweepRequest{BatchId: batchID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetPendingItems() != 1 || response.GetConfirmedItems() != 0 {
		t.Fatalf("reorged receipt response = %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileCryptoSweepAcceptsUnbroadcastNullHash(t *testing.T) {
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		if method == "eth_getBlockByNumber" {
			return map[string]any{"number": "0x20", "hash": "0x" + strings.Repeat("aa", 32), "baseFeePerGas": "0x1"}
		}
		return nil
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	batchID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery(`SELECT network FROM purser.crypto_sweep_batches`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"network"}).AddRow("base"))
	mock.ExpectQuery(`SELECT id::text AS item_id, COALESCE\(wallet_id::text, ''\)::text AS wallet_id`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "wallet_id", "tx_hash", "status"}).
			AddRow("22222222-2222-2222-2222-222222222222", "", nil, "planned"))
	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	response, err := server.ReconcileCryptoSweep(sweepServiceCtx(), &purserpb.ReconcileCryptoSweepRequest{BatchId: batchID, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetPendingItems() != 1 {
		t.Fatalf("null-hash response = %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluateSweepReleaseSignedUSDCRequiresExpiredUnusedAuthorization(t *testing.T) {
	used := false
	rpcServer := sweepRPCServer(t, func(method string, params []any) any {
		switch method {
		case "eth_getBlockByNumber":
			return map[string]any{"number": "0x20", "hash": "0x" + strings.Repeat("aa", 32)}
		case "eth_call":
			if used {
				return "0x1"
			}
			return "0x0"
		default:
			_ = params
			return nil
		}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	server := &PurserServer{rpcClient: handlers.NewRPCClient()}
	network := handlers.Networks["base"]
	item := sweepReleaseItem{
		asset: "USDC", sourceAddress: "0x2222222222222222222222222222222222222222", amount: big.NewInt(1),
		authorizationNonce:  sql.NullString{String: "0x" + strings.Repeat("11", 32), Valid: true},
		authorizationBefore: sql.NullInt64{Int64: time.Now().Add(time.Minute).Unix(), Valid: true},
		signedPayload:       sql.NullString{String: "0xsigned", Valid: true}, status: "signed", claimedSources: 1,
	}
	decision := server.evaluateSweepRelease(context.Background(), network, time.Now().Add(-time.Minute), item)
	if decision.action != "quarantined" {
		t.Fatalf("unexpired authorization decision=%+v", decision)
	}
	item.authorizationBefore.Int64 = time.Now().Add(-time.Minute).Unix()
	decision = server.evaluateSweepRelease(context.Background(), network, time.Now().Add(-time.Minute), item)
	if decision.action != "released" {
		t.Fatalf("expired unused authorization decision=%+v", decision)
	}
	used = true
	decision = server.evaluateSweepRelease(context.Background(), network, time.Now().Add(-time.Minute), item)
	if decision.action != "quarantined" {
		t.Fatalf("used authorization decision=%+v", decision)
	}
}

func TestEvaluateSweepReleaseSignedETHRequiresAdvancedNonce(t *testing.T) {
	nonce := "0x7"
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		if method == "eth_getBlockByNumber" {
			return map[string]any{"number": "0x20", "hash": "0x" + strings.Repeat("aa", 32)}
		}
		if method == "eth_getTransactionCount" {
			return nonce
		}
		return nil
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	server := &PurserServer{rpcClient: handlers.NewRPCClient()}
	item := sweepReleaseItem{
		asset: "ETH", sourceAddress: "0x2222222222222222222222222222222222222222", amount: big.NewInt(1),
		sourceNonce: sql.NullInt64{Int64: 7, Valid: true}, signedPayload: sql.NullString{String: "0xsigned", Valid: true},
		status: "signed", claimedSources: 1,
	}
	decision := server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item)
	if decision.action != "quarantined" {
		t.Fatalf("unchanged nonce decision=%+v", decision)
	}
	nonce = "0x8"
	decision = server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item)
	if decision.action != "released" {
		t.Fatalf("advanced nonce decision=%+v", decision)
	}
}

func TestEvaluateSweepReleaseBroadcastOutcomeMustBeCanonical(t *testing.T) {
	receiptHash := "0x" + strings.Repeat("aa", 32)
	canonicalHash := receiptHash
	receiptAvailable := true
	rpcServer := sweepRPCServer(t, func(method string, params []any) any {
		switch method {
		case "eth_getTransactionReceipt":
			if !receiptAvailable {
				return nil
			}
			return map[string]any{
				"transactionHash": "0xtx", "blockNumber": "0x10", "blockHash": receiptHash, "status": "0x0",
			}
		case "eth_getBlockByNumber":
			if len(params) > 0 && params[0] == "finalized" {
				return map[string]any{"number": "0x20", "hash": "0x" + strings.Repeat("bb", 32)}
			}
			return map[string]any{"number": "0x10", "hash": canonicalHash}
		default:
			return nil
		}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	server := &PurserServer{rpcClient: handlers.NewRPCClient()}
	item := sweepReleaseItem{
		asset: "ETH", sourceAddress: "0x2222222222222222222222222222222222222222", amount: big.NewInt(1),
		txHash: sql.NullString{String: "0xtx", Valid: true}, status: "broadcast", claimedSources: 1,
	}
	if decision := server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item); decision.action != "released" {
		t.Fatalf("canonical revert decision=%+v", decision)
	}
	canonicalHash = "0x" + strings.Repeat("cc", 32)
	if decision := server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item); decision.action != "quarantined" {
		t.Fatalf("noncanonical receipt decision=%+v", decision)
	}
	receiptAvailable = false
	if decision := server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item); decision.action != "quarantined" {
		t.Fatalf("missing receipt decision=%+v", decision)
	}
	item.claimedSources = 0
	if decision := server.evaluateSweepRelease(context.Background(), handlers.Networks["base"], time.Now().Add(-time.Minute), item); decision.action != "blocked" {
		t.Fatalf("duplicate release decision=%+v", decision)
	}
}

func TestReleaseCryptoSweepExpiredUnsignedClaimIsAuditedAndReplannable(t *testing.T) { //nolint:funlen // SQL expectations prove the entire release state transition.
	const (
		batchID   = "11111111-1111-1111-1111-111111111111"
		itemID    = "22222222-2222-2222-2222-222222222222"
		address   = "0x3333333333333333333333333333333333333333"
		checksum  = "manifest-checksum"
		blockHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		switch method {
		case "eth_getBlockByNumber":
			return map[string]any{"number": "0x64", "hash": blockHash, "baseFeePerGas": "0x1"}
		case "eth_getBalance":
			return "0x3e8"
		default:
			return nil
		}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT manifest_checksum FROM purser.crypto_sweep_batches`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"manifest_checksum"}).AddRow(checksum))
	mock.ExpectQuery(`SELECT manifest_checksum, network, expires_at`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"manifest_checksum", "network", "expires_at"}).
			AddRow(checksum, "base", time.Now().Add(-time.Hour)))
	mock.ExpectQuery(`SELECT item.id::text AS item_id, item.asset, LOWER\(item.source_address\)::text`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "asset", "source_address", "amount", "source_nonce", "authorization_nonce", "authorization_before",
			"signed_payload", "relay_transaction", "tx_hash", "status", "broadcast_at", "claimed_sources",
		}).AddRow(itemID, "ETH", address, "500", nil, nil, nil, nil, nil, nil, "planned", nil, int64(1)))
	mock.ExpectQuery(`SELECT snapshot_block, snapshot_block_hash`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot_block", "snapshot_block_hash"}).AddRow(int64(100), blockHash))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, signed_payload, relay_transaction, tx_hash, broadcast_at`).WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "signed_payload", "relay_transaction", "tx_hash", "broadcast_at"}).
			AddRow("planned", nil, nil, nil, nil))
	mock.ExpectExec(`UPDATE purser.crypto_sweep_sources`).
		WithArgs("released", "", "operator recovery: expired unsigned manifest passed canonical balance recheck", itemID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser.crypto_sweep_items`).
		WithArgs("expired", "expired unsigned manifest passed canonical balance recheck", itemID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser.crypto_sweep_events`).
		WithArgs(batchID, itemID, "claim_released", "", "operator recovery", "expired unsigned manifest passed canonical balance recheck").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectExec(`UPDATE purser.crypto_sweep_batches SET status = \$3`).
		WithArgs("", "operator recovery", "expired", int32(1), int32(0), int32(0), batchID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	response, err := server.ReleaseCryptoSweep(sweepServiceCtx(), &purserpb.ReleaseCryptoSweepRequest{
		BatchId: batchID, Reason: "operator recovery", DryRun: false,
		CeremonyAck: sweepCeremonyPrefix + checksum,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetStatus() != "expired" || response.GetReleasedItems() != 1 || response.GetQuarantinedItems() != 0 {
		t.Fatalf("release response=%+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseCryptoSweepRejectsAcknowledgementMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	batchID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery(`SELECT manifest_checksum FROM purser.crypto_sweep_batches`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"manifest_checksum"}).AddRow("expected"))
	server := &PurserServer{db: db}
	_, err = server.ReleaseCryptoSweep(sweepServiceCtx(), &purserpb.ReleaseCryptoSweepRequest{
		BatchId: batchID, Reason: "operator recovery", CeremonyAck: sweepCeremonyPrefix + "wrong",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("error=%v, want FailedPrecondition", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCryptoSweepRPCsRejectUnmarkedContext(t *testing.T) {
	server := &PurserServer{}
	if _, err := server.GetCryptoReadiness(context.Background(), nil); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("readiness error=%v, want PermissionDenied", err)
	}
	if _, err := server.ReleaseCryptoSweep(context.Background(), &purserpb.ReleaseCryptoSweepRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("release error=%v, want PermissionDenied", err)
	}
	if _, err := server.ResolveX402MutationResult(context.Background(), &purserpb.ResolveX402MutationResultRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mutation resolution error=%v, want PermissionDenied", err)
	}
}

func TestReleaseCryptoSweepConcurrentItemChangePreservesAborted(t *testing.T) {
	const (
		batchID   = "11111111-1111-1111-1111-111111111111"
		itemID    = "22222222-2222-2222-2222-222222222222"
		address   = "0x3333333333333333333333333333333333333333"
		checksum  = "manifest-checksum"
		blockHash = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		switch method {
		case "eth_getBlockByNumber":
			return map[string]any{"number": "0x64", "hash": blockHash, "baseFeePerGas": "0x1"}
		case "eth_getBalance":
			return "0x3e8"
		default:
			return nil
		}
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT manifest_checksum FROM purser.crypto_sweep_batches`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"manifest_checksum"}).AddRow(checksum))
	mock.ExpectQuery(`SELECT manifest_checksum, network, expires_at`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"manifest_checksum", "network", "expires_at"}).
			AddRow(checksum, "base", time.Now().Add(-time.Hour)))
	mock.ExpectQuery(`SELECT item.id::text AS item_id, item.asset, LOWER\(item.source_address\)::text`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "asset", "source_address", "amount", "source_nonce", "authorization_nonce", "authorization_before",
			"signed_payload", "relay_transaction", "tx_hash", "status", "broadcast_at", "claimed_sources",
		}).AddRow(itemID, "ETH", address, "500", nil, nil, nil, nil, nil, nil, "planned", nil, int64(1)))
	mock.ExpectQuery(`SELECT snapshot_block, snapshot_block_hash`).WithArgs(batchID).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot_block", "snapshot_block_hash"}).AddRow(int64(100), blockHash))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT status, signed_payload, relay_transaction, tx_hash, broadcast_at`).WithArgs(itemID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "signed_payload", "relay_transaction", "tx_hash", "broadcast_at"}).
			AddRow("signed", "0xsigned-after-evaluation", nil, nil, nil))
	mock.ExpectRollback()
	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	_, err = server.ReleaseCryptoSweep(serviceTestContext(), &purserpb.ReleaseCryptoSweepRequest{
		BatchId: batchID, Reason: "operator recovery", CeremonyAck: sweepCeremonyPrefix + checksum,
	})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("error=%v, want Aborted", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBroadcastCryptoSweepDuplicateReplaysSameETHIntent(t *testing.T) {
	key, err := ethcrypto.HexToECDSA("4f3edf983ac63ad7c43a0d8a05d3d4a6c67e594e0d56e4b1d72c05b90f3e6f7a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	treasury := "0x1111111111111111111111111111111111111111"
	item := cryptosweep.ManifestItem{
		ItemID: "22222222-2222-2222-2222-222222222222", CustodyAddressID: "33333333-3333-3333-3333-333333333333",
		Asset: "ETH", SourceAddress: ethcrypto.PubkeyToAddress(key.PublicKey).Hex(), DestinationAddress: treasury,
		AmountBaseUnits: "1000000000000000", SourceNonce: 7, MaxFeePerGas: "3000000000",
		MaxPriorityFeePerGas: "1000000000", GasLimit: 21000,
	}
	manifest := cryptosweep.Manifest{
		BatchID: "11111111-1111-1111-1111-111111111111", Network: "base", ChainID: 8453,
		TreasuryAddress: treasury, XPub: "xpub-test", SnapshotBlock: 100,
		SnapshotBlockHash: "0x" + strings.Repeat("aa", 32), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		Items: []cryptosweep.ManifestItem{item},
	}
	if finalizeErr := manifest.Finalize(); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: big.NewInt(manifest.ChainID), Nonce: item.SourceNonce, GasTipCap: big.NewInt(1_000_000_000),
		GasFeeCap: big.NewInt(3_000_000_000), Gas: item.GasLimit,
		To: ptrAddress(common.HexToAddress(treasury)), Value: big.NewInt(1_000_000_000_000_000),
	})
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(manifest.ChainID)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signedTx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	bundle := cryptosweep.SignedBundle{
		Manifest: manifest, ManifestChecksum: manifest.Checksum, SignedAt: now,
		Items: []cryptosweep.SignedBundleItem{{ItemID: item.ItemID, RawTransaction: "0x" + hex.EncodeToString(raw)}},
	}
	if finalizeErr := bundle.Finalize(); finalizeErr != nil {
		t.Fatal(finalizeErr)
	}
	payload, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	rpcCalls := 0
	rpcServer := sweepRPCServer(t, func(method string, _ []any) any {
		if method == "eth_sendRawTransaction" {
			rpcCalls++
			return signedTx.Hash().Hex()
		}
		return nil
	})
	defer rpcServer.Close()
	t.Setenv("BASE_RPC_ENDPOINT", rpcServer.URL)
	t.Setenv("CRYPTO_TREASURY_BASE", treasury)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 2 {
		mock.ExpectQuery(`SELECT manifest_checksum, network, LOWER\(treasury_address\)::text AS treasury_address`).WithArgs(manifest.BatchID).
			WillReturnRows(sqlmock.NewRows([]string{
				"manifest_checksum", "network", "treasury_address", "snapshot_block", "snapshot_block_hash", "status", "expires_at",
			}).AddRow(manifest.Checksum, manifest.Network, strings.ToLower(treasury), manifest.SnapshotBlock, manifest.SnapshotBlockHash, "broadcast", manifest.ExpiresAt))
		mock.ExpectQuery(`SELECT status, tx_hash, relay_transaction`).WithArgs(item.ItemID, manifest.BatchID).
			WillReturnRows(sqlmock.NewRows([]string{"status", "tx_hash", "relay_transaction"}).AddRow("broadcast", signedTx.Hash().Hex(), nil))
		mock.ExpectExec(`UPDATE purser.crypto_sweep_items`).WithArgs("0x"+hex.EncodeToString(raw), signedTx.Hash().Hex(), item.ItemID).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`UPDATE purser.crypto_sweep_batches SET status = 'broadcast'`).
			WithArgs("", bundle.Checksum, manifest.BatchID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	server := &PurserServer{db: db, rpcClient: handlers.NewRPCClient()}
	request := &purserpb.BroadcastCryptoSweepRequest{
		SignedBundleJson: payload, CeremonyAck: sweepCeremonyPrefix + bundle.Checksum,
	}
	first, err := server.BroadcastCryptoSweep(sweepServiceCtx(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := server.BroadcastCryptoSweep(sweepServiceCtx(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.GetItems()[0].GetTxHash() != second.GetItems()[0].GetTxHash() || rpcCalls != 2 {
		t.Fatalf("duplicate broadcast changed intent: first=%+v second=%+v calls=%d", first, second, rpcCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySweepBundleItemBindsUSDCAuthorization(t *testing.T) {
	key, err := ethcrypto.HexToECDSA("6cbed15c177e12c9fab71e7bf88b1a9941b3f2d5db6f6f13b6f67d4dcf3d2c5a")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := cryptosweep.Manifest{ChainID: 8453}
	item := cryptosweep.ManifestItem{
		ItemID: "item", Asset: "USDC", SourceAddress: ethcrypto.PubkeyToAddress(key.PublicKey).Hex(),
		DestinationAddress: "0x1111111111111111111111111111111111111111", AmountBaseUnits: "5000000",
		AssetContract:      "0x3333333333333333333333333333333333333333",
		AuthorizationNonce: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AuthorizationAfter: now.Add(-time.Minute).Unix(), AuthorizationBefore: now.Add(time.Hour).Unix(),
		TokenDomainName: "USD Coin", TokenDomainVersion: "2",
	}
	digest, err := cryptosweep.EIP3009Digest(manifest, item)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := ethcrypto.Sign(digest, key)
	if err != nil {
		t.Fatal(err)
	}
	signature[64] += 27
	signed := cryptosweep.SignedBundleItem{ItemID: item.ItemID, AuthorizationSignature: "0x" + hex.EncodeToString(signature)}
	if err := verifySweepBundleItem(manifest, item, signed); err != nil {
		t.Fatalf("valid signed USDC item rejected: %v", err)
	}

	tests := map[string]func(*cryptosweep.Manifest, *cryptosweep.ManifestItem){
		"wrong chain": func(m *cryptosweep.Manifest, _ *cryptosweep.ManifestItem) { m.ChainID = 1 },
		"wrong token": func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) {
			i.AssetContract = "0x4444444444444444444444444444444444444444"
		},
		"wrong destination": func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) {
			i.DestinationAddress = "0x2222222222222222222222222222222222222222"
		},
		"changed amount": func(_ *cryptosweep.Manifest, i *cryptosweep.ManifestItem) { i.AmountBaseUnits = "1" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidateManifest, candidateItem := manifest, item
			mutate(&candidateManifest, &candidateItem)
			if err := verifySweepBundleItem(candidateManifest, candidateItem, signed); err == nil {
				t.Fatal("altered signed USDC intent unexpectedly verified")
			}
		})
	}
}
