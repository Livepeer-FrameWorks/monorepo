//go:build schema_verify

package database

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// TestPoolRetiresConnectionsAtTheirOwnLifetime_RealPG keeps one pooled connection busy through Connect and proves the
// pool replaces it at the connection's own jittered lifetime, before ConnMaxLifetime, without failing a query.
func TestPoolRetiresConnectionsAtTheirOwnLifetime_RealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-pool-lifetime-realpg-%d", time.Now().UnixNano())
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
	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable&TimeZone=Europe%%2FAmsterdam", port)

	const maxLifetime = 8 * time.Second
	// The largest jitter retires the connection just after three quarters of ConnMaxLifetime.
	connectionLifetimeJitter = func() float64 { return 0.999 }
	t.Cleanup(func() { connectionLifetimeJitter = rand.Float64 })
	var db PostgresConn
	deadline := time.Now().Add(2 * time.Minute)
	for {
		db, err = Connect(Config{URL: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: maxLifetime}, logging.NewLogger())
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("connect: %v", err)
		}
		time.Sleep(time.Second)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Each physical connection must use UTC even when the DSN asks for a local
	// timezone; timestamp/timestamptz conversions represent UTC ledger instants.
	db.SetMaxOpenConns(2)
	firstConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer firstConn.Close()
	secondConn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer secondConn.Close()
	for _, conn := range []*sql.Conn{firstConn, secondConn} {
		var zone string
		if err := conn.QueryRowContext(context.Background(), "SHOW TimeZone").Scan(&zone); err != nil {
			t.Fatal(err)
		}
		if zone != "UTC" {
			t.Fatalf("physical connection timezone=%s, want UTC", zone)
		}
	}
	if err := firstConn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secondConn.Close(); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	backend := func() int {
		t.Helper()
		var pid int
		if queryErr := db.QueryRowContext(context.Background(), "SELECT pg_backend_pid()").Scan(&pid); queryErr != nil {
			t.Fatalf("query on the pooled connection failed: %v", queryErr)
		}
		return pid
	}
	first := backend()
	opened := time.Now()
	for {
		time.Sleep(100 * time.Millisecond)
		if backend() != first {
			break
		}
		if time.Since(opened) > 2*maxLifetime {
			t.Fatalf("connection %d still served after %s", first, time.Since(opened))
		}
	}
	retiredAfter := time.Since(opened)
	want := maxLifetime * 3 / 4
	if retiredAfter < want-time.Second || retiredAfter >= maxLifetime-500*time.Millisecond {
		t.Fatalf("connection retired after %s, want about %s, before ConnMaxLifetime %s", retiredAfter, want, maxLifetime)
	}
}
