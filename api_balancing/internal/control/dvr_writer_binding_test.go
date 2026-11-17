package control

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestStaleRecordingWriterCannotFinalizeOrPromoteSuccessor(t *testing.T) {
	const current = "live+stream_fw1p_84ed1d3113d2468db7b5ef132d4e3ee9"
	ctx := dvrWriterReportContext(t.Context(), "live+stream_fw1p_faed1d3113d2468db7b5ef132d4e3ee9")
	t.Run("progress", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT status,`).WithArgs("recording").WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id", "stream_id", "stream_internal_name", "dispatch_node", "source_runtime_name", "dispatch_state"}).AddRow("recording", progressTenant, "stream-id", "stream", "edge", current, "source_handoff_pending"))
		mock.ExpectRollback()
		applied, _, err := (&dvrRepositoryDB{}).UpdateDVRProgressByHash(ctx, "recording", "recording", 100, 1, "edge")
		if applied || !errors.Is(err, errStaleDVRWriter) {
			t.Fatalf("stale writer promoted successor: applied=%v error=%v", applied, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finalization", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT COALESCE\(status, ''\)::text AS status, revision`).WithArgs("recording").WillReturnRows(sqlmock.NewRows([]string{"status", "revision", "stream_id", "source_runtime_name"}).AddRow("recording", 2, "stream-id", current))
		mock.ExpectRollback()
		_, err := claimDVRFinalization(ctx, "recording")
		if !errors.Is(err, errStaleDVRWriter) {
			t.Fatalf("stale writer claimed successor: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
