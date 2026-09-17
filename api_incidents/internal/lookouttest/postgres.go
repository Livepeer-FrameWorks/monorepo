//go:build schema_verify

// Package lookouttest opens disposable PostgreSQL and YugabyteDB databases with
// the Lookout baseline for schema_verify tests.
package lookouttest

import (
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"

	_ "github.com/lib/pq"
)

// StartPostgres runs the pinned PostgreSQL image, applies lookout.sql, and
// removes the container when the test ends.
func StartPostgres(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-lookout-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	applyBaseline(t, db)
	return db
}

// StartYugabyte opens a fresh database on the suite-owned Yugabyte fixture when
// the contract harness provides one, and otherwise runs the pinned Yugabyte
// image for this test. The Lookout baseline is applied either way.
func StartYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "lookout"); ok {
		applyBaseline(t, db)
		return db
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-lookout-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=false`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	applyBaseline(t, db)
	return db
}

func applyBaseline(t *testing.T, db *sql.DB) {
	t.Helper()
	schema, err := dbsql.Content.ReadFile("schema/lookout.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply lookout baseline: %v", err)
	}
}
