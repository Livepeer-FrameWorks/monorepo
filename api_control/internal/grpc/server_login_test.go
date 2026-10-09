package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLoginChecksPasswordBeforeUnverifiedState(t *testing.T) {
	hashedPassword, err := auth.HashPassword("correct-password")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}

	tests := []struct {
		name        string
		password    string
		wantMessage string
	}{
		{
			name:        "wrong password stays generic",
			password:    "wrong-password",
			wantMessage: "invalid credentials",
		},
		{
			name:        "correct password exposes verification state",
			password:    "correct-password",
			wantMessage: "email not verified",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			now := time.Now()
			rows := sqlmock.NewRows([]string{
				"id", "tenant_id", "email", "password_hash", "first_name", "last_name",
				"role", "permissions", "is_active", "verified", "created_at", "updated_at", "platform_operator",
			}).AddRow(
				"user-1", "tenant-1", "user@example.com", hashedPassword,
				sql.NullString{}, sql.NullString{}, "owner", pq.StringArray{"streams:read"},
				true, false, now, now, false,
			)
			mock.ExpectQuery(regexp.QuoteMeta("FROM commodore.users WHERE lower(email::text) = lower($1::text)")).
				WithArgs("user@example.com").
				WillReturnRows(rows)
			// The rejected sign-in is recorded in its own transaction.
			mock.ExpectBegin()
			expectLegacyEventInsert(mock, eventAuthLoginFailed)
			mock.ExpectCommit()

			server := &CommodoreServer{db: db, logger: logrus.New()}
			_, err = server.Login(context.Background(), &commodorepb.LoginRequest{
				Email:    "user@example.com",
				Password: tt.password,
				Behavior: &commodorepb.BehaviorData{
					FormShownAt: 1,
					SubmittedAt: 5000,
					Mouse:       true,
					Typed:       true,
				},
				HumanCheck: "human",
			})
			if err == nil {
				t.Fatal("expected login error")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected status error, got %v", err)
			}
			if st.Code() != codes.Unauthenticated {
				t.Fatalf("code = %s, want %s", st.Code(), codes.Unauthenticated)
			}
			if !strings.Contains(st.Message(), tt.wantMessage) {
				t.Fatalf("message = %q, want %q", st.Message(), tt.wantMessage)
			}
			sanitized := grpcutil.SanitizeError(err)
			if got, want := grpcutil.IsEmailNotVerified(sanitized), tt.password == "correct-password"; got != want {
				t.Fatalf("sanitized email verification reason = %v, want %v", got, want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// An unknown email and an account without a password must cost the same bcrypt
// comparison as a wrong password and answer the same way, so response timing
// does not reveal whether an account exists.
func TestLoginComparesPasswordWhenNoStoredHashExists(t *testing.T) {
	loginRequest := &commodorepb.LoginRequest{
		Email:    "nobody@example.com",
		Password: "guess",
		Behavior: &commodorepb.BehaviorData{
			FormShownAt: 1,
			SubmittedAt: 5000,
			Mouse:       true,
			Typed:       true,
		},
		HumanCheck: "human",
	}
	loginQuery := regexp.QuoteMeta("FROM commodore.users WHERE lower(email::text) = lower($1::text)")

	tests := []struct {
		name   string
		expect func(sqlmock.Sqlmock)
	}{
		{
			name: "unknown email",
			expect: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(loginQuery).WithArgs("nobody@example.com").WillReturnError(sql.ErrNoRows)
			},
		},
		{
			name: "account without a password",
			expect: func(mock sqlmock.Sqlmock) {
				now := time.Now()
				mock.ExpectQuery(loginQuery).WithArgs("nobody@example.com").WillReturnRows(sqlmock.NewRows([]string{
					"id", "tenant_id", "email", "password_hash", "first_name", "last_name",
					"role", "permissions", "is_active", "verified", "created_at", "updated_at", "platform_operator",
				}).AddRow(
					"user-1", "tenant-1", "nobody@example.com", "",
					sql.NullString{}, sql.NullString{}, "owner", pq.StringArray{},
					true, true, now, now, false,
				))
				mock.ExpectBegin()
				expectLegacyEventInsert(mock, eventAuthLoginFailed)
				mock.ExpectCommit()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			tt.expect(mock)

			var compared []string
			server := &CommodoreServer{db: db, logger: logrus.New(), checkPassword: func(_, hash string) bool {
				compared = append(compared, hash)
				// A matching comparison still must not admit a login that has no stored hash.
				return true
			}}
			_, err = server.Login(context.Background(), loginRequest)
			if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != "invalid credentials" {
				t.Fatalf("login error = %v, want Unauthenticated invalid credentials", err)
			}
			if len(compared) != 1 || compared[0] != auth.DummyPasswordHash {
				t.Fatalf("password comparisons = %q, want one against the dummy hash", compared)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
