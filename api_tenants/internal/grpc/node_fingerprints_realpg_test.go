//go:build schema_verify

package grpc

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The operator walks the duplicate listing a page at a time, unbinds one twin,
// and the delete and its node.fingerprint_unbound outbox row commit together.
func TestNodeFingerprintOperatorQueries_RealPG(t *testing.T) {
	db := startQuartermasterDomainEventsRealPG(t)
	const tenantID = "66666666-6666-4666-8666-666666666666"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// A pre-v0.3.0 database has no unique fingerprint indexes; that is the
	// state in which duplicates exist and the operator needs this listing.
	exec(`DROP INDEX quartermaster.uq_qm_fingerprints_machine`)
	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Fingerprints')`, tenantID)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
		VALUES ('fp-cluster', 'FP', 'edge', 'https://fp.example')`)
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type)
		VALUES ('fp-old', 'fp-cluster', 'old', 'edge'), ('fp-new', 'fp-cluster', 'new', 'edge'), ('fp-solo', 'fp-cluster', 'solo', 'edge')`)
	exec(`INSERT INTO quartermaster.node_fingerprints (id, node_id, tenant_id, fingerprint_machine_sha256, first_seen) VALUES
		('a0000000-0000-4000-8000-000000000001'::uuid, 'fp-old', $1::uuid, 'machine-twin', NOW() - INTERVAL '2 days'),
		('a0000000-0000-4000-8000-000000000002'::uuid, 'fp-new', $1::uuid, 'machine-twin', NOW() - INTERVAL '1 day'),
		('a0000000-0000-4000-8000-000000000003'::uuid, 'fp-solo', $1::uuid, 'machine-solo', NOW())`, tenantID)

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := fingerprintOperatorCtx()

	page := func(p *commonpb.CursorPaginationRequest) *quartermasterpb.ListNodeFingerprintsResponse {
		t.Helper()
		resp, err := server.ListNodeFingerprints(ctx, &quartermasterpb.ListNodeFingerprintsRequest{DuplicatesOnly: true, Pagination: p})
		if err != nil {
			t.Fatalf("ListNodeFingerprints: %v", err)
		}
		return resp
	}
	first := page(&commonpb.CursorPaginationRequest{First: 1})
	if len(first.GetFingerprints()) != 1 || first.GetFingerprints()[0].GetNodeId() != "fp-new" ||
		!first.GetPagination().GetHasNextPage() || first.GetPagination().GetTotalCount() != 2 {
		t.Fatalf("first page = %+v", first)
	}
	second := page(&commonpb.CursorPaginationRequest{First: 1, After: first.GetPagination().EndCursor})
	if len(second.GetFingerprints()) != 1 || second.GetFingerprints()[0].GetNodeId() != "fp-old" ||
		second.GetFingerprints()[0].GetMachineDuplicateCount() != 2 || second.GetPagination().GetHasNextPage() {
		t.Fatalf("second page = %+v", second)
	}

	unbind := &quartermasterpb.UnbindNodeFingerprintRequest{
		NodeId: "fp-old", FingerprintId: "a0000000-0000-4000-8000-000000000001", Reason: "host reinstalled as fp-new",
	}
	if _, err := server.UnbindNodeFingerprint(ctx, unbind); err != nil {
		t.Fatalf("UnbindNodeFingerprint: %v", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM quartermaster.node_fingerprints WHERE node_id = 'fp-old'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("binding still present: %d %v", remaining, err)
	}
	type outboxRow struct {
		scope, userID, resourceType, resourceID, payload string
		tenant                                           sql.NullString
	}
	readOutbox := func() []outboxRow {
		t.Helper()
		rows, err := db.Query(`SELECT scope, user_id, resource_type, resource_id, payload::text, tenant_id::text
			FROM quartermaster.service_event_outbox WHERE event_type = 'node.fingerprint_unbound'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []outboxRow
		for rows.Next() {
			var r outboxRow
			if err := rows.Scan(&r.scope, &r.userID, &r.resourceType, &r.resourceID, &r.payload, &r.tenant); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	events := readOutbox()
	if len(events) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.scope != "platform" || ev.tenant.Valid || ev.userID != "operator-user" || ev.resourceType != "node_fingerprint" ||
		ev.resourceID != unbind.FingerprintId || !strings.Contains(ev.payload, "host reinstalled as fp-new") ||
		strings.Contains(ev.payload, "machine-twin") {
		t.Fatalf("outbox row = %+v", ev)
	}

	// A repeated unbind matches nothing and records nothing.
	if _, err := server.UnbindNodeFingerprint(ctx, unbind); status.Code(err) != codes.NotFound {
		t.Fatalf("repeated unbind code = %s, want NotFound", status.Code(err))
	}
	if n := len(readOutbox()); n != 1 {
		t.Fatalf("outbox rows after repeated unbind = %d, want 1", n)
	}
	if after := page(nil); after.GetPagination().GetTotalCount() != 0 {
		t.Fatalf("duplicates after unbind = %+v", after.GetFingerprints())
	}
}
