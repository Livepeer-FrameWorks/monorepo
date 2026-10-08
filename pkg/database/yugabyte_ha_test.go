//go:build yugabyte_ha || yugabyte_measure

package database

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	ybpgx "github.com/yugabyte/pgx/v5"
	ybstdlib "github.com/yugabyte/pgx/v5/stdlib"
)

type yugabyteHANode struct {
	name     string
	hostPort string
	address  string
}

// yugabyteHAExtraTServerFlags is appended, comma-separated, to the tserver flags every node of the next cluster starts
// with.
var yugabyteHAExtraTServerFlags string

const yugabyteHAMaxOpenConns = 16

type yugabyteHACluster struct {
	network       string
	addressPrefix string
	nodes         []yugabyteHANode
}

func TestYugabyteSmartDriverThreeNodeHA(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	cluster := startYugabyteHACluster(t)
	db := cluster.openSmartDriver(t)
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, `
CREATE TABLE frameworks_ha_probe (
    id BIGINT PRIMARY KEY,
    value BIGINT NOT NULL
)
`); err != nil {
		t.Fatalf("create HA probe table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO frameworks_ha_probe (id, value) VALUES (1, 0)"); err != nil {
		t.Fatalf("seed HA probe table: %v", err)
	}

	connections := holdYugabyteConnections(t, ctx, db, 9)
	seen := connectionAddresses(t, ctx, connections)
	for _, node := range cluster.nodes {
		if seen[node.address] == 0 {
			closeYugabyteConnections(connections)
			t.Fatalf("smart driver did not connect through %s (%s); distribution = %#v", node.name, node.address, seen)
		}
	}

	assertYugabyteServerRetry(t, ctx, db)
	assertYugabyteBackendSurvivesConcurrentDDL(t, ctx, db)

	victim := cluster.nodes[len(cluster.nodes)-1]
	var victimConnection *sql.Conn
	for _, connection := range connections {
		if yugabyteConnectionAddress(t, ctx, connection) == victim.address && victimConnection == nil {
			victimConnection = connection
			continue
		}
		_ = connection.Close()
	}
	if victimConnection == nil {
		t.Fatalf("no connection was pinned to failover victim %s", victim.name)
	}

	victimContext, cancelVictim := context.WithTimeout(ctx, 15*time.Second)
	defer cancelVictim()
	tx, err := victimConnection.BeginTx(victimContext, nil)
	if err != nil {
		t.Fatalf("begin victim transaction: %v", err)
	}
	if _, err := tx.ExecContext(victimContext, "INSERT INTO frameworks_ha_probe (id, value) VALUES (999, 1)"); err != nil {
		t.Fatalf("write uncommitted victim transaction: %v", err)
	}
	cluster.partitionNode(t, victim)
	commitErr := tx.Commit()
	cancelVictim()
	if commitErr == nil {
		t.Fatal("transaction pinned to partitioned tserver committed unexpectedly")
	}
	_ = victimConnection.Close()

	// The pool keeps the idle connections it holds to the partitioned tserver, as a service's pool does. Each request
	// that draws one fails its ping and discards it, so the pool converges on the survivors within a few requests.
	partitioned := time.Now()
	failedProbes := 0
	eventuallyYugabyte(t, 60*time.Second, func(probe context.Context) error {
		_, err := db.ExecContext(probe, "INSERT INTO frameworks_ha_probe (id, value) VALUES (2, 2) ON CONFLICT (id) DO NOTHING")
		if err != nil {
			failedProbes++
		}
		return err
	})
	eventuallyYugabyte(t, 60*time.Second, func(probe context.Context) error {
		survivors := holdYugabyteConnectionsE(probe, db, 6)
		if len(survivors) == 0 {
			failedProbes++
			return fmt.Errorf("could not hold 6 connections")
		}
		defer closeYugabyteConnections(survivors)
		for _, connection := range survivors {
			address, err := yugabyteConnectionAddressE(probe, connection)
			if err != nil {
				return err
			}
			if address == victim.address {
				return fmt.Errorf("a pooled connection still reaches partitioned tserver %s", victim.name)
			}
		}
		return nil
	})
	t.Logf("pool served only surviving tservers %s after the partition, %d failed probes", time.Since(partitioned).Round(time.Second), failedProbes)
	var uncommittedRows int
	eventuallyYugabyte(t, 30*time.Second, func(probe context.Context) error {
		return db.QueryRowContext(probe, "SELECT COUNT(*) FROM frameworks_ha_probe WHERE id = 999").Scan(&uncommittedRows)
	})
	if uncommittedRows != 0 {
		t.Fatalf("partitioned-node transaction leaked %d uncommitted rows", uncommittedRows)
	}

	cluster.healNode(t, victim)
	// The pool reuses its idle connections first; only the connections it opens beyond them can land on the rejoined
	// tserver.
	eventuallyYugabyte(t, 60*time.Second, func(probe context.Context) error {
		connections := holdYugabyteConnectionsE(probe, db, min(db.Stats().Idle+3, yugabyteHAMaxOpenConns))
		if len(connections) == 0 {
			return fmt.Errorf("no connections acquired")
		}
		defer closeYugabyteConnections(connections)
		for _, connection := range connections {
			address, err := yugabyteConnectionAddressE(probe, connection)
			if err == nil && address == victim.address {
				return nil
			}
		}
		return fmt.Errorf("rejoined tserver %s not selected yet", victim.name)
	})

	var durableRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM frameworks_ha_probe WHERE id IN (1, 2)").Scan(&durableRows); err != nil {
		t.Fatalf("read durable rows after recovery: %v", err)
	}
	if durableRows != 2 {
		t.Fatalf("durable rows after recovery = %d, want 2", durableRows)
	}
}

func assertYugabyteServerRetry(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
CREATE FUNCTION frameworks_raise_serialization_failure(should_fail boolean) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF should_fail THEN
        RAISE EXCEPTION 'forced Yugabyte serialization retry' USING ERRCODE = '40001';
    END IF;
END
$$
`); err != nil {
		t.Fatalf("create Yugabyte retry probe: %v", err)
	}
	var attempts atomic.Int32
	err := WithRetryablePostgresTxWithHook(ctx, db, nil, nil, func(tx *sql.Tx) error {
		attempt := attempts.Add(1)
		if _, err := tx.ExecContext(ctx, "SELECT frameworks_raise_serialization_failure($1)", attempt == 1); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE frameworks_ha_probe SET value = value + 1 WHERE id = 1")
		return err
	})
	if err != nil {
		t.Fatalf("retry serializable Yugabyte transaction: %v", err)
	}
	if attempts.Load() < 2 {
		t.Fatalf("serializable transaction attempts = %d, want a real Yugabyte retry", attempts.Load())
	}
	var value int64
	if err := db.QueryRowContext(ctx, "SELECT value FROM frameworks_ha_probe WHERE id = 1").Scan(&value); err != nil {
		t.Fatalf("read retried value: %v", err)
	}
	if value != 1 {
		t.Fatalf("retried value = %d, want 1", value)
	}
}

// assertYugabyteBackendSurvivesConcurrentDDL proves that a warm backend whose cached catalog a DDL on another tserver
// made stale reads the changed table again on the same session, through the ordinary statement retry: a schema or
// catalog version mismatch needs no fresh connection, which would load its whole catalog cold.
func assertYugabyteBackendSurvivesConcurrentDDL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	connections := holdYugabyteConnections(t, ctx, db, 6)
	defer closeYugabyteConnections(connections)
	reader, writer := connections[0], (*sql.Conn)(nil)
	readerAddress := yugabyteConnectionAddress(t, ctx, reader)
	for _, connection := range connections[1:] {
		if yugabyteConnectionAddress(t, ctx, connection) != readerAddress {
			writer = connection
			break
		}
	}
	if writer == nil {
		t.Fatal("no connection on a tserver other than the reader's")
	}
	var readerPID, value int64
	if err := reader.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&readerPID); err != nil {
		t.Fatalf("read reader backend: %v", err)
	}
	if err := reader.QueryRowContext(ctx, "SELECT value FROM frameworks_ha_probe WHERE id = 1").Scan(&value); err != nil {
		t.Fatalf("warm reader: %v", err)
	}
	if _, err := writer.ExecContext(ctx, "ALTER TABLE frameworks_ha_probe ADD COLUMN ddl_probe BIGINT NOT NULL DEFAULT 7"); err != nil {
		t.Fatalf("alter probe table from another tserver: %v", err)
	}
	attempts := 0
	var pid int64
	err := RetryPostgres(ctx, DefaultRetryAttempts, 25*time.Millisecond, func() error {
		attempts++
		return reader.QueryRowContext(ctx, "SELECT value, pg_backend_pid() FROM frameworks_ha_probe WHERE id = 1").Scan(&value, &pid)
	})
	if err != nil {
		t.Fatalf("warm backend did not read the altered table within the retry budget (%d attempts): %v", attempts, err)
	}
	if pid != readerPID {
		t.Fatalf("read on backend %d, want the reader's backend %d", pid, readerPID)
	}
	// The other tserver learns the new catalog version from its master heartbeat; until then its backends plan with
	// the old columns. The same backend sees the new column once it has.
	var probe int64
	changed := time.Now()
	eventuallyYugabyte(t, 15*time.Second, func(probeCtx context.Context) error {
		return reader.QueryRowContext(probeCtx, "SELECT ddl_probe, pg_backend_pid() FROM frameworks_ha_probe WHERE id = 1").Scan(&probe, &pid)
	})
	if probe != 7 || pid != readerPID {
		t.Fatalf("read ddl_probe=%d on backend %d, want 7 on the reader's backend %d", probe, pid, readerPID)
	}
	t.Logf("warm backend read the altered table after %d attempt(s) and its new column after %s", attempts, time.Since(changed).Round(time.Millisecond))
}

func startYugabyteHACluster(t *testing.T) *yugabyteHACluster {
	t.Helper()
	image := yugabyteHAImage(t)
	base := fmt.Sprintf("fw-yb-ha-%d", time.Now().UnixNano())
	cluster := &yugabyteHACluster{network: base}
	cluster.addressPrefix = createYugabyteHANetwork(t, cluster.network)
	t.Cleanup(func() {
		for _, node := range cluster.nodes {
			_, _ = dockerYugabyteHAOutput(30*time.Second, "rm", "-f", node.name)
		}
		_, _ = dockerYugabyteHAOutput(30*time.Second, "network", "rm", cluster.network)
	})

	for index := 1; index <= 3; index++ {
		name := fmt.Sprintf("%s-%d", base, index)
		address := fmt.Sprintf("%s.%d", cluster.addressPrefix, index+10)
		args := []string{
			"run", "-d", "--name", name, "--hostname", name,
			"--network", cluster.network, "--ip", address, "-P", image,
			"bin/yugabyted", "start", "--background=false", "--ui=false", "--advertise_address=" + address,
			"--tserver_flags=" + strings.TrimSuffix("yb_enable_read_committed_isolation=true,"+yugabyteHAExtraTServerFlags, ","),
			fmt.Sprintf("--cloud_location=frameworks.eu.z%d", index),
		}
		if index == 1 {
			args = append(args, "--fault_tolerance=zone")
		} else {
			args = append(args, "--join="+cluster.nodes[0].address)
		}
		dockerYugabyteHA(t, 3*time.Minute, args...)
		node := yugabyteHANode{name: name, address: address}
		cluster.nodes = append(cluster.nodes, node)
		waitYugabyteHANode(t, name, 3*time.Minute)
	}

	eventuallyYugabyte(t, 2*time.Minute, func(context.Context) error {
		out, err := dockerYugabyteHAOutput(20*time.Second, "exec", cluster.nodes[0].name,
			"bin/ysqlsh", "-h", cluster.nodes[0].name, "-U", "yugabyte", "-d", "yugabyte", "-tAc",
			"SELECT COUNT(*) FROM yb_servers()")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "3" {
			return fmt.Errorf("yb_servers count = %q", strings.TrimSpace(out))
		}
		return nil
	})

	for index := range cluster.nodes {
		port := yugabyteHAPublishedPort(t, cluster.nodes[index].name)
		address := strings.TrimSpace(dockerYugabyteHA(t, 20*time.Second, "inspect", "-f",
			"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", cluster.nodes[index].name))
		if address == "" {
			t.Fatalf("empty container address for %s", cluster.nodes[index].name)
		}
		cluster.nodes[index].hostPort = port
		cluster.nodes[index].address = address
	}
	return cluster
}

func createYugabyteHANetwork(t *testing.T, name string) string {
	t.Helper()
	seed := int(time.Now().UnixNano()%200) + 20
	for offset := range 200 {
		octet := 20 + (seed+offset)%200
		prefix := fmt.Sprintf("172.28.%d", octet)
		if _, err := dockerYugabyteHAOutput(30*time.Second, "network", "create", "--subnet", prefix+".0/24", name); err == nil {
			return prefix
		}
	}
	t.Fatal("could not allocate an isolated Docker subnet for Yugabyte HA")
	return ""
}

func (cluster *yugabyteHACluster) openSmartDriver(t *testing.T) *sql.DB {
	t.Helper()
	return cluster.openSmartDriverAt(t, "yugabyte")
}

func (cluster *yugabyteHACluster) openSmartDriverAt(t *testing.T, database string) *sql.DB {
	t.Helper()
	contactPoints := make([]string, 0, len(cluster.nodes))
	dialTargets := make(map[string]string, len(cluster.nodes))
	bridgeAddresses := make(map[string]struct{}, len(cluster.nodes))
	for _, node := range cluster.nodes {
		contactPoints = append(contactPoints, net.JoinHostPort("127.0.0.1", node.hostPort))
		dialTargets[node.name] = net.JoinHostPort("127.0.0.1", node.hostPort)
		dialTargets[node.address] = net.JoinHostPort("127.0.0.1", node.hostPort)
		bridgeAddresses[node.address] = struct{}{}
	}
	dsn := fmt.Sprintf(
		"postgres://yugabyte@%s/%s?sslmode=disable&load_balance=true&connect_timeout=3&yb_servers_refresh_interval=0&failed_host_reconnect_delay_secs=1",
		strings.Join(contactPoints, ","), database,
	)
	dsn, err := withPgxExecMode(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config, err := ybpgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse smart-driver DSN: %v", err)
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr == nil {
			if target := dialTargets[host]; target != "" {
				connection, dialErr := dialer.DialContext(ctx, network, target)
				if dialErr == nil {
					return connection, nil
				}
				// Docker Desktop cannot route host traffic directly to bridge addresses, so the harness normally translates
				// discovered tserver IPs through published localhost ports. Linux Docker may leave that proxy refused after
				// network disconnect/connect even though the container has rejoined at the same private IP. In that case the
				// bridge address is directly reachable and is the authoritative recovery path.
				if _, isBridgeAddress := bridgeAddresses[host]; isBridgeAddress {
					return dialer.DialContext(ctx, network, address)
				}
				return nil, dialErr
			}
		}
		return dialer.DialContext(ctx, network, address)
	}
	// The services' pool policy, with fewer connections.
	pool := DefaultConfig()
	pool.MaxOpenConns, pool.MaxIdleConns = yugabyteHAMaxOpenConns, yugabyteHAMaxOpenConns
	db := ybstdlib.OpenDB(*config, lifetimeOptions(pool.ConnMaxLifetime)...)
	configurePool(db, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("connect through Yugabyte smart driver: %v", err)
	}
	return db
}

func (cluster *yugabyteHACluster) partitionNode(t *testing.T, node yugabyteHANode) {
	t.Helper()
	dockerYugabyteHA(t, 30*time.Second, "pause", node.name)
}

func (cluster *yugabyteHACluster) healNode(t *testing.T, node yugabyteHANode) {
	t.Helper()
	dockerYugabyteHA(t, 30*time.Second, "unpause", node.name)
	waitYugabyteHANode(t, node.name, 3*time.Minute)
}

func holdYugabyteConnections(t *testing.T, ctx context.Context, db *sql.DB, count int) []*sql.Conn {
	t.Helper()
	connections := holdYugabyteConnectionsE(ctx, db, count)
	if len(connections) != count {
		closeYugabyteConnections(connections)
		t.Fatalf("acquired %d Yugabyte connections, want %d", len(connections), count)
	}
	return connections
}

func holdYugabyteConnectionsE(ctx context.Context, db *sql.DB, count int) []*sql.Conn {
	connections := make([]*sql.Conn, 0, count)
	for range count {
		connection, err := db.Conn(ctx)
		if err != nil {
			closeYugabyteConnections(connections)
			return nil
		}
		connections = append(connections, connection)
	}
	return connections
}

func connectionAddresses(t *testing.T, ctx context.Context, connections []*sql.Conn) map[string]int {
	t.Helper()
	addresses := make(map[string]int)
	for _, connection := range connections {
		addresses[yugabyteConnectionAddress(t, ctx, connection)]++
	}
	return addresses
}

func yugabyteConnectionAddress(t *testing.T, ctx context.Context, connection *sql.Conn) string {
	t.Helper()
	address, err := yugabyteConnectionAddressE(ctx, connection)
	if err != nil {
		t.Fatalf("read Yugabyte connection address: %v", err)
	}
	return address
}

func yugabyteConnectionAddressE(ctx context.Context, connection *sql.Conn) (string, error) {
	var address string
	err := connection.QueryRowContext(ctx, "SELECT host(inet_server_addr())").Scan(&address)
	return address, err
}

func closeYugabyteConnections(connections []*sql.Conn) {
	for _, connection := range connections {
		_ = connection.Close()
	}
}

func eventuallyYugabyte(t *testing.T, timeout time.Duration, probe func(context.Context) error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		last = probe(ctx)
		cancel()
		if last == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("Yugabyte condition did not converge within %s: %v", timeout, last)
}

func waitYugabyteHANode(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	eventuallyYugabyte(t, timeout, func(context.Context) error {
		out, err := dockerYugabyteHAOutput(15*time.Second, "exec", name,
			"bin/ysqlsh", "-h", name, "-U", "yugabyte", "-d", "yugabyte", "-tAc", "SELECT 1")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "1" {
			return fmt.Errorf("readiness output = %q", strings.TrimSpace(out))
		}
		return nil
	})
}

func yugabyteHAPublishedPort(t *testing.T, name string) string {
	t.Helper()
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatalf("discover published YSQL port for %s: %v", name, err)
	}
	return port
}

func yugabyteHAImage(t *testing.T) string {
	t.Helper()
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func dockerYugabyteHA(t *testing.T, timeout time.Duration, args ...string) string {
	t.Helper()
	out, err := dockerYugabyteHAOutput(timeout, args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func dockerYugabyteHAOutput(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}
