//go:build schema_verify

package database

import (
	"context"
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
	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)

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
