package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"

	pb "frameworks/pkg/proto"
)

func TestValidateStreamKey(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       *pb.ValidateStreamKeyRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, *pb.ValidateStreamKeyResponse, error)
	}{
		{
			name: "empty_stream_key",
			req:  &pb.ValidateStreamKeyRequest{StreamKey: ""},
			assert: func(t *testing.T, resp *pb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "stream_key required" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "invalid_stream_key",
			req:  &pb.ValidateStreamKeyRequest{StreamKey: "bad-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM commodore.streams").WithArgs("bad-key").WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *pb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "Invalid stream key" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "inactive_user",
			req:  &pb.ValidateStreamKeyRequest{StreamKey: "inactive-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", false, true)
				mock.ExpectQuery("FROM commodore.streams").WithArgs("inactive-key").WillReturnRows(rows)
			},
			assert: func(t *testing.T, resp *pb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "User account is inactive" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "active_user",
			req:  &pb.ValidateStreamKeyRequest{StreamKey: "good-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", true, true)
				mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
			},
			assert: func(t *testing.T, resp *pb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !resp.Valid {
					t.Fatalf("expected valid response")
				}
				if resp.BillingModel != "postpaid" {
					t.Fatalf("unexpected billing model: %q", resp.BillingModel)
				}
				if resp.InternalName != "internal" {
					t.Fatalf("unexpected internal name: %q", resp.InternalName)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &CommodoreServer{db: db, logger: logrus.New()}
			resp, err := server.ValidateStreamKey(ctx, test.req)
			test.assert(t, resp, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func TestValidateAPIToken(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       *pb.ValidateAPITokenRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, *pb.ValidateAPITokenResponse, error)
	}{
		{
			name: "empty_token",
			req:  &pb.ValidateAPITokenRequest{Token: ""},
			assert: func(t *testing.T, resp *pb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
			},
		},
		{
			name: "invalid_token",
			req:  &pb.ValidateAPITokenRequest{Token: "bad-token"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM commodore.api_tokens").WithArgs(hashToken("bad-token")).WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *pb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
			},
		},
		{
			name: "valid_token",
			req:  &pb.ValidateAPITokenRequest{Token: "good-token"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "permissions"}).
					AddRow("token-id", "user-id", "tenant-id", "{read,write}")
				mock.ExpectQuery("FROM commodore.api_tokens").WithArgs(hashToken("good-token")).WillReturnRows(rows)
				mock.ExpectExec("UPDATE commodore.api_tokens SET last_used_at").WithArgs("token-id").WillReturnResult(sqlmock.NewResult(1, 1))
				userRows := sqlmock.NewRows([]string{"email", "role"}).AddRow("user@example.com", "admin")
				mock.ExpectQuery("SELECT email, role FROM commodore.users").WithArgs("user-id").WillReturnRows(userRows)
			},
			assert: func(t *testing.T, resp *pb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !resp.Valid {
					t.Fatalf("expected valid response")
				}
				if resp.Email != "user@example.com" || resp.Role != "admin" {
					t.Fatalf("unexpected user details: %q %q", resp.Email, resp.Role)
				}
				if resp.TenantId != "tenant-id" || resp.UserId != "user-id" {
					t.Fatalf("unexpected ids: %q %q", resp.TenantId, resp.UserId)
				}
				if len(resp.Permissions) != 2 || strings.Join(resp.Permissions, ",") != strings.Join([]string{"read", "write"}, ",") {
					t.Fatalf("unexpected permissions: %v", resp.Permissions)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &CommodoreServer{db: db, logger: logrus.New()}
			resp, err := server.ValidateAPIToken(ctx, test.req)
			test.assert(t, resp, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}
