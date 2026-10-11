//go:build schema_verify

package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

// TestMediaAuthorityDoctorLegacySchemaTenantsRealPG runs the doctor's real
// script and SQL against the current Commodore baseline. Only a live tenant
// still on schema 1 in a cell that attests enforcement makes the check
// unhealthy; the same tenant in a cell that does not enforce, a schema-2 tenant,
// and a tombstone do not count.
func TestMediaAuthorityDoctorLegacySchemaTenantsRealPG(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-cli-media-authority-doctor-realpg-%d", time.Now().UnixNano())
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
	admin, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE commodore`); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/commodore?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	wrapper := "#!/bin/sh\nexec " + ssh.ShellQuote(docker) + " exec -i " + ssh.ShellQuote(name) + " psql \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	mustExec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	// A live schema-1 tenant in an enforcing cell is the only stranded row; the
	// same tenant also targets a cell that does not enforce.
	mustExec(`
		INSERT INTO commodore.media_authority_versions
			(authority_kind, authority_id, authority_version, payload_schema_version, payload, payload_sha256, tombstone, issued_at, refresh_after, valid_until)
		VALUES
			('tenant', 'legacy', 1, 1, '\x', decode(repeat('00', 32), 'hex'), FALSE, NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 day'),
			('tenant', 'placed', 1, 2, '\x', decode(repeat('00', 32), 'hex'), FALSE, NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 day'),
			('tenant', 'deleted', 1, 1, '\x', decode(repeat('00', 32), 'hex'), TRUE, NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 day');
		INSERT INTO commodore.media_authority_current (authority_kind, authority_id, authority_version)
		VALUES ('tenant', 'legacy', 1), ('tenant', 'placed', 1), ('tenant', 'deleted', 1);
		INSERT INTO commodore.media_authority_targets (authority_kind, authority_id, cell_id, highest_targeted_version)
		VALUES ('tenant', 'legacy', 'cell-ready', 1), ('tenant', 'legacy', 'cell-legacy', 1),
		       ('tenant', 'placed', 'cell-ready', 1), ('tenant', 'deleted', 'cell-ready', 1);
		INSERT INTO commodore.media_cell_placement_capabilities (cell_id, max_schema_version, enforcement_ready, live_replicas, first_ready_at)
		VALUES ('cell-ready', 2, TRUE, 2, NOW()), ('cell-legacy', 1, FALSE, 1, NULL)
	`)

	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {Name: "db-1", ExternalIP: "127.0.0.1", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled:   true,
				Host:      "db-1",
				Port:      5432,
				Password:  "harness",
				Databases: []inventory.DatabaseConfig{{Name: "commodore"}},
			},
		},
	}
	pool := ssh.NewPool(10*time.Second, "")
	t.Cleanup(func() { pool.Close() })

	result := doctorMediaAuthorityConvergence(ctx, &resolvedCluster{Manifest: manifest}, pool, "")
	want := "1 tenant authority(ies) still on schema 1 in cells that enforce placement (cell-ready: 1)"
	if result.Status != "unhealthy" || !strings.Contains(result.Error, want) {
		t.Fatalf("doctor status=%s error=%q, want unhealthy naming %q", result.Status, result.Error, want)
	}
	if strings.Contains(result.Error, "could not be checked") || strings.Contains(result.Error, "cell-legacy") {
		t.Fatalf("doctor misread the probe: %q", result.Error)
	}

	mustExec(`
		INSERT INTO commodore.media_authority_versions
			(authority_kind, authority_id, authority_version, payload_schema_version, payload, payload_sha256, issued_at, refresh_after, valid_until)
		VALUES ('tenant', 'legacy', 2, 2, '\x', decode(repeat('00', 32), 'hex'), NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '1 day');
		UPDATE commodore.media_authority_current SET authority_version = 2 WHERE authority_kind = 'tenant' AND authority_id = 'legacy'
	`)
	result = doctorMediaAuthorityConvergence(ctx, &resolvedCluster{Manifest: manifest}, pool, "")
	if strings.Contains(result.Error, "still on schema 1") || strings.Contains(result.Error, "could not be checked") {
		t.Fatalf("activated tenant still reported: status=%s error=%q", result.Status, result.Error)
	}
}
