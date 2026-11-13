package handlers

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestWebhookEventFailureStoresBoundedCode(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("UPDATE purser.webhook_events").WithArgs("blocked", "provider callback blocked", false, "stripe", "evt_known").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := (&Service{db: db}).markWebhookFailed(t.Context(), "stripe", "evt_known", "provider failure containing credential=secret", true, false); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
