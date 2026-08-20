//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestEmbeddedFacilitatorSerializesRelayerNoncesAcrossReplicas_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	gasAddress, err := deriveAddressFromPrivKey(durableBroadcastTestPrivateKey)
	if err != nil {
		t.Fatal(err)
	}

	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var rpcRequest struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(request.Body).Decode(&rpcRequest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := "0x3b9aca00"
		if rpcRequest.Method == "eth_getTransactionCount" {
			result = "0x5"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	}))
	defer rpcServer.Close()
	network := Networks["base"]
	t.Setenv(network.RPCEndpointEnv, rpcServer.URL)

	settlementIDs := []string{uuid.NewString(), uuid.NewString()}
	for index, settlementID := range settlementIDs {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.x402_nonces (
				id, network, payer_address, nonce, tenant_id, amount_cents, status
			) VALUES ($1, $2, $3, $4, $5, 500, 'submitting')
		`, settlementID, network.Name, "0x1111111111111111111111111111111111111111",
			uuid.NewString(), uuid.NewString()); err != nil {
			t.Fatalf("insert settlement %d: %v", index, err)
		}
	}

	start := make(chan struct{})
	errorsByReplica := make(chan error, len(settlementIDs))
	var wg sync.WaitGroup
	for index, settlementID := range settlementIDs {
		wg.Add(1)
		go func(index int, settlementID string) {
			defer wg.Done()
			<-start
			handler := &X402Handler{
				db: db, rpc: NewRPCClient(), gasWalletPrivKey: durableBroadcastTestPrivateKey,
				gasWalletAddress: gasAddress,
			}
			_, err := handler.prepareEmbeddedSettlementAttempt(ctx, settlementID, network,
				network.USDCContract, []byte{byte(index + 1)})
			errorsByReplica <- err
		}(index, settlementID)
	}
	close(start)
	wg.Wait()
	close(errorsByReplica)
	for err := range errorsByReplica {
		if err != nil {
			t.Fatal(err)
		}
	}

	rows, err := db.QueryContext(ctx, `
		SELECT relayer_nonce FROM purser.x402_settlement_attempts
		WHERE settlement_id = ANY($1::uuid[])
	`, "{"+settlementIDs[0]+","+settlementIDs[1]+"}")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var nonces []int
	for rows.Next() {
		var nonce int
		if err := rows.Scan(&nonce); err != nil {
			t.Fatal(err)
		}
		nonces = append(nonces, nonce)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Ints(nonces)
	if len(nonces) != 2 || nonces[0] != 5 || nonces[1] != 6 {
		t.Fatalf("relayer nonces = %v, want [5 6]", nonces)
	}
}
