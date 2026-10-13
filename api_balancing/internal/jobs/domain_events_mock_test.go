package jobs

import (
	"github.com/DATA-DOG/go-sqlmock"
)

// mockTenantUUID is a tenant for sqlmock paths that publish domain events,
// whose envelope requires a tenant UUID.
const mockTenantUUID = "11111111-1111-4111-8111-111111111111"

// mockArtifactRevision is the revision the mocked artifact row returns when a
// lifecycle transition advances it.
const mockArtifactRevision = int64(3)

// expectTransitionInsert expects a domain event of eventType keyed by
// aggregateID, followed by its legacy artifact_event_outbox row, which carries
// the event's ID. A lifecycle event first advances the artifact's revision and
// carries it as the aggregate version; a node-copy change carries none.
func expectTransitionInsert(mock sqlmock.Sqlmock, eventType, aggregateID, kind, tenantID, streamID, artifactID string) {
	version := int64(0)
	if eventType != "artifact.node_copy_changed" {
		mock.ExpectQuery(`UPDATE foghorn\.artifacts\s+SET revision = revision \+ 1`).
			WithArgs(aggregateID, tenantID).
			WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(mockArtifactRevision))
		version = mockArtifactRevision
	}
	mock.ExpectExec(`INSERT INTO foghorn\.domain_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventType, "foghorn", sqlmock.AnyArg(), aggregateID, version,
			"tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn\.artifact_event_outbox`).
		WithArgs(sqlmock.AnyArg(), kind, tenantID, streamID, artifactID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// expectChapterFailedEvent expects MarkChapterFailed's recording.chapter_failed:
// the chapter's recording context, then the fact on the parent recording.
func expectChapterFailedEvent(mock sqlmock.Sqlmock, chapterID, recordingHash string) {
	mock.ExpectQuery(`SELECT c.artifact_hash AS recording_hash`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"recording_hash", "stream_id", "tenant_id", "start_ms", "end_ms", "playback_id", "replayable_chapters"}).
			AddRow(recordingHash, "", mockTenantUUID, int64(0), int64(60000), "", int64(0)))
	mock.ExpectQuery(`UPDATE foghorn\.artifacts\s+SET revision = revision \+ 1`).
		WithArgs(recordingHash, mockTenantUUID).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(mockArtifactRevision))
	mock.ExpectExec(`INSERT INTO foghorn\.domain_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "recording.chapter_failed", "foghorn", sqlmock.AnyArg(), recordingHash, mockArtifactRevision,
			"tenant", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}
