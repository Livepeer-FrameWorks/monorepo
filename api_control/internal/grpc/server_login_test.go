package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
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
				"role", "permissions", "is_active", "verified", "created_at", "updated_at",
			}).AddRow(
				"user-1", "tenant-1", "user@example.com", hashedPassword,
				sql.NullString{}, sql.NullString{}, "owner", pq.StringArray{"streams:read"},
				true, false, now, now,
			)
			mock.ExpectQuery("FROM commodore.users WHERE email = \\$1").
				WithArgs("user@example.com").
				WillReturnRows(rows)

			server := &CommodoreServer{db: db, logger: logrus.New()}
			_, err = server.Login(context.Background(), &pb.LoginRequest{
				Email:    "user@example.com",
				Password: tt.password,
				Behavior: &pb.BehaviorData{
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
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}
