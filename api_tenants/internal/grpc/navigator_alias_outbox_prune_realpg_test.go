//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

// A retention pass deletes alias hand-offs delivered more than a week ago, across more than one batch, and keeps
// recently delivered and undelivered rows.
func TestNavigatorAliasOutboxPrunesDeliveredRows_RealPG(t *testing.T) {
	db := startQuartermasterDomainEventsRealPG(t)
	ctx := context.Background()
	const tenant = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.navigator_tenant_alias_outbox (tenant_id, subdomain, action, created_at, completed_at)
		SELECT $1::uuid, 'old-' || g, 'ensure', NOW() - INTERVAL '30 days', NOW() - INTERVAL '8 days'
		FROM generate_series(1, $2::int) g`, tenant, outbox.PruneBatch+5); err != nil {
		t.Fatalf("seed delivered rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.navigator_tenant_alias_outbox (tenant_id, subdomain, action, created_at, completed_at)
		VALUES ($1::uuid, 'recent', 'ensure', NOW() - INTERVAL '7 days', NOW() - INTERVAL '6 days'),
		       ($1::uuid, 'pending', 'ensure', NOW() - INTERVAL '30 days', NULL)`, tenant); err != nil {
		t.Fatalf("seed kept rows: %v", err)
	}

	pruned, err := pruneCompletedAliasOutbox(ctx, quartermasterdb.New(db))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != outbox.PruneBatch+5 {
		t.Fatalf("pruned %d rows, want %d", pruned, outbox.PruneBatch+5)
	}
	var remaining []string
	rows, err := db.QueryContext(ctx, `SELECT subdomain FROM quartermaster.navigator_tenant_alias_outbox ORDER BY subdomain`)
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subdomain string
		if err := rows.Scan(&subdomain); err != nil {
			t.Fatalf("scan: %v", err)
		}
		remaining = append(remaining, subdomain)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(remaining) != 2 || remaining[0] != "pending" || remaining[1] != "recent" {
		t.Fatalf("remaining rows %v, want [pending recent]", remaining)
	}
}
