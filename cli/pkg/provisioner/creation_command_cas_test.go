//go:build schema_verify

package provisioner

import (
	"strings"
	"testing"
)

// TestCreationCommandCASMutualExclusion validates, on a real Postgres engine, that the creation-command
// CAS transitions are mutually exclusive: once a row leaves `accepted`, the other transition's
// `WHERE status='accepted'` guard matches zero rows and cannot overwrite the terminal state — asserted
// in both orders (commit-then-reject, reject-then-commit) plus the CHECK constraint. This runs against
// real Postgres rather than sqlmock's canned row counts, but exercises the guards SEQUENTIALLY: it does
// NOT run overlapping transactions and does not by itself demonstrate concurrent row-lock/EvalPlanQual
// contention (which is Postgres's own guarantee for a single-row UPDATE's WHERE, not asserted here).
func TestCreationCommandCASMutualExclusion(t *testing.T) {
	requireDocker(t)

	const name = "fw-creation-cas"
	pgStart(t, name)
	defer rmContainer(t, name)
	pgCreateDB(t, name, "cas")

	// Minimal schema: the command table with its real CHECK constraints (no artifacts table, so the
	// expiry guard's NOT EXISTS is always satisfied for a stranded accept).
	pgApply(t, name, "cas", `
CREATE SCHEMA foghorn;
CREATE TABLE foghorn.artifact_creation_commands (
    request_id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(32) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'accepted',
    catalog_revision BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT cmd_kind_check CHECK (kind IN ('clip','dvr','vod')),
    CONSTRAINT cmd_status_check CHECK (status IN ('accepted','committed','rejected'))
);`)

	const tid = "11111111-1111-1111-1111-111111111111"
	commitCAS := func(reqID string) string {
		return `UPDATE foghorn.artifact_creation_commands SET status='committed', catalog_revision=5, updated_at=NOW() WHERE request_id='` + reqID + `'::uuid AND tenant_id='` + tid + `'::uuid AND status='accepted';`
	}
	rejectCAS := func(reqID string) string {
		return `UPDATE foghorn.artifact_creation_commands c SET status='rejected', updated_at=NOW() WHERE c.request_id='` + reqID + `'::uuid AND c.status='accepted' AND NOT EXISTS (SELECT 1 FROM foghorn.artifact_creation_commands x WHERE FALSE);`
	}
	// runTag executes a single UPDATE and returns psql's command tag line ("UPDATE 1" / "UPDATE 0").
	runTag := func(sql string) string {
		out, err := docker(t, sql, "exec", "-i", name, "psql", "-U", "postgres", "-d", "cas", "-v", "ON_ERROR_STOP=1")
		if err != nil {
			t.Fatalf("psql: %v\n%s", err, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "UPDATE ") {
				return strings.TrimSpace(line)
			}
		}
		t.Fatalf("no UPDATE tag in output: %s", out)
		return ""
	}
	statusOf := func(reqID string) string {
		out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-d", "cas", "-tAc",
			"SELECT status FROM foghorn.artifact_creation_commands WHERE request_id='"+reqID+"'::uuid")
		if err != nil {
			t.Fatalf("select status: %v", err)
		}
		return strings.TrimSpace(out)
	}
	seed := func(reqID string) {
		pgApply(t, name, "cas", `INSERT INTO foghorn.artifact_creation_commands (request_id, tenant_id, kind, artifact_hash) VALUES ('`+reqID+`'::uuid, '`+tid+`'::uuid, 'clip', '`+strings.ReplaceAll(reqID, "-", "")[:32]+`');`)
	}

	// Order A: commit wins, later expiry-reject matches zero rows and leaves committed.
	const a = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	seed(a)
	if tag := runTag(commitCAS(a)); tag != "UPDATE 1" {
		t.Fatalf("commit CAS on accepted: got %q, want UPDATE 1", tag)
	}
	if tag := runTag(rejectCAS(a)); tag != "UPDATE 0" {
		t.Fatalf("reject CAS after commit must match 0 rows: got %q", tag)
	}
	if s := statusOf(a); s != "committed" {
		t.Fatalf("final status after commit-then-reject: got %q, want committed", s)
	}

	// Order B: expiry wins, later commit matches zero rows and leaves rejected (no artifact behind it).
	const b = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	seed(b)
	if tag := runTag(rejectCAS(b)); tag != "UPDATE 1" {
		t.Fatalf("reject CAS on accepted: got %q, want UPDATE 1", tag)
	}
	if tag := runTag(commitCAS(b)); tag != "UPDATE 0" {
		t.Fatalf("commit CAS after reject must match 0 rows: got %q", tag)
	}
	if s := statusOf(b); s != "rejected" {
		t.Fatalf("final status after reject-then-commit: got %q, want rejected", s)
	}

	// The CHECK constraint rejects an illegal status.
	if out, err := docker(t, `UPDATE foghorn.artifact_creation_commands SET status='bogus' WHERE request_id='`+a+`'::uuid;`,
		"exec", "-i", name, "psql", "-U", "postgres", "-d", "cas", "-v", "ON_ERROR_STOP=1"); err == nil {
		t.Fatalf("CHECK constraint should reject status='bogus', but UPDATE succeeded: %s", out)
	}
}
