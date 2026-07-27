//go:build schema_verify

package provisioner

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// TestArtifactEventsDedupedPreservesLegacyRows proves, against a real ClickHouse
// engine, the correctness invariant of the artifact_events_deduped view: legacy
// rows (those with an empty event_id) have no trustworthy dedup identity and MUST pass through
// verbatim, while only non-empty event_id rows collapse to one per event_id.
//
// The old LIMIT-1-BY-hash form collapsed two DISTINCT legacy events that shared
// tenant/stream/second/request_id/stage/content_type but differed elsewhere
// (percent/message/file_path/…), silently losing history. This asserts they are
// both kept: count() == 3, not 2.
func TestArtifactEventsDedupedPreservesLegacyRows(t *testing.T) {
	requireDocker(t)

	baselineSQL, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read baseline periscope.sql: %v", err)
	}

	const name = "fw-sv-ch-dedup"
	chStart(t, name)
	defer rmContainer(t, name)
	chApply(t, name, string(baselineSQL))

	cols := "(timestamp, tenant_id, stream_id, internal_name, request_id, stage, content_type, percent, event_id)"

	// chApply runs without a --database flag, so fully-qualify the table. Use now()
	// for the timestamp so the rows stay inside the table's 90-day TTL window (a
	// fixed past literal would be TTL-expired and vanish). now() is constant within
	// one query, so both legacy rows below share the same second — exactly the case
	// the old hash-dedup collapsed.
	//
	// Two DISTINCT legacy rows: identical tenant/stream/second/request_id/stage/
	// content_type, differ ONLY in percent, both event_id=''. Under the old view
	// these collapsed to one; they must now both survive.
	legacy := "INSERT INTO periscope.artifact_events " + cols + " VALUES " +
		"(now(), '11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 'legacy-artifact', 'req-legacy', 'processing', 'clip', 10, ''), " +
		"(now(), '11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 'legacy-artifact', 'req-legacy', 'processing', 'clip', 50, '');"
	chApply(t, name, legacy)

	// One modern event with a stable non-empty event_id, delivered twice (an
	// at-least-once redelivery). These MUST collapse to one row per event_id.
	modern := "INSERT INTO periscope.artifact_events " + cols + " VALUES " +
		"(now(), '11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 'modern-artifact', 'req-modern', 'done', 'clip', 100, 'evt-modern-1');"
	chApply(t, name, modern)
	chApply(t, name, modern)

	out, err := docker(t, "", "exec", name, "clickhouse-client", "--database", "periscope", "-q",
		"SELECT count() FROM artifact_events_deduped")
	if err != nil {
		t.Fatalf("count artifact_events_deduped: %v", err)
	}
	got := strings.TrimSpace(out)
	if got != "3" {
		// Sanity-check the base table too so a failure distinguishes
		// "view collapsed legacy rows" from "insert never landed".
		base, _ := docker(t, "", "exec", name, "clickhouse-client", "--database", "periscope", "-q",
			"SELECT count() FROM artifact_events")
		t.Fatalf("artifact_events_deduped count = %q, want 3 (2 legacy + 1 modern); base artifact_events count = %q",
			got, strings.TrimSpace(base))
	}
}
