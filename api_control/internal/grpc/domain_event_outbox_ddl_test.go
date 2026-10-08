package grpc

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
)

// Baselines include the current shared indexes; shipped migrations retain the
// table contract consumed by the relay.
func TestCommodoreDomainEventOutboxDDLMatchesSharedTable(t *testing.T) {
	ddl, err := eventoutbox.TableDDL(DomainEventSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"schema/commodore.sql", "migrations/commodore/v0.3.11/expand/015_domain_event_outbox.sql"} {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := ddl
		if strings.HasPrefix(path, "migrations/") {
			// Shipped DDL keeps the same table contract; later migrations replace its indexes.
			want = strings.Split(ddl, "\nCREATE INDEX")[0]
		}
		if !strings.Contains(string(content), want) {
			t.Fatalf("%s does not contain outbox.TableDDL(%q) verbatim", path, DomainEventSchema)
		}
	}
}
