//go:build schema_verify

package foghorndb

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestPublisherLookupPlansBounded_RealPG(t *testing.T) {
	verifyPublisherLookupPlans(t, startFoghornCatalogPostgres(t), false)
}
func TestPublisherLookupPlansBounded_RealYugabyte(t *testing.T) {
	conn, ok := dockerpg.OpenSharedYugabyteBaseline(t, "publisher_lookup_plan", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	verifyPublisherLookupPlans(t, conn, true)
}

func verifyPublisherLookupPlans(t *testing.T, conn *sql.DB, distributed bool) {
	ctx := t.Context()
	const tenant = "a4000000-0000-0000-0000-000000000001"
	t.Run("preflight finds inactive authority without scanning other credentials", func(t *testing.T) {
		_, err := conn.ExecContext(ctx, `INSERT INTO foghorn.media_object_authority_projection
 (authority_id,authority_version,object_kind,tenant_id,internal_name,playback_id,lifecycle,playback_policy_kind,playback_policy,stream_id,ingest_mode,valid_until,publishing_credential_sha256)
 SELECT 'lookup-plan-'||i,1,'live_stream',$1::uuid,'lookup-plan-'||i,'lookup-plan-'||i,'inactive','public','\x'::bytea,$1::uuid,'push',NOW()+INTERVAL '1 hour',decode(repeat(md5(i::text),2),'hex') FROM generate_series(1,5000) i`, tenant)
		if err != nil {
			t.Fatal(err)
		}
		for _, identity := range []struct{ kind, id string }{{"tenant", tenant}} {
			if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.media_authorities (authority_kind,authority_id,authority_version,signer_key_id,audience_cell_id,issued_at,refresh_after,valid_until,payload_sha256,signed_envelope,payload) VALUES ($1,$2,1,'lookup-key','lookup-cell',NOW(),NOW()+INTERVAL '30 minutes',NOW()+INTERVAL '1 hour',decode(repeat('00',32),'hex'),'\x00'::bytea,'\x01'::bytea)`, identity.kind, identity.id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.media_authorities (authority_kind,authority_id,authority_version,signer_key_id,audience_cell_id,issued_at,refresh_after,valid_until,payload_sha256,signed_envelope,payload)
 SELECT 'media_object',authority_id,1,'lookup-key','lookup-cell',NOW(),NOW()+INTERVAL '30 minutes',NOW()+INTERVAL '1 hour',decode(repeat('00',32),'hex'),'\x00'::bytea,'\x01'::bytea FROM foghorn.media_object_authority_projection WHERE authority_id LIKE 'lookup-plan-%'`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO foghorn.tenant_authority_projection (tenant_id,authority_version,lifecycle,billing_decision,billing_model,valid_until) VALUES ($1::uuid,1,'active','allow','postpaid',NOW()+INTERVAL '1 hour')`, tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `ANALYZE foghorn.media_object_authority_projection`); err != nil {
			t.Fatal(err)
		}
		var digest []byte
		if err := conn.QueryRowContext(ctx, `SELECT publishing_credential_sha256 FROM foghorn.media_object_authority_projection WHERE authority_id='lookup-plan-5000'`).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		got, err := New(conn).GetPublisherAuthorityPreflight(ctx, GetPublisherAuthorityPreflightParams{NodeID: "lookup-node", TriggerUuid: "lookup-execution", CredentialDigest: digest})
		if err != nil || got.ObjectAuthorityID != "lookup-plan-5000" {
			t.Fatalf("inactive authority lookup=%+v %v", got, err)
		}
		assertPublisherLookupScanned(t, conn, distributed, "media_object_authority_projection", getPublisherAuthorityPreflight, "lookup-node", "lookup-execution", digest)
	})
	t.Run("legacy cleanup finds ended execution without scanning retained history", func(t *testing.T) {
		_, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_sessions
 (tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,started_at_unix_millis,publisher_admission_version,ended_at,ended_reason)
 SELECT md5(i::text)::uuid,'legacy-lookup-node-'||i,'legacy-lookup-stream-'||i,i,'legacy-lookup-execution-'||i,1,0,NOW(),'node_lost' FROM generate_series(1,5000) i`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `ANALYZE foghorn.ingest_sessions`); err != nil {
			t.Fatal(err)
		}
		rows, err := New(conn).ListLegacyPublisherClaimReleasesByExecution(ctx, ListLegacyPublisherClaimReleasesByExecutionParams{NodeID: "legacy-lookup-node-5000", TriggerUuid: "legacy-lookup-execution-5000"})
		if err != nil || len(rows) != 1 || !rows[0].Ended || rows[0].TenantID == "" {
			t.Fatalf("ended legacy owner=%+v %v", rows, err)
		}
		// A valid node paired with another execution is a normal marker miss.
		assertPublisherLookupScanned(t, conn, distributed, "ingest_sessions", listLegacyPublisherClaimReleasesByExecution, "legacy-lookup-node-5000", "legacy-lookup-execution-4999")
		assertPublisherLookupScanned(t, conn, distributed, "ingest_sessions", listLegacyPublisherClaimReleasesByExecution, "legacy-lookup-node-5000", "legacy-lookup-execution-5000")
	})
	t.Run("PID identity checks all retained protocol versions without scanning other executions", func(t *testing.T) {
		_, err := conn.ExecContext(ctx, `INSERT INTO foghorn.ingest_sessions
 (tenant_id,node_id,stream_internal_name,connector_pid,start_trigger_uuid,started_at_unix_millis,publisher_admission_version,ended_at,ended_reason)
 SELECT md5(('pid-'||i)::text)::uuid,'pid-lookup-node-'||i,'pid-lookup-stream-'||i,i,'pid-lookup-execution-'||i,1,(i % 2)::smallint,NOW(),'node_lost' FROM generate_series(1,6000) i`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, `ANALYZE foghorn.ingest_sessions`); err != nil {
			t.Fatal(err)
		}
		q := New(conn)
		for _, test := range []struct {
			node, execution string
			pid             int64
			want            bool
		}{
			{"pid-lookup-node-6000", "pid-lookup-execution-6000", 6000, false},
			{"pid-lookup-node-6000", "pid-lookup-execution-6000", 6001, true},
			{"pid-lookup-node-5999", "pid-lookup-execution-5999", 5999, false},
			{"pid-lookup-node-5999", "pid-lookup-execution-5999", 6001, true},
			{"pid-lookup-node-6000", "pid-lookup-execution-5999", 6000, false},
		} {
			got, err := q.IngestAdmissionExecutionPIDMismatch(ctx, IngestAdmissionExecutionPIDMismatchParams{NodeID: test.node, StartTriggerUuid: test.execution, ConnectorPid: test.pid})
			if err != nil || got != test.want {
				t.Fatalf("PID mismatch node=%s execution=%s pid=%d: got=%v want=%v err=%v", test.node, test.execution, test.pid, got, test.want, err)
			}
			assertPublisherLookupScanned(t, conn, distributed, "ingest_sessions", ingestAdmissionExecutionPIDMismatch, test.node, test.execution, test.pid)
		}
	})
}

func assertPublisherLookupScanned(t *testing.T, conn *sql.DB, distributed bool, relation, query string, args ...any) {
	t.Helper()
	explain := "EXPLAIN (ANALYZE, FORMAT JSON) "
	if distributed {
		explain = "EXPLAIN (ANALYZE, DIST, FORMAT JSON) "
	}
	var raw []byte
	if err := conn.QueryRowContext(t.Context(), explain+query, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan publisherLookupPlanNode }
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	scanned := 0.0
	var walk func(publisherLookupPlanNode)
	walk = func(n publisherLookupPlanNode) {
		if n.Relation == relation {
			scanned += max((n.Rows+n.Removed)*n.Loops, (n.TableRows+n.IndexRows)*n.Loops)
		}
		for _, child := range n.Plans {
			walk(child)
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
	t.Logf("publisher lookup examined %.0f %s rows", scanned, relation)
	if scanned > 64 {
		t.Fatalf("publisher lookup examined %.0f %s rows; want <=64:\n%s", scanned, relation, raw)
	}
}

type publisherLookupPlanNode struct {
	TableRows float64                   `json:"Storage Table Rows Scanned"`
	IndexRows float64                   `json:"Storage Index Rows Scanned"`
	Relation  string                    `json:"Relation Name"`
	Rows      float64                   `json:"Actual Rows"`
	Removed   float64                   `json:"Rows Removed by Filter"`
	Loops     float64                   `json:"Actual Loops"`
	Plans     []publisherLookupPlanNode `json:"Plans"`
}
