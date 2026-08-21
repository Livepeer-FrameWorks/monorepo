//go:build schema_verify

package tieraccess

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startTierAccessRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-tieraccess-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
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
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatalf("read Purser schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply Purser schema: %v", err)
	}
	return db
}

func TestTierAccessEligibilityQuery_RealPG(t *testing.T) {
	db := startTierAccessRealPG(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.cluster_pricing
			(id, cluster_id, pricing_model, required_tier_level, allow_free_tier)
		VALUES
			(gen_random_uuid(), 'official-free', 'free_unmetered', 0, true),
			(gen_random_uuid(), 'official-supporter', 'metered', 2, false),
			(gen_random_uuid(), 'official-enterprise', 'custom', 5, false),
			(gen_random_uuid(), 'not-official', 'metered', 1, false)
	`); err != nil {
		t.Fatalf("seed cluster pricing: %v", err)
	}

	qm := &fakeQM{official: []string{"official-free", "official-supporter", "official-enterprise"}}
	reconciler := &Reconciler{db: db, qm: qm, logger: logging.NewLogger()}
	eligible, primary, err := reconciler.Reconcile(ctx, "tenant-contract", 2, "")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if strings.Join(eligible, ",") != "official-supporter,official-free" || primary != "official-supporter" {
		t.Fatalf("eligible=%v primary=%q", eligible, primary)
	}
	wantCalls := "grant:official-supporter|grant:official-free|primary:official-supporter"
	if strings.Join(qm.calls, "|") != wantCalls {
		t.Fatalf("calls=%v, want %s", qm.calls, wantCalls)
	}
}
