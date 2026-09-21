//go:build schema_verify

package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startMediaAuthorityRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-foghorn-authority-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if out, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err = dockerpg.WaitReady(conn, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/foghorn.sql")
	if err != nil {
		t.Fatalf("read foghorn schema: %v", err)
	}
	if _, err = conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply foghorn schema: %v", err)
	}
	return conn
}

// Every rejection Store.Apply can return must commit its audit row against the
// real outcome constraint. A rejected outcome the constraint does not allow
// rolls the apply back and surfaces as a retryable persistence error, so the
// sender redelivers a rejection forever.
func TestMediaAuthorityApplyRejectionsCommitAudit_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	store, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	store.now = func() time.Time { return storeFixtureNow.Add(time.Minute) }
	ctx := context.Background()

	active := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE
	tombstone := mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	steps := []struct {
		name      string
		lifecycle mediaauthoritypb.AuthorityLifecycle
		version   uint64
		wantErr   error
		outcome   string
	}{
		{name: "first active version applies", lifecycle: active, version: 2, outcome: "applied"},
		{name: "older version is stale", lifecycle: active, version: 1, wantErr: ErrRollback, outcome: "stale_version_rejected"},
		{name: "same version is a duplicate", lifecycle: active, version: 2, outcome: "duplicate"},
		{name: "tombstone applies", lifecycle: tombstone, version: 3, outcome: "applied"},
		{name: "resurrection is terminal", lifecycle: active, version: 4, wantErr: ErrTombstoneTerminal, outcome: "terminal_lifecycle_rejected"},
	}
	for _, step := range steps {
		encoded, _ := signedArtifactFixture(t, step.lifecycle, step.version)
		_, applyErr := store.Apply(ctx, encoded)
		if !errors.Is(applyErr, step.wantErr) {
			t.Fatalf("%s: Apply error = %v, want %v", step.name, applyErr, step.wantErr)
		}
		var outcome string
		if err := conn.QueryRowContext(ctx, `
			SELECT outcome FROM foghorn.media_authority_apply_audit
			WHERE authority_version = $1 ORDER BY id DESC LIMIT 1`, int64(step.version)).Scan(&outcome); err != nil {
			t.Fatalf("%s: read audit row: %v", step.name, err)
		}
		if outcome != step.outcome {
			t.Fatalf("%s: audit outcome = %q, want %q", step.name, outcome, step.outcome)
		}
	}

	var held int64
	if err := conn.QueryRowContext(ctx, `SELECT authority_version FROM foghorn.media_authorities WHERE authority_kind = 'media_object'`).Scan(&held); err != nil {
		t.Fatalf("read held authority: %v", err)
	}
	if held != 3 {
		t.Fatalf("held authority version = %d, want the tombstone at 3", held)
	}
}
