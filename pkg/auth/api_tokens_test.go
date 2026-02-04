package auth

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
)

func TestValidateAPIToken(t *testing.T) {
	query := regexp.QuoteMeta(`
		SELECT id, tenant_id, user_id, token_name,
		       permissions, is_active, expires_at, created_at
		FROM commodore.api_tokens
		WHERE token_value = $1 AND is_active = true
	`)
	baseTime := time.Now()

	tests := []struct {
		name           string
		tokenValue     string
		setupMock      func(sqlmock.Sqlmock)
		wantErr        error
		wantErrContain string
		wantTokenID    string
	}{
		{
			name:       "valid token",
			tokenValue: "valid-token",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "tenant_id", "user_id", "token_name", "permissions", "is_active", "expires_at", "created_at",
				}).AddRow(
					"token-id",
					"tenant-id",
					"user-id",
					"token-name",
					pq.Array([]string{"read", "write"}),
					true,
					baseTime.Add(10*time.Minute),
					baseTime,
				)
				mock.ExpectQuery(query).WithArgs(hashToken("valid-token")).WillReturnRows(rows)
			},
			wantTokenID: "token-id",
		},
		{
			name:       "expired token",
			tokenValue: "expired-token",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "tenant_id", "user_id", "token_name", "permissions", "is_active", "expires_at", "created_at",
				}).AddRow(
					"token-id",
					"tenant-id",
					"user-id",
					"token-name",
					pq.Array([]string{}),
					true,
					baseTime.Add(-10*time.Minute),
					baseTime,
				)
				mock.ExpectQuery(query).WithArgs(hashToken("expired-token")).WillReturnRows(rows)
			},
			wantErr: ErrExpiredAPIToken,
		},
		{
			name:       "invalid token",
			tokenValue: "missing-token",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(query).WithArgs(hashToken("missing-token")).WillReturnError(sql.ErrNoRows)
			},
			wantErr: ErrInvalidAPIToken,
		},
		{
			name:       "inactive token",
			tokenValue: "inactive-token",
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{
					"id", "tenant_id", "user_id", "token_name", "permissions", "is_active", "expires_at", "created_at",
				}).AddRow(
					"token-id",
					"tenant-id",
					"user-id",
					"token-name",
					pq.Array([]string{"read"}),
					false,
					baseTime.Add(10*time.Minute),
					baseTime,
				)
				mock.ExpectQuery(query).WithArgs(hashToken("inactive-token")).WillReturnRows(rows)
			},
			wantErr: ErrInvalidAPIToken,
		},
		{
			name:       "db error",
			tokenValue: "error-token",
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(query).WithArgs(hashToken("error-token")).WillReturnError(errors.New("db down"))
			},
			wantErrContain: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer db.Close()

			tt.setupMock(mock)

			token, err := ValidateAPIToken(db, tt.tokenValue)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if tt.wantErrContain != "" {
				if err == nil {
					t.Fatalf("expected error containing %q", tt.wantErrContain)
				}
				if !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErrContain, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token == nil {
				t.Fatal("expected token")
			}
			if token.ID != tt.wantTokenID {
				t.Fatalf("expected token ID %q, got %q", tt.wantTokenID, token.ID)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

func TestHasPermission(t *testing.T) {
	token := &APIToken{Permissions: []string{"read", "write"}}

	if !token.HasPermission("read") {
		t.Error("expected permission read")
	}
	if !token.HasPermission("write") {
		t.Error("expected permission write")
	}
	if token.HasPermission("admin") {
		t.Error("unexpected permission admin")
	}
	if (&APIToken{}).HasPermission("read") {
		t.Error("empty permissions should not match")
	}
}

func TestHashToken(t *testing.T) {
	first := hashToken("token-a")
	second := hashToken("token-a")
	third := hashToken("token-b")

	if first != second {
		t.Fatal("expected hash to be deterministic")
	}
	if first == third {
		t.Fatal("expected different inputs to hash differently")
	}
	if len(first) != 64 {
		t.Fatalf("expected 64 hex characters, got %d", len(first))
	}
}
