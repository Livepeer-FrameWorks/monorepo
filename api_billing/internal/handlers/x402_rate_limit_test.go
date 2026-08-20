package handlers

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestConsumeX402RateLimitRejectsAboveLimit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &X402Handler{db: db}
	mock.ExpectExec("DELETE FROM purser.x402_rate_limit_windows").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO purser.x402_rate_limit_windows (")).
		WithArgs("settle_quote", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"request_count"}).AddRow(6))
	if err := h.consumeX402RateLimit(context.Background(), "settle_quote", "quote-1", 5); err == nil {
		t.Fatal("rate limit accepted a request above the configured limit")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeX402RateLimitSkipsUnavailableIdentity(t *testing.T) {
	h := &X402Handler{}
	if err := h.consumeX402RateLimit(context.Background(), "quote_ip", "", 30); err != nil {
		t.Fatal(err)
	}
}
