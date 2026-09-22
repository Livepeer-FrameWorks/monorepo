package mediaauthority

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/lib/pq/pqerror"
)

func TestMarkPlaybackPairLocalReadReadyReplaysOnSerializationFailure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	store := &Store{db: db}

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn\\.tenant_authority_projection").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn\\.media_object_authority_projection").
		WillReturnError(&pq.Error{Code: "40001", Message: "restart read required"})
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE foghorn\\.tenant_authority_projection").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE foghorn\\.media_object_authority_projection").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	promoted, err := store.MarkPlaybackPairLocalReadReady(context.Background(), "tenant-a", 3, "object-a", 7)
	if err != nil {
		t.Fatalf("MarkPlaybackPairLocalReadReady: %v", err)
	}
	if !promoted {
		t.Fatal("expected the replayed transaction to promote the pair")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCollectOneReplaysAndResetsItsCommittedResult(t *testing.T) {
	for _, deletedOnReplay := range []int64{0, 1} {
		t.Run(fmt.Sprint(deletedOnReplay), func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			for attempt, deleted := range []int64{1, deletedOnReplay} {
				mock.ExpectBegin()
				expectMediaAuthorityLockTimeout(mock)
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("DELETE FROM foghorn.media_authorities").WillReturnResult(sqlmock.NewResult(0, deleted))
				if deleted != 0 {
					mock.ExpectExec("DELETE FROM foghorn.tenant_authority_projection").WillReturnResult(sqlmock.NewResult(0, 1))
				}
				if attempt == 0 {
					mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001"})
				} else {
					mock.ExpectCommit()
				}
			}
			collected, err := store.collectOne(context.Background(), foghorndb.ListCollectableMediaAuthoritiesRow{
				AuthorityKind: "tenant", AuthorityID: "tenant-a", AuthorityVersion: 7,
			})
			if err != nil || collected != (deletedOnReplay != 0) {
				t.Fatalf("collected=%v err=%v", collected, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectOneOnlySuppressesLockContention(t *testing.T) {
	for _, code := range []pqerror.Code{"55P03", "42501"} {
		t.Run(string(code), func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			mock.ExpectBegin()
			expectMediaAuthorityLockTimeout(mock)
			cause := &pq.Error{Code: code}
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnError(cause)
			mock.ExpectRollback()
			collected, err := store.collectOne(context.Background(), foghorndb.ListCollectableMediaAuthoritiesRow{
				AuthorityKind: "tenant", AuthorityID: "tenant-a", AuthorityVersion: 7,
			})
			if collected || (code == "55P03" && err != nil) || (code != "55P03" && !errors.Is(err, cause)) {
				t.Fatalf("collected=%v err=%v", collected, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchedDuplicateReplaysConfirmationBeforeObserver(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	encoded, _, signed := storeFixture(t, "cell-a")
	observed := 0
	store.SetApplyObserver(func(context.Context, ApplyResult) error {
		observed++
		return nil
	})
	for attempt := range 2 {
		mock.ExpectBegin()
		expectMediaAuthorityLockTimeout(mock)
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT authority_version, payload_sha256, payload").WillReturnRows(
			sqlmock.NewRows([]string{"authority_version", "payload_sha256", "payload", "valid_until"}).
				AddRow(7, signed.GetEnvelope().GetPayloadSha256(), signed.GetEnvelope().GetPayload(), storeFixtureNow.Add(time.Hour)),
		)
		mock.ExpectExec("INSERT INTO foghorn.media_authority_apply_audit").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("UPDATE foghorn.media_authorities").
			WithArgs(storeFixtureNow, "tenant", signed.GetEnvelope().GetAuthorityId(), int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		if attempt == 0 {
			mock.ExpectCommit().WillReturnError(&pq.Error{Code: "40001"})
		} else {
			mock.ExpectCommit()
		}
	}
	result, err := store.apply(context.Background(), encoded, storeFixtureNow, 0)
	if err != nil || result.Status != ApplyStatusDuplicate || observed != 1 {
		t.Fatalf("result=%+v err=%v observed=%d", result, err, observed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
