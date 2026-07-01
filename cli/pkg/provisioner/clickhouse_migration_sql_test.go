package provisioner

import (
	"strings"
	"testing"
)

func testRemoteSource() RemoteSource {
	return RemoteSource{Host: "yuga-eu-1.internal", DB: "periscope", User: "frameworks", Pass: "secret"}
}

func TestSyncPartitionSQL_idempotentReplaceByID(t *testing.T) {
	// Tuple-partitioned table: partition_id "202606-7f3a..." is the stable key —
	// filter via _partition_id and REPLACE PARTITION ID (works for ANY shape).
	got := SyncPartitionSQL(testRemoteSource(), "periscope", "stream_event_log", "202606-deadbeef")
	joined := strings.Join(got, "\n")
	for _, frag := range []string{
		"CREATE TABLE IF NOT EXISTS periscope.stream_event_log__migstage AS periscope.stream_event_log",
		"INSERT INTO periscope.stream_event_log__migstage SELECT * FROM remote(",
		"WHERE _partition_id = '202606-deadbeef'",
		"ALTER TABLE periscope.stream_event_log REPLACE PARTITION ID '202606-deadbeef' FROM periscope.stream_event_log__migstage",
	} {
		if !strings.Contains(joined, frag) {
			t.Fatalf("SyncPartitionSQL missing %q in:\n%s", frag, joined)
		}
	}
	if strings.Contains(joined, "INSERT INTO periscope.stream_event_log ") {
		t.Fatalf("SyncPartitionSQL must REPLACE the live table's partition, never INSERT into it:\n%s", joined)
	}
}

func TestEscapeCHString_password(t *testing.T) {
	src := RemoteSource{Host: "h", DB: "periscope", User: "u", Pass: "pa'ss\\x"}
	got := src.table("t")
	if !strings.Contains(got, `'pa\'ss\\x'`) {
		t.Fatalf("password not escaped in remote(): %s", got)
	}
}

func TestFullReplaceTableSQL_atomicExchange(t *testing.T) {
	got := FullReplaceTableSQL(testRemoteSource(), "periscope", "stream_state_current")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "EXCHANGE TABLES periscope.stream_state_current AND periscope.stream_state_current__migstage") {
		t.Fatalf("FullReplaceTableSQL must atomically EXCHANGE, got:\n%s", joined)
	}
	if strings.Contains(joined, "TRUNCATE TABLE periscope.stream_state_current\n") {
		t.Fatalf("FullReplaceTableSQL must not truncate the live table (no blank window):\n%s", joined)
	}
}

func TestMVControlSQL(t *testing.T) {
	if got := StopRefreshableViewSQL("periscope", "tenant_usage_5m_mv"); got != "SYSTEM STOP VIEW periscope.tenant_usage_5m_mv" {
		t.Fatalf("StopRefreshableViewSQL = %q", got)
	}
	if got := StartRefreshableViewSQL("periscope", "tenant_usage_5m_mv"); got != "SYSTEM START VIEW periscope.tenant_usage_5m_mv" {
		t.Fatalf("StartRefreshableViewSQL = %q", got)
	}
}
