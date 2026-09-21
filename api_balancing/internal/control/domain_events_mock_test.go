package control

import (
	"github.com/DATA-DOG/go-sqlmock"
)

// mockTenantUUID is a tenant for sqlmock paths that publish domain events,
// whose envelope requires a tenant UUID.
const mockTenantUUID = "11111111-1111-4111-8111-111111111111"

// mockArtifactRevision is the revision the mocked artifact row returns when a
// lifecycle transition advances it.
const mockArtifactRevision = int64(3)

// expectDomainEventInsert expects one foghorn.domain_event_outbox row of
// eventType keyed by aggregateID.
func expectDomainEventInsert(mock sqlmock.Sqlmock, eventType, aggregateID string) {
	expectVersionedDomainEventInsert(mock, eventType, aggregateID, sqlmock.AnyArg())
}

func expectVersionedDomainEventInsert(mock sqlmock.Sqlmock, eventType, aggregateID string, version any) {
	mock.ExpectExec(`INSERT INTO foghorn\.domain_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventType, "foghorn", sqlmock.AnyArg(), aggregateID, version,
			"tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectTransitionInsert expects a domain event followed by its legacy
// artifact_event_outbox row, which carries the event's ID. A lifecycle event
// first advances the artifact's revision and carries it as the aggregate
// version; a node-copy change carries none.
func expectTransitionInsert(mock sqlmock.Sqlmock, eventType, aggregateID, kind, tenantID, streamID, artifactID string) {
	if eventType == "artifact.node_copy_changed" {
		expectVersionedDomainEventInsert(mock, eventType, aggregateID, int64(0))
	} else {
		mock.ExpectQuery(`UPDATE foghorn\.artifacts\s+SET revision = revision \+ 1`).
			WithArgs(aggregateID, tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(mockArtifactRevision))
		expectVersionedDomainEventInsert(mock, eventType, aggregateID, mockArtifactRevision)
	}
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(sqlmock.AnyArg(), kind, tenantID, streamID, artifactID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
