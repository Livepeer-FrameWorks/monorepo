package handlers

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEffectiveVATRateDoesNotReplaceDatabaseFailureWithDevelopmentRate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failure := errors.New("VAT rate database temporarily unavailable")
	mock.ExpectQuery("-- name: GetEffectiveVATRate").WillReturnError(failure)
	decision, err := (&X402Handler{db: db}).loadEffectiveVATRate(t.Context(), "NL", time.Now().UTC())
	if !errors.Is(err, failure) || decision.RateBPS != 0 {
		t.Fatalf("database failure became guessed VAT: %+v %v", decision, err)
	}
}
