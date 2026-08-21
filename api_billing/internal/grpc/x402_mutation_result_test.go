package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	mutationTenantID    = "11111111-1111-1111-1111-111111111111"
	mutationQuoteID     = "22222222-2222-2222-2222-222222222222"
	mutationFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func mutationClaimRequest() *purserpb.ClaimX402MutationResultRequest {
	return &purserpb.ClaimX402MutationResultRequest{
		TenantId: mutationTenantID, QuoteId: mutationQuoteID,
		IdempotencyKey: "mutation-123", RequestFingerprint: mutationFingerprint,
		Protocol: "http", Operation: "createStream",
	}
}

func TestClaimX402MutationResultClaimsAndReplays(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inserted  int64
		status    string
		result    []byte
		wantState string
	}{
		{name: "new claim", inserted: 1, status: "claimed", wantState: "claimed"},
		{name: "completed replay", inserted: 0, status: "completed", result: []byte(`{"data":{"id":"stream-1"}}`), wantState: "completed"},
		{name: "unknown outcome remains in progress", inserted: 0, status: "claimed", wantState: "in_progress"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			server := &PurserServer{db: db, logger: logging.NewLogger()}
			mock.ExpectExec(`INSERT INTO purser\.x402_mutation_results`).
				WillReturnResult(sqlmock.NewResult(0, tc.inserted))
			mock.ExpectQuery(`SELECT quote_id::text AS quote_id, request_fingerprint, protocol, operation, status`).
				WillReturnRows(sqlmock.NewRows([]string{
					"quote_id", "request_fingerprint", "protocol", "operation", "status", "result", "content_type", "status_code", "updated_at",
				}).AddRow(mutationQuoteID, mutationFingerprint, "http", "createStream", tc.status, tc.result, "application/json", 201, time.Now()))

			response, err := server.ClaimX402MutationResult(context.Background(), mutationClaimRequest())
			if err != nil {
				t.Fatal(err)
			}
			if response.GetState() != tc.wantState {
				t.Fatalf("state=%q want=%q", response.GetState(), tc.wantState)
			}
			if tc.wantState == "completed" && string(response.GetResult()) != string(tc.result) {
				t.Fatalf("replayed result=%q want=%q", response.GetResult(), tc.result)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaimX402MutationResultRejectsFingerprintCollision(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	mock.ExpectExec(`INSERT INTO purser\.x402_mutation_results`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT quote_id::text AS quote_id, request_fingerprint, protocol, operation, status`).
		WillReturnRows(sqlmock.NewRows([]string{
			"quote_id", "request_fingerprint", "protocol", "operation", "status", "result", "content_type", "status_code", "updated_at",
		}).AddRow(mutationQuoteID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "http", "createStream", "completed", []byte(`{}`), "application/json", 200, time.Now()))

	_, err = server.ClaimX402MutationResult(context.Background(), mutationClaimRequest())
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("error=%v code=%v want AlreadyExists", err, status.Code(err))
	}
}

func TestClaimX402MutationResultMovesAbandonedClaimToOperatorReview(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	mock.ExpectExec(`INSERT INTO purser\.x402_mutation_results`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT quote_id::text AS quote_id, request_fingerprint, protocol, operation, status`).
		WillReturnRows(sqlmock.NewRows([]string{
			"quote_id", "request_fingerprint", "protocol", "operation", "status", "result", "content_type", "status_code", "updated_at",
		}).AddRow(mutationQuoteID, mutationFingerprint, "http", "createStream", "claimed", nil, nil, nil, time.Now().Add(-16*time.Minute)))
	mock.ExpectExec(`UPDATE purser\.x402_mutation_results`).WillReturnResult(sqlmock.NewResult(0, 1))
	response, err := server.ClaimX402MutationResult(context.Background(), mutationClaimRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetState() != "operator_review" {
		t.Fatalf("state=%q", response.GetState())
	}
}

func TestCompleteX402MutationResultIsIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	mock.ExpectExec(`UPDATE purser\.x402_mutation_results`).WillReturnResult(sqlmock.NewResult(0, 1))
	response, err := server.CompleteX402MutationResult(context.Background(), &purserpb.CompleteX402MutationResultRequest{
		TenantId: mutationTenantID, QuoteId: mutationQuoteID,
		IdempotencyKey: "mutation-123", RequestFingerprint: mutationFingerprint,
		Result: []byte(`{"data":{"id":"stream-1"}}`), ContentType: "application/json", StatusCode: 201,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetCompleted() {
		t.Fatal("expected completed response")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
