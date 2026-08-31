package control

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestAdoptRemoteArtifactCreatesReadyPointerAndSettlesRevisionAtomically(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.artifacts (")).
		WithArgs("hash-remote", "clip", "10000000-0000-0000-0000-000000000001", "artifact-internal", "stream-internal", "mp4", "synced", "cluster-origin", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.artifacts\nSET catalog_synced_rev = catalog_revision")).
		WithArgs("hash-remote", "10000000-0000-0000-0000-000000000001").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := adoptRemoteArtifactRow(context.Background(), testDB, "hash-remote", "clip",
		"10000000-0000-0000-0000-000000000001", "artifact-internal", "stream-internal", "mp4",
		"cluster-origin", "", true); err != nil {
		t.Fatalf("adoptRemoteArtifactRow: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptRemoteArtifactRollsBackWhenRevisionCannotSettle(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.artifacts (")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.artifacts\nSET catalog_synced_rev = catalog_revision")).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	if err := adoptRemoteArtifactRow(context.Background(), testDB, "hash-remote", "clip",
		"10000000-0000-0000-0000-000000000001", "artifact-internal", "stream-internal", "mp4",
		"cluster-origin", "", false); err == nil {
		t.Fatal("catalog settlement failure must roll back adoption")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptRemoteArtifactFailsClosedWhenIdentityFenceMatchesNoRow(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.artifacts (")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err = adoptRemoteArtifactRow(context.Background(), testDB, "hash-collision", "chapter",
		"10000000-0000-0000-0000-000000000001", "artifact-internal", "stream-internal", "mp4",
		"cluster-origin", "", false)
	if err == nil {
		t.Fatal("zero-row adoption must not return a relay URL backed by no local pointer")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
