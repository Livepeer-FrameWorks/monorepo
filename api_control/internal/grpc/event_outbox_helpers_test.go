package grpc

import (
	"database/sql/driver"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
)

// testTenantID is a UUID tenant for handler tests whose path records a domain
// event, which needs a tenant UUID.
const testTenantID = "10000000-0000-4000-8000-0000000000a1"

func testTokenHasher(t testing.TB) *events.TokenHasher {
	t.Helper()
	hasher, err := events.NewTokenHasher("commodore-test-usage-hash-secret")
	if err != nil {
		t.Fatalf("token hasher: %v", err)
	}
	return hasher
}

// expectDomainEventInsert expects the commodore.domain_event_outbox insert of
// eventType (an Exec with the event ID first and the type second).
func expectDomainEventInsert(mock sqlmock.Sqlmock, eventType string) {
	args := []driver.Value{sqlmock.AnyArg(), eventType}
	for range 11 {
		args = append(args, sqlmock.AnyArg())
	}
	mock.ExpectExec(`INSERT INTO commodore\.domain_event_outbox`).
		WithArgs(args...).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectLegacyEventInsert expects the commodore.service_event_outbox insert of
// eventType (INSERT ... RETURNING id, so a query).
func expectLegacyEventInsert(mock sqlmock.Sqlmock, eventType string) {
	mock.ExpectQuery(`INSERT INTO commodore\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventType, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-1"))
}

// expectDualEventInsert expects a domain event and its legacy row, in the
// order enqueueEventTx writes them.
func expectDualEventInsert(mock sqlmock.Sqlmock, domainType, legacyType string) {
	expectDomainEventInsert(mock, domainType)
	expectLegacyEventInsert(mock, legacyType)
}
