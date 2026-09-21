package store

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
)

// The baseline and the v0.3.11 expand migration both contain the shared
// outbox table definition verbatim, so the relay's queries match both.
func TestNavigatorDomainEventOutboxDDLMatchesSharedTable(t *testing.T) {
	ddl, err := eventoutbox.TableDDL(DomainEventSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"schema/navigator.sql", "migrations/navigator/v0.3.11/expand/001_domain_event_outbox.sql"} {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), ddl) {
			t.Fatalf("%s does not contain outbox.TableDDL(%q) verbatim", path, DomainEventSchema)
		}
	}
}
