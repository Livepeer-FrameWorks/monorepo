//go:build schema_verify

package datamigrations

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestNodeIdentityKeyGateUsesRemediationOwnershipRealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-node-identity-gate-realpg-%d", time.Now().UnixNano())
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
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters
			(cluster_id, cluster_name, cluster_type, base_url, is_active)
		VALUES
			('active-cell', 'Active cell', 'edge', 'https://active.invalid', true),
			('inactive-cell', 'Inactive cell', 'edge', 'https://inactive.invalid', false);

		INSERT INTO quartermaster.infrastructure_nodes
			(node_id, cluster_id, node_name, node_type, status, enrollment_origin)
		VALUES
			('managed-gitops', 'active-cell', 'Managed GitOps', 'edge', 'active', 'gitops_seed'),
			('managed-adopted', 'active-cell', 'Managed adopted', 'edge', 'active', 'adopted_local'),
			('self-hosted', 'active-cell', 'Self hosted', 'edge', 'active', 'runtime_enrolled'),
			('managed-keyed', 'active-cell', 'Managed keyed', 'edge', 'active', 'gitops_seed'),
			('managed-offline', 'active-cell', 'Managed offline', 'edge', 'offline', 'gitops_seed'),
			('managed-inactive-cell', 'inactive-cell', 'Managed inactive cell', 'edge', 'active', 'gitops_seed');

		INSERT INTO quartermaster.node_fingerprints
			(node_id, node_identity_public_key_ed25519)
		VALUES
			('managed-gitops', NULL),
			('managed-adopted', NULL),
			('self-hosted', NULL),
			('managed-keyed', decode(repeat('ab', 32), 'hex')),
			('managed-offline', NULL),
			('managed-inactive-cell', NULL)
	`); err != nil {
		t.Fatal(err)
	}

	count, err := countManagedActiveKeylessNodes(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("managed active keyless count = %d, want 2", count)
	}
	ids, err := listManagedActiveKeylessNodeIDs(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids, ","); got != "managed-adopted,managed-gitops" {
		t.Fatalf("blocked nodes = %q, want managed-adopted,managed-gitops", got)
	}
	if err := verifyNodeIdentityKeys(ctx, db); err == nil || strings.Contains(err.Error(), "self-hosted") {
		t.Fatalf("verification error = %v, want only operator-managed blockers", err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE quartermaster.node_fingerprints
		SET node_identity_public_key_ed25519 = decode(repeat('cd', 32), 'hex')
		WHERE node_id IN ('managed-gitops', 'managed-adopted')
	`); err != nil {
		t.Fatal(err)
	}
	if err := verifyNodeIdentityKeys(ctx, db); err != nil {
		t.Fatalf("self-hosted keyless node blocked platform release: %v", err)
	}
}
