//go:build schema_verify

package provisioner

import (
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestCreationCommandAckLeaseClaim validates, on a real Postgres engine, the durable-lease
// property the ack-drain claim depends on: the claim stamps command_ack_leased_until (a column
// that OUTLIVES the transient FOR UPDATE SKIP LOCKED row lock) and the due-query EXCLUDES a
// still-leased row, so a second replica's claim — issued AFTER the first claim's transaction has
// already committed and released its row locks — selects a DISJOINT set of rows and cannot
// re-drive an ack RPC that is still in flight behind the lease. It also asserts the claim touches
// NEITHER command_ack_next_at NOR command_ack_attempts (the lease is a separate axis from the
// retry schedule), that an expired lease becomes reclaimable, and that the backoff UPDATE is the
// only thing that pushes command_ack_next_at while releasing the lease.
//
// The two claims run SEQUENTIALLY via docker-exec psql (the harness cannot open overlapping
// Go-driver transactions), which is exactly the case a bare SKIP LOCKED gets wrong: the first
// claim's row lock is already gone by the time the second runs, so ONLY the durable lease
// column keeps the second claim off the in-flight rows.
func TestCreationCommandAckLeaseClaim(t *testing.T) {
	requireDocker(t)

	const name = "fw-ack-lease"
	pgStart(t, name)
	defer rmContainer(t, name)
	pgCreateDB(t, name, "ack")

	// Minimal slice of commodore.artifact_creation_intents: only the columns the claim, backoff,
	// and clear statements read or write. Same column names/types as the baseline so the exact
	// production SQL runs unmodified.
	pgApply(t, name, "ack", `
CREATE SCHEMA commodore;
CREATE TABLE commodore.artifact_creation_intents (
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(64) NOT NULL,
    request_id UUID NOT NULL,
    origin_cluster_id TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'committed',
    command_ack_pending BOOLEAN NOT NULL DEFAULT FALSE,
    command_acked_at TIMESTAMP,
    command_ack_attempts INT NOT NULL DEFAULT 0,
    command_ack_next_at TIMESTAMP,
    command_ack_leased_until TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, kind, artifact_hash)
);`)

	const tid = "11111111-1111-1111-1111-111111111111"

	// The EXACT ack-drain claim from drainCreationCommandAcks (api_control), with $1 LIMIT and
	// $2 lease interval inlined. RETURNING only request_id so -tA yields bare UUID lines.
	claim := func(limit, leaseSeconds int) []string {
		sql := `
WITH due AS (
    SELECT ctid
      FROM commodore.artifact_creation_intents
     WHERE command_ack_pending
       AND (command_ack_next_at IS NULL OR command_ack_next_at <= NOW())
       AND (command_ack_leased_until IS NULL OR command_ack_leased_until <= NOW())
     ORDER BY command_ack_next_at NULLS FIRST
     LIMIT ` + strconv.Itoa(limit) + `
     FOR UPDATE SKIP LOCKED
)
UPDATE commodore.artifact_creation_intents i
   SET command_ack_leased_until = NOW() + INTERVAL '` + strconv.Itoa(leaseSeconds) + ` seconds',
       updated_at = NOW()
  FROM due
 WHERE i.ctid = due.ctid
 RETURNING i.request_id::text;`
		out, err := docker(t, sql, "exec", "-i", name, "psql", "-U", "postgres", "-d", "ack", "-tA", "-v", "ON_ERROR_STOP=1")
		if err != nil {
			t.Fatalf("claim: %v\n%s", err, out)
		}
		var ids []string
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.Count(line, "-") == 4 { // a UUID row, not the UPDATE tag
				ids = append(ids, line)
			}
		}
		sort.Strings(ids)
		return ids
	}

	scalar := func(sql string) string {
		out, err := docker(t, "", "exec", name, "psql", "-U", "postgres", "-d", "ack", "-tAc", sql)
		if err != nil {
			t.Fatalf("scalar %q: %v", sql, err)
		}
		return strings.TrimSpace(out)
	}

	// Six terminal intents all DUE (next_at NULL) and UNLEASED.
	for i := 0; i < 6; i++ {
		rid := "0000000" + strconv.Itoa(i) + "-0000-0000-0000-000000000000"
		pgApply(t, name, "ack", `INSERT INTO commodore.artifact_creation_intents
			(tenant_id, kind, artifact_hash, request_id, origin_cluster_id, command_ack_pending)
			VALUES ('`+tid+`'::uuid, 'clip', 'hash`+strconv.Itoa(i)+`', '`+rid+`'::uuid, 'cluster-a', TRUE);`)
	}

	// Replica 1 claims 3. Its transaction commits (docker-exec autocommit) → its row locks are
	// released before replica 2 runs.
	set1 := claim(3, 120)
	if len(set1) != 3 {
		t.Fatalf("claim 1 should lease 3 rows, got %d: %v", len(set1), set1)
	}

	// Replica 2 claims 3 AFTER claim 1 committed. A bare SKIP LOCKED would now happily re-return
	// claim 1's rows (their locks are gone). The durable lease must exclude them → disjoint set.
	set2 := claim(3, 120)
	if len(set2) != 3 {
		t.Fatalf("claim 2 should lease the remaining 3 rows, got %d: %v", len(set2), set2)
	}
	for _, id := range set2 {
		for _, prev := range set1 {
			if id == prev {
				t.Fatalf("claim 2 reselected a still-leased row %s (lease did not exclude it): set1=%v set2=%v", id, set1, set2)
			}
		}
	}

	// All six are now leased; a third claim finds nothing due.
	if got := claim(6, 120); len(got) != 0 {
		t.Fatalf("claim 3 should find no unleased due rows, got %v", got)
	}

	// The claim is a LEASE only: it must not have advanced the retry schedule for anyone.
	if n := scalar(`SELECT count(*) FROM commodore.artifact_creation_intents WHERE command_ack_next_at IS NOT NULL`); n != "0" {
		t.Fatalf("claim must not touch command_ack_next_at, but %s rows have it set", n)
	}
	if n := scalar(`SELECT count(*) FROM commodore.artifact_creation_intents WHERE command_ack_attempts <> 0`); n != "0" {
		t.Fatalf("claim must not touch command_ack_attempts, but %s rows are non-zero", n)
	}
	if n := scalar(`SELECT count(*) FROM commodore.artifact_creation_intents WHERE command_ack_leased_until > NOW()`); n != "6" {
		t.Fatalf("all 6 rows should be leased into the future, got %s", n)
	}

	// Expire replica 1's leases (as a crashed worker's would); those rows become reclaimable.
	pgApply(t, name, "ack", `UPDATE commodore.artifact_creation_intents
		SET command_ack_leased_until = NOW() - INTERVAL '1 second'
		WHERE request_id IN ('`+strings.Join(set1, "'::uuid,'")+`'::uuid);`)
	reclaimed := claim(6, 120)
	if len(reclaimed) != 3 {
		t.Fatalf("only the 3 lease-expired rows should be reclaimable, got %d: %v", len(reclaimed), reclaimed)
	}
	sort.Strings(set1)
	sort.Strings(reclaimed)
	for i := range set1 {
		if set1[i] != reclaimed[i] {
			t.Fatalf("reclaimed set must equal the lease-expired set: want %v got %v", set1, reclaimed)
		}
	}

	// The backoff UPDATE is the ONLY path that pushes next_at; it also releases the lease.
	first := set1[0]
	pgApply(t, name, "ack", `UPDATE commodore.artifact_creation_intents
		SET command_ack_attempts = command_ack_attempts + 1,
		    command_ack_next_at = NOW() + LEAST(INTERVAL '30 seconds' * power(2, LEAST(command_ack_attempts, 20)), INTERVAL '15 minutes'),
		    command_ack_leased_until = NULL,
		    updated_at = NOW()
		WHERE request_id = '`+first+`'::uuid AND command_ack_pending = TRUE;`)
	if n := scalar(`SELECT command_ack_attempts FROM commodore.artifact_creation_intents WHERE request_id='` + first + `'::uuid`); n != "1" {
		t.Fatalf("backoff should increment attempts to 1, got %s", n)
	}
	// Clear EVERY lease (the reclaim above re-leased its rows). The backed-off row still must not
	// be claimable: its command_ack_next_at is ~30s in the future, so the retry schedule — not the
	// lease — now holds it back. All 5 others become due again.
	pgApply(t, name, "ack", `UPDATE commodore.artifact_creation_intents SET command_ack_leased_until = NOW() - INTERVAL '1 second';`)
	due := claim(6, 120)
	if len(due) != 5 {
		t.Fatalf("backed-off row must stay held by its retry schedule; expected 5 due, got %d: %v", len(due), due)
	}
	for _, id := range due {
		if id == first {
			t.Fatalf("backed-off row %s became due before its next_at elapsed", first)
		}
	}
}

// TestCreationCommandAckLeaseTokenFencesStaleSettlement proves the OWNERSHIP FENCE: after a
// worker's lease expires and another replica reclaims the row (stamping a NEW token), the first
// worker's stale settlement — CAS'd on its OWN token — matches ZERO rows, so it can neither clear
// nor back off the row the new owner holds. Timestamp expiry alone cannot provide this fence (the
// stale worker's UPDATE keyed only on artifact identity would still land); the lease token can.
func TestCreationCommandAckLeaseTokenFencesStaleSettlement(t *testing.T) {
	requireDocker(t)

	const name = "fw-ack-token"
	pgStart(t, name)
	defer rmContainer(t, name)
	pgCreateDB(t, name, "ack")

	pgApply(t, name, "ack", `
CREATE SCHEMA commodore;
CREATE TABLE commodore.artifact_creation_intents (
    tenant_id UUID NOT NULL,
    kind VARCHAR(16) NOT NULL,
    artifact_hash VARCHAR(64) NOT NULL,
    request_id UUID NOT NULL,
    command_ack_pending BOOLEAN NOT NULL DEFAULT FALSE,
    command_ack_attempts INT NOT NULL DEFAULT 0,
    command_ack_next_at TIMESTAMP,
    command_ack_leased_until TIMESTAMP,
    command_ack_lease_token UUID,
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, kind, artifact_hash)
);`)

	const tid = "11111111-1111-1111-1111-111111111111"
	const tokA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	const tokB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"

	pgApply(t, name, "ack", `INSERT INTO commodore.artifact_creation_intents
		(tenant_id, kind, artifact_hash, request_id, command_ack_pending)
		VALUES ('`+tid+`'::uuid, 'clip', 'h0', '00000000-0000-0000-0000-000000000000'::uuid, TRUE);`)

	// The exact ack-drain claim (lease + token), parameterized by token, LIMIT 1.
	claim := func(token string) {
		pgApply(t, name, "ack", `
WITH due AS (
    SELECT ctid FROM commodore.artifact_creation_intents
     WHERE command_ack_pending
       AND (command_ack_next_at IS NULL OR command_ack_next_at <= NOW())
       AND (command_ack_leased_until IS NULL OR command_ack_leased_until <= NOW())
     ORDER BY command_ack_next_at NULLS FIRST LIMIT 1 FOR UPDATE SKIP LOCKED
)
UPDATE commodore.artifact_creation_intents i
   SET command_ack_leased_until = NOW() + INTERVAL '120 seconds',
       command_ack_lease_token = '`+token+`'::uuid, updated_at = NOW()
  FROM due WHERE i.ctid = due.ctid;`)
	}

	// The exact backoff settlement, CAS-fenced on a token; returns psql's UPDATE tag.
	backoffTag := func(token string) string {
		out, err := docker(t, `UPDATE commodore.artifact_creation_intents
		   SET command_ack_attempts = command_ack_attempts + 1, command_ack_leased_until = NULL,
		       command_ack_lease_token = NULL, updated_at = NOW()
		 WHERE tenant_id = '`+tid+`'::uuid AND kind = 'clip' AND artifact_hash = 'h0'
		   AND command_ack_pending = TRUE AND command_ack_lease_token = '`+token+`'::uuid;`,
			"exec", "-i", name, "psql", "-U", "postgres", "-d", "ack", "-v", "ON_ERROR_STOP=1")
		if err != nil {
			t.Fatalf("backoff: %v\n%s", err, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "UPDATE ") {
				return strings.TrimSpace(line)
			}
		}
		t.Fatalf("no UPDATE tag in output: %s", out)
		return ""
	}

	claim(tokA)
	// A's lease expires; replica B reclaims and stamps its own token.
	pgApply(t, name, "ack", `UPDATE commodore.artifact_creation_intents SET command_ack_leased_until = NOW() - INTERVAL '1 second';`)
	claim(tokB)

	// A's stale backoff, CAS'd on token A, must affect ZERO rows.
	if tag := backoffTag(tokA); tag != "UPDATE 0" {
		t.Fatalf("stale settlement must be fenced by the lease token: want UPDATE 0, got %q", tag)
	}
	// The current owner B's settlement, CAS'd on token B, affects exactly one row.
	if tag := backoffTag(tokB); tag != "UPDATE 1" {
		t.Fatalf("current-owner settlement: want UPDATE 1, got %q", tag)
	}
}
