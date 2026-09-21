package billingevents

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
)

// The baseline and the v0.3.11 expand migration both contain the shared
// outbox table definition verbatim, so the relay's queries match both.
func TestPurserDomainEventOutboxDDLMatchesSharedTable(t *testing.T) {
	ddl, err := eventoutbox.TableDDL(Schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"schema/purser.sql", "migrations/purser/v0.3.11/expand/006_domain_event_outbox.sql"} {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), ddl) {
			t.Fatalf("%s does not contain outbox.TableDDL(%q) verbatim", path, Schema)
		}
	}
}

func TestLegacyRowIDReusesTheDomainEventID(t *testing.T) {
	ev, err := New(uuid.NewString(), "topup-1", &publicv1.TopupCredited{TopupId: "topup-1", Amount: EUR(500)}, events.Actor{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := LegacyRowID(ev)
	if err != nil || id.String() != ev.ID {
		t.Fatalf("legacy row id = %s, %v, want %s", id, err, ev.ID)
	}
	fresh, err := LegacyRowID(nil)
	if err != nil || fresh.Version() != 7 {
		t.Fatalf("legacy row id without a domain event = %s, %v, want a fresh UUIDv7", fresh, err)
	}
}

func TestEURFromDecimal(t *testing.T) {
	for amount, want := range map[string]int64{"20.00": 2000, "0.5": 50, " 12.345 ": 1235, "0": 0} {
		money, err := EURFromDecimal(amount)
		if err != nil || money.GetAmountMinor() != want || money.GetCurrency() != "EUR" {
			t.Fatalf("EURFromDecimal(%q) = %v, %v, want %d EUR", amount, money, err, want)
		}
	}
	if _, err := EURFromDecimal("twelve"); err == nil {
		t.Fatal("EURFromDecimal accepted a non-numeric amount")
	}
}
