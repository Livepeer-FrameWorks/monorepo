//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// fakeFinalityChain serves the JSON-RPC calls of one scanner tick for a chain
// whose finalized head trails its latest head, as Arbitrum's does.
type fakeFinalityChain struct {
	mu        sync.Mutex
	latest    int64
	finalized int64
}

func (c *fakeFinalityChain) set(latest, finalized int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest, c.finalized = latest, finalized
}

func fakeBlockHash(number int64) string { return fmt.Sprintf("0x%064x", number) }

func (c *fakeFinalityChain) result(method string, params []any) any {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch method {
	case "eth_blockNumber":
		return fmt.Sprintf("0x%x", c.latest)
	case "eth_getBlockByNumber":
		number := c.finalized
		if tag, _ := params[0].(string); tag != "finalized" {
			_, _ = fmt.Sscanf(tag, "0x%x", &number)
		}
		return map[string]any{
			"number": fmt.Sprintf("0x%x", number), "hash": fakeBlockHash(number),
			"parentHash": fakeBlockHash(number - 1), "timestamp": "0x0", "transactions": []any{},
		}
	case "eth_getLogs":
		return []any{}
	}
	return nil
}

func (c *fakeFinalityChain) serve(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		type call struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		if strings.HasPrefix(strings.TrimSpace(string(body)), "[") {
			var calls []call
			_ = json.Unmarshal(body, &calls)
			out := make([]map[string]any, len(calls))
			for i, item := range calls {
				out[i] = map[string]any{"jsonrpc": "2.0", "id": item.ID, "result": c.result(item.Method, item.Params)}
			}
			_ = json.NewEncoder(writer).Encode(out)
			return
		}
		var item call
		_ = json.Unmarshal(body, &item)
		_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": item.ID, "result": c.result(item.Method, item.Params)})
	}))
	t.Cleanup(server.Close)
	appconfigtest.Set(t, Networks["arbitrum"].RPCEndpointEnv, server.URL)
}

type scanCursorState struct {
	next, lag int64
	readiness error
}

func readScanCursor(t *testing.T, db *sql.DB, now time.Time) scanCursorState {
	t.Helper()
	ctx := context.Background()
	var next int64
	if err := db.QueryRowContext(ctx, `SELECT next_block FROM purser.crypto_scan_cursors WHERE network = 'arbitrum'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	row, err := purserdb.New(db).GetCryptoScannerReadiness(ctx, "arbitrum")
	if err != nil {
		t.Fatal(err)
	}
	return scanCursorState{next: next, lag: row.LagBlocks.Int64, readiness: CryptoScannerReadinessError(row.ScannedAt, row.LastError, row.LagBlocks, now)}
}

// A healthy scanner reaches Arbitrum's finalized head within one tick however far
// the head jumped, keeps its readiness while it waits caught up for the next
// jump with the latest head thousands of blocks ahead, and loses readiness when
// it has not reached the finalized head for longer than the catch-up limit.
func TestCryptoScannerReadinessFollowsFinalizedCatchUp_RealPG(t *testing.T) {
	appconfigtest.Set(t, "BUILD_ENV", "production")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	chain := &fakeFinalityChain{}
	chain.serve(t)
	monitor := &CryptoMonitor{db: db, logger: logging.NewLogger(), rpc: NewRPCClient()}
	network := Networks["arbitrum"]

	// Caught up at the previous finalized head 8_000 since ten minutes ago.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.crypto_scan_cursors
			(network, next_block, last_scanned_block, last_scanned_block_hash, safe_head_block, lag_blocks, scanned_at)
		VALUES ('arbitrum', 8001, 8000, $1, 8000, 0, NOW() - INTERVAL '10 minutes')`, fakeBlockHash(8000)); err != nil {
		t.Fatal(err)
	}

	// The finalized head jumps 1_300 blocks, as production observed: one tick
	// scans all of them.
	chain.set(13_300, 9_300)
	if err := monitor.scanRPCNetwork(ctx, network); err != nil {
		t.Fatalf("scan after a finality jump: %v", err)
	}
	if got := readScanCursor(t, db, time.Now()); got.next != 9_301 || got.lag != 0 || got.readiness != nil {
		t.Fatalf("after a 1300-block finality jump: next=%d lag=%d readiness=%v, want next 9301, lag 0 and ready", got.next, got.lag, got.readiness)
	}

	// Waiting caught up for the next jump while the latest head runs 4_000
	// blocks ahead of the finalized head is healthy.
	if _, err := db.ExecContext(ctx, `UPDATE purser.crypto_scan_cursors SET scanned_at = NOW() - INTERVAL '3 minutes' WHERE network = 'arbitrum'`); err != nil {
		t.Fatal(err)
	}
	chain.set(13_400, 9_300)
	if err := monitor.scanRPCNetwork(ctx, network); err != nil {
		t.Fatalf("caught-up scan: %v", err)
	}
	if got := readScanCursor(t, db, time.Now()); got.next != 9_301 || got.lag != 0 || got.readiness != nil {
		t.Fatalf("caught up with latest 4100 blocks ahead: next=%d lag=%d readiness=%v, want lag 0 and ready", got.next, got.lag, got.readiness)
	}

	// A jump larger than one tick's bound advances the cursor without counting
	// as caught up; the next tick finishes it.
	if _, err := db.ExecContext(ctx, `UPDATE purser.crypto_scan_cursors SET scanned_at = NOW() - INTERVAL '10 minutes' WHERE network = 'arbitrum'`); err != nil {
		t.Fatal(err)
	}
	jump := cryptoScanTickMaxBatches*cryptoScanBatchBlocks + 1_000
	chain.set(9_300+jump+4_000, 9_300+jump)
	if err := monitor.scanRPCNetwork(ctx, network); err != nil {
		t.Fatalf("bounded scan: %v", err)
	}
	got := readScanCursor(t, db, time.Now())
	if got.next != 9_301+cryptoScanTickMaxBatches*cryptoScanBatchBlocks || got.lag != 1_000 || got.readiness == nil {
		t.Fatalf("after a jump past the tick bound: next=%d lag=%d readiness=%v, want the bound scanned, 1000 left and not ready", got.next, got.lag, got.readiness)
	}
	if err := monitor.scanRPCNetwork(ctx, network); err != nil {
		t.Fatalf("finishing scan: %v", err)
	}
	if got := readScanCursor(t, db, time.Now()); got.next != 9_301+jump || got.lag != 0 || got.readiness != nil {
		t.Fatalf("after the finishing tick: next=%d lag=%d readiness=%v, want caught up and ready", got.next, got.lag, got.readiness)
	}

	// Not reaching the finalized head for longer than the limit loses readiness.
	if got := readScanCursor(t, db, time.Now().Add(CryptoScannerMaxCatchUpAge+time.Second)); got.readiness == nil {
		t.Fatal("a scanner not caught up for longer than the limit stayed ready")
	}
}
