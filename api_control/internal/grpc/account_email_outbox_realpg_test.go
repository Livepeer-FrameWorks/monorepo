//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/sirupsen/logrus"
)

// A verification email that fails to send is retried by the outbox until SMTP
// accepts it, and only the link in the delivered email verifies the account.
// Runs the real outbox worker over the real commodore.sql schema; the SMTP
// relay is the only fake.
func TestAccountEmailOutbox_RetriesUntilDelivered_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "10000000-0000-4000-8000-00000000e001"
		userID   = "20000000-0000-4000-8000-00000000e001"
		verified = "20000000-0000-4000-8000-00000000e002"
		expired  = "20000000-0000-4000-8000-00000000e003"
	)
	for _, u := range []struct {
		id, email string
		verified  bool
	}{{userID, "new@example.com", false}, {verified, "done@example.com", true}, {expired, "late@example.com", false}} {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO commodore.users (id, tenant_id, email, verified) VALUES ($1::uuid, $2::uuid, $3, $4)
		`, u.id, tenantID, u.email, u.verified); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}

	var sent []string
	failNext := true
	server := &CommodoreServer{db: conn, logger: logrus.New()}
	server.verificationEmailSendFn = func(email, token string) error {
		if email != "new@example.com" {
			t.Fatalf("email sent to %q; only the unverified, in-window user has anything to deliver", email)
		}
		sent = append(sent, token)
		if failNext {
			failNext = false
			return errors.New("551 5.7.1 Not authorised to send from this header address")
		}
		return nil
	}

	// Register and resend write the obligation in the same transaction as the user change.
	for _, id := range []string{userID, verified, expired} {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enqueueAccountEmailTx(ctx, tx, id, tenantID, accountEmailPurposeVerification); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE commodore.account_email_outbox SET created_at = NOW() - INTERVAL '25 hours' WHERE user_id = $1::uuid`, expired); err != nil {
		t.Fatal(err)
	}

	worker := server.accountEmailOutboxWorker()
	drain := func() {
		for range 3 {
			worker.ProcessBatch(ctx)
		}
	}

	// Attempt 1: SMTP refuses. The row stays pending with the failure recorded.
	drain()
	if len(sent) != 1 {
		t.Fatalf("expected exactly one send attempt, got %d", len(sent))
	}
	var status string
	var attempts int
	var lastError sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT status, attempts, last_error FROM commodore.account_email_outbox WHERE user_id = $1::uuid`, userID).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || attempts != 1 || !lastError.Valid {
		t.Fatalf("after a failed send: status=%s attempts=%d last_error=%v, want pending/1/set", status, attempts, lastError)
	}

	// Attempt 2 once the backoff has elapsed: delivered.
	if _, err := conn.ExecContext(ctx, `UPDATE commodore.account_email_outbox SET next_attempt_at = NOW() WHERE user_id = $1::uuid`, userID); err != nil {
		t.Fatal(err)
	}
	drain()
	if len(sent) != 2 {
		t.Fatalf("expected a retry, got %d send attempts", len(sent))
	}
	if err := conn.QueryRowContext(ctx, `SELECT status FROM commodore.account_email_outbox WHERE user_id = $1::uuid`, userID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("delivered email left the row %s", status)
	}

	queries := commodoredb.New(conn)
	user, err := queries.GetVerificationUser(ctx, sql.NullString{String: hashToken(sent[1]), Valid: true})
	if err != nil || user.ID != userID {
		t.Fatalf("delivered link does not resolve the account: %+v, %v", user, err)
	}
	if _, err := queries.GetVerificationUser(ctx, sql.NullString{String: hashToken(sent[0]), Valid: true}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the undelivered first link must be dead, got %v", err)
	}

	for id, want := range map[string]string{verified: "completed", expired: "abandoned"} {
		if err := conn.QueryRowContext(ctx, `SELECT status FROM commodore.account_email_outbox WHERE user_id = $1::uuid`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != want {
			t.Fatalf("user %s: status %s, want %s", id, status, want)
		}
	}
}
