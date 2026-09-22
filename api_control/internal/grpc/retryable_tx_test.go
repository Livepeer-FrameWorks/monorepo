package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newRetryTestServer(t *testing.T) (*CommodoreServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := logrus.New()
	return &CommodoreServer{db: db, logger: logger}, mock
}

func serializationFailure() error {
	return &pq.Error{Code: "40001", Message: "could not serialize access due to concurrent update"}
}

func TestUnlinkWalletReplaysSerializationFailure(t *testing.T) {
	s, mock := newRetryTestServer(t)

	expectBody := func() {
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"has_password"}).AddRow(true))
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
		mock.ExpectQuery("DELETE FROM commodore.wallet_identities").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wallet-1"))
		expectLegacyEventInsert(mock, eventWalletUnlinked)
	}

	// First attempt: the mutation and its event both succeed, then the commit
	// aborts and rolls both back.
	expectBody()
	mock.ExpectCommit().WillReturnError(serializationFailure())

	// Replay: the mutation and event run again in a fresh transaction.
	expectBody()
	mock.ExpectCommit()

	resp, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("unlink = (%+v, %v)", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkWalletReplaysStatementSerializationFailure(t *testing.T) {
	s, mock := newRetryTestServer(t)

	// The body maps this failure to a gRPC status; the SQLSTATE must still
	// reach the retry helper so the transaction is replayed.
	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
		WithArgs("user-1", "tenant-1").
		WillReturnError(serializationFailure())
	mock.ExpectRollback()

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
		WithArgs("user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"has_password"}).AddRow(true))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs("wallet-1", "user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
	mock.ExpectQuery("DELETE FROM commodore.wallet_identities").
		WithArgs("wallet-1", "user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wallet-1"))
	expectLegacyEventInsert(mock, eventWalletUnlinked)
	mock.ExpectCommit()

	resp, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("unlink = (%+v, %v)", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnlinkWalletReportsStatusAfterRetriesExhausted(t *testing.T) {
	s, mock := newRetryTestServer(t)
	for range database.DefaultRetryAttempts {
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
			WithArgs("user-1", "tenant-1").
			WillReturnError(serializationFailure())
		mock.ExpectRollback()
	}

	_, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal || st.Message() != "failed to verify account authentication methods" {
		t.Fatalf("error = %v, want Internal status with the body's message", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestApplyStreamPlacementKeepsTheDatabaseCause proves a serialization abort
// while locking placement scopes reaches the retry helper as retryable, while
// the caller still receives the mapped gRPC status.
func TestApplyStreamPlacementKeepsTheDatabaseCause(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectExec("INSERT INTO commodore.media_placement_policies").WillReturnError(serializationFailure())

	plan := &streamPlacementPlan{write: true}
	_, applyErr := applyStreamPlacement(context.Background(), db, "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "actor-1", plan)
	if !database.IsRetryablePostgresError(applyErr) {
		t.Fatalf("placement error is not retryable: %v", applyErr)
	}
	var wrapped *txStatusError
	if !errors.As(applyErr, &wrapped) || status.Code(wrapped.status) != codes.Internal {
		t.Fatalf("placement error = %v, want txStatusError carrying codes.Internal", applyErr)
	}
}
