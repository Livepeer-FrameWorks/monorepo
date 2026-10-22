//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
)

// TestLivepeerFundingSignerLostLock_RealPG runs the lost-signer-lock contract
// on PostgreSQL, where a session drops its advisory locks with
// pg_advisory_unlock_all while its later statements keep succeeding.
func TestLivepeerFundingSignerLostLock_RealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-livepeer-funding-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err = dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.Exec(string(schema)); err != nil {
		t.Fatalf("apply purser schema: %v", err)
	}
	runLostFundingSignerLockContract(t, admin, dsn, func(t *testing.T, signer driver.Conn) {
		t.Helper()
		if _, err := signer.(driver.ExecerContext).ExecContext(context.Background(), `SELECT pg_advisory_unlock_all()`, nil); err != nil {
			t.Fatalf("drop the signer session's locks: %v", err)
		}
	})
}

// TestLivepeerFundingSignerLostLock_RealYugabyte runs the lost-signer-lock
// contract on YugabyteDB, where an expired session-level lock transaction
// drops the session's advisory locks while its ordinary writes keep working.
func TestLivepeerFundingSignerLostLock_RealYugabyte(t *testing.T) {
	admin, dsn, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "livepeer_funding", "purser")
	if !ok {
		t.Fatal("run make verify-yugabyte-purser-contracts to provide the isolated Yugabyte fixture")
	}
	runLostFundingSignerLockContract(t, admin, dsn, func(t *testing.T, _ driver.Conn) {
		t.Helper()
		// yb_get_current_transaction reports the session's lock transaction
		// only until the session runs a transaction of its own, so the lock
		// transaction is read from the signer key's lock. Its key columns are
		// the database, the high and the low 32 bits of the lock key.
		var transaction string
		if err := admin.QueryRowContext(context.Background(), `
			SELECT ybdetails->>'transactionid' FROM pg_locks
			WHERE locktype = 'advisory' AND granted
			  AND (ybdetails->'keyrangedetails'->'cols'->>2)::bigint = (hashtext('purser-livepeer-funding-signer')::bigint & 4294967295)`).Scan(&transaction); err != nil {
			t.Fatalf("read the signer lock's transaction: %v", err)
		}
		var cancelled bool
		if err := admin.QueryRowContext(context.Background(), `SELECT yb_cancel_transaction($1::uuid)`, transaction).Scan(&cancelled); err != nil || !cancelled {
			t.Fatalf("cancel the signer session's lock transaction %q = %v, %v", transaction, cancelled, err)
		}
	})
}

// recordingConnector hands out PostgreSQL sessions and keeps each one, so a
// test can act on the session that holds the signer lock while the monitor
// waits on the chain.
type recordingConnector struct {
	driver.Connector
	mu    sync.Mutex
	conns []driver.Conn
}

func (c *recordingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.conns = append(c.conns, conn)
	c.mu.Unlock()
	return conn, nil
}

func (c *recordingConnector) first(t *testing.T) driver.Conn {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.conns) == 0 {
		t.Fatal("the replica opened no session")
	}
	return c.conns[0]
}

// fundingChain is an Arbitrum RPC fake shared by two replicas, told apart by
// URL path. Gateway readings stay low, as they do while a top-up is unmined.
// A gate holds one replica's request for a method until the test releases it.
// The funding wallet's sent transactions are mined at once unless holdMining
// is set; hideMempool makes the pending nonce read as the latest one, as an
// RPC node that has not seen the wallet's unmined transaction reports it.
type fundingChain struct {
	t           *testing.T
	mu          sync.Mutex
	sends       map[string]int
	gates       map[string]*fundingGate
	all         []*fundingGate
	latest      int
	pending     int
	unmined     []string
	mined       map[string]bool
	holdMining  bool
	hideMempool bool
}

type fundingGate struct {
	arrived chan struct{}
	release chan struct{}
	once    sync.Once
}

// open lets the held request through.
func (g *fundingGate) open() { g.once.Do(func() { close(g.release) }) }

func newFundingChain(t *testing.T) *fundingChain {
	return &fundingChain{t: t, sends: map[string]int{}, gates: map[string]*fundingGate{}, mined: map[string]bool{}}
}

// mine includes every unmined transaction of the wallet in a block.
func (c *fundingChain) mine() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, hash := range c.unmined {
		c.mined[hash] = true
	}
	c.unmined = nil
	c.latest = c.pending
}

// gate holds the first request of method from replica until the gate opens.
func (c *fundingChain) gate(replica, method string) *fundingGate {
	g := &fundingGate{arrived: make(chan struct{}), release: make(chan struct{})}
	c.mu.Lock()
	c.gates[replica+" "+method] = g
	c.all = append(c.all, g)
	c.mu.Unlock()
	return g
}

// openAll lets every held request through, so a failed test can close the
// server.
func (c *fundingChain) openAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.all {
		g.open()
	}
}

func (c *fundingChain) sent(replica string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sends[replica]
}

func (c *fundingChain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.t.Errorf("decode RPC request: %v", err)
		return
	}
	replica := strings.TrimPrefix(r.URL.Path, "/")
	c.mu.Lock()
	g := c.gates[replica+" "+req.Method]
	delete(c.gates, replica+" "+req.Method)
	c.mu.Unlock()
	if g != nil {
		close(g.arrived)
		<-g.release
	}
	w.Header().Set("Content-Type", "application/json")
	reply := func(result string) { _, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result) }
	switch req.Method {
	case "eth_getBalance":
		reply(`"0xde0b6b3a7640000"`)
	case "eth_call":
		reply(`"0x` + strings.Repeat("0", 4*64) + `"`)
	case "eth_getTransactionCount":
		var block string
		if len(req.Params) == 2 {
			_ = json.Unmarshal(req.Params[1], &block)
		}
		c.mu.Lock()
		nonce := c.latest
		if block == "pending" && !c.hideMempool {
			nonce = c.pending
		}
		c.mu.Unlock()
		reply(fmt.Sprintf(`"0x%x"`, nonce))
	case "eth_gasPrice":
		reply(`"0x1"`)
	case "eth_estimateGas":
		reply(`"0x186a0"`)
	case "eth_sendRawTransaction":
		var rawHex string
		if len(req.Params) != 1 || json.Unmarshal(req.Params[0], &rawHex) != nil {
			c.t.Errorf("invalid send params: %s", req.Params)
			return
		}
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(common.FromHex(rawHex)); err != nil {
			c.t.Errorf("decode signed transaction: %v", err)
			return
		}
		c.mu.Lock()
		c.sends[replica]++
		c.pending++
		c.unmined = append(c.unmined, tx.Hash().Hex())
		hold := c.holdMining
		c.mu.Unlock()
		if !hold {
			c.mine()
		}
		reply(fmt.Sprintf("%q", tx.Hash().Hex()))
	case "eth_getTransactionReceipt":
		var hash string
		if len(req.Params) == 1 {
			_ = json.Unmarshal(req.Params[0], &hash)
		}
		c.mu.Lock()
		mined := c.mined[hash]
		c.mu.Unlock()
		if !mined {
			reply(`null`)
			return
		}
		reply(`{"status":"0x1"}`)
	default:
		c.t.Errorf("unexpected RPC method %q", req.Method)
	}
}

// runLostFundingSignerLockContract runs two Purser replicas that share one
// funding wallet against one database. loseLock drops the signer lock of the
// replica's session while that session stays usable for writes, as a
// YugabyteDB lock transaction that expires during a storage stall does. It
// also runs one replica whose receipt wait gives up on an unmined top-up.
func runLostFundingSignerLockContract(t *testing.T, admin *sql.DB, dsn string, loseLock func(*testing.T, driver.Conn)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gateway := "0x" + strings.Repeat("56", 20)
	key, err := crypto.HexToECDSA(testFundingPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	wallet := strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	oneETH := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	type replica struct {
		monitor   *LivepeerDepositMonitor
		connector *recordingConnector
	}
	newReplica := func(t *testing.T, server *httptest.Server, name string) replica {
		t.Helper()
		base, err := pq.NewConnector(dsn)
		if err != nil {
			t.Fatal(err)
		}
		connector := &recordingConnector{Connector: base}
		db := sql.OpenDB(connector)
		t.Cleanup(func() { _ = db.Close() })
		return replica{connector: connector, monitor: &LivepeerDepositMonitor{
			logger: logging.NewLogger(), db: db, rpcEndpoint: server.URL + "/" + name,
			gasWalletPrivKey: testFundingPrivateKey, gasWalletAddress: wallet,
			depositLowThreshold: 1, reserveLowThreshold: 1,
			topupAmountWei: oneETH, dailyCapWei: new(big.Int).Mul(oneETH, big.NewInt(100)),
			receiptTimeout: 30 * time.Second, receiptPollInterval: 10 * time.Millisecond,
			topupCounter: prometheus.NewCounter(prometheus.CounterOpts{Name: "test_topups"}),
			alertCounter: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_alerts"}, []string{"reason"}),
		}}
	}
	low := &GatewayDepositState{Address: gateway, DepositLow: true, ReserveLow: true}
	attempts := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.livepeer_funding_attempts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	wait := func(t *testing.T, ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, `DELETE FROM purser.livepeer_funding_attempts`); err != nil {
			t.Fatal(err)
		}
	}

	// A sends its top-up and waits for it to be mined. Its signer lock is
	// lost meanwhile and B takes it: B reads the still-low deposit, and must
	// not send a second top-up from the same wallet while A's is in flight.
	t.Run("lock lost while a top-up is unmined", func(t *testing.T) {
		reset(t)
		chain := newFundingChain(t)
		server := httptest.NewServer(chain)
		defer server.Close()
		defer chain.openAll()
		a, b := newReplica(t, server, "a"), newReplica(t, server, "b")
		receipt := chain.gate("a", "eth_getTransactionReceipt")
		aDone := make(chan struct{})
		go func() {
			defer close(aDone)
			a.monitor.fundTicketBroker(ctx, low, "a")
		}()
		wait(t, receipt.arrived, "A to wait for its receipt")
		loseLock(t, a.connector.first(t))

		b.monitor.fundTicketBroker(ctx, low, "b")
		receipt.open()
		wait(t, aDone, "A to finish")

		if chain.sent("a") != 1 || chain.sent("b") != 0 {
			t.Fatalf("top-ups sent: A=%d B=%d; want only A's, B must not fund while A's top-up is unmined", chain.sent("a"), chain.sent("b"))
		}
		if n := attempts(t); n != 1 {
			t.Fatalf("funding attempts = %d, want 1", n)
		}
	})

	// A loses its signer lock before reserving and B takes it. A, no longer
	// the signer, must neither reserve nor send; B, the holder, funds.
	t.Run("lock lost to another holder before reserving", func(t *testing.T) {
		reset(t)
		chain := newFundingChain(t)
		server := httptest.NewServer(chain)
		defer server.Close()
		defer chain.openAll()
		a, b := newReplica(t, server, "a"), newReplica(t, server, "b")
		aRead, bRead := chain.gate("a", "eth_getBalance"), chain.gate("b", "eth_getBalance")
		aDone, bDone := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(aDone)
			a.monitor.fundTicketBroker(ctx, low, "a")
		}()
		wait(t, aRead.arrived, "A to re-read the gateway under its lock")
		loseLock(t, a.connector.first(t))
		go func() {
			defer close(bDone)
			b.monitor.fundTicketBroker(ctx, low, "b")
		}()
		wait(t, bRead.arrived, "B to take the signer lock")

		aRead.open()
		wait(t, aDone, "A to finish")
		if chain.sent("a") != 0 || attempts(t) != 0 {
			t.Fatalf("A, which lost the signer lock to B, sent %d top-ups and left %d attempts; want none", chain.sent("a"), attempts(t))
		}
		bRead.open()
		wait(t, bDone, "B to finish")
		if chain.sent("b") != 1 || attempts(t) != 1 {
			t.Fatalf("B, the signer lock holder, sent %d top-ups with %d attempts; want 1 and 1", chain.sent("b"), attempts(t))
		}
	})

	// The receipt wait gives up while the top-up is still in the mempool. The
	// next cycle reads the gateway as low again, but the wallet's pending
	// nonce shows the unmined top-up, so it must not send another.
	t.Run("receipt wait timed out while the top-up is pending", func(t *testing.T) {
		reset(t)
		chain := newFundingChain(t)
		chain.holdMining = true
		server := httptest.NewServer(chain)
		defer server.Close()
		a := newReplica(t, server, "a")
		a.monitor.receiptTimeout = 200 * time.Millisecond
		a.monitor.fundTicketBroker(ctx, low, "a")
		if chain.sent("a") != 1 {
			t.Fatalf("first cycle sent %d top-ups, want 1", chain.sent("a"))
		}
		a.monitor.fundTicketBroker(ctx, low, "a")
		if chain.sent("a") != 1 {
			t.Fatalf("top-ups sent = %d; the second cycle funded again while the first top-up was pending", chain.sent("a"))
		}
	})

	// The RPC node does not show the pending top-up in the wallet's nonce,
	// so only the attempt whose receipt wait timed out shows it is in flight.
	// Once the top-up is mined, the next cycle records its receipt and the
	// attempt no longer blocks funding.
	t.Run("timed-out attempt stays in flight until its receipt is found", func(t *testing.T) {
		reset(t)
		chain := newFundingChain(t)
		chain.holdMining, chain.hideMempool = true, true
		server := httptest.NewServer(chain)
		defer server.Close()
		a := newReplica(t, server, "a")
		a.monitor.receiptTimeout = 200 * time.Millisecond
		a.monitor.fundTicketBroker(ctx, low, "a")
		a.monitor.fundTicketBroker(ctx, low, "a")
		if chain.sent("a") != 1 {
			t.Fatalf("top-ups sent = %d; the second cycle funded again while the timed-out top-up had no receipt", chain.sent("a"))
		}

		chain.mine()
		chain.mu.Lock()
		chain.holdMining = false
		chain.mu.Unlock()
		a.monitor.fundTicketBroker(ctx, low, "a")
		var unconfirmed int
		if err := admin.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.livepeer_funding_attempts WHERE error_message IS NOT NULL`).Scan(&unconfirmed); err != nil {
			t.Fatal(err)
		}
		if unconfirmed != 0 || chain.sent("a") != 2 {
			t.Fatalf("after the top-up was mined: %d attempts still carry an error and %d top-ups were sent; want the receipt recorded and the next top-up sent", unconfirmed, chain.sent("a"))
		}
	})
}
