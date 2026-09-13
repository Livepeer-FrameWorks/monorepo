//go:build schema_verify

package control

import "testing"

func TestDVRRecordingSource_RealPG(t *testing.T) {
	conn := startRealPG(t)
	const tenant = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, status)
		VALUES ('recording-hash', 'dvr', $1, 'recording', 'recording')`, tenant)
	exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, is_orphaned) VALUES ('recording-hash', 'origin', false)`)
	check := func(name, owner, internal, node string, want bool) {
		t.Helper()
		if got := DVRRecordingSource(t.Context(), conn, owner, internal, node); got != want {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
	}
	check("active owner", tenant, "recording", "origin", true)
	check("foreign tenant", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "recording", "origin", false)
	check("other artifact", tenant, "another", "origin", false)
	check("other node", tenant, "recording", "replica", false)
	exec(`UPDATE foghorn.artifact_nodes SET is_orphaned = true WHERE artifact_hash = 'recording-hash'`)
	check("orphaned owner", tenant, "recording", "origin", false)
	exec(`UPDATE foghorn.artifact_nodes SET is_orphaned = false WHERE artifact_hash = 'recording-hash'`)
	exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, is_orphaned) VALUES ('recording-hash', 'replica', false)`)
	check("ambiguous owner", tenant, "recording", "origin", false)
	exec(`UPDATE foghorn.artifact_nodes SET is_orphaned = true WHERE artifact_hash = 'recording-hash' AND node_id = 'replica'`)
	check("only active owner", tenant, "recording", "origin", true)
	for _, status := range []string{"requested", "starting", "finalizing", "completed", "failed"} {
		exec(`UPDATE foghorn.artifacts SET status = $1 WHERE tenant_id = $2 AND artifact_hash = 'recording-hash'`, status, tenant)
		check(status, tenant, "recording", "origin", false)
	}
}
