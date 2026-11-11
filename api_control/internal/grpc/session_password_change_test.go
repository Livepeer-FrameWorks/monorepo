package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPasswordChangeRequiresInteractiveNamedSessionAndValidInput(t *testing.T) {
	for _, tc := range []struct {
		name, authType, sid, current, next string
		want                               codes.Code
	}{
		{"wallet bearer", "wallet", authSessionTestID, "old-password", "new-password", codes.PermissionDenied},
		{"API token", "api_token", authSessionTestID, "old-password", "new-password", codes.PermissionDenied},
		{"service", "service", authSessionTestID, "old-password", "new-password", codes.PermissionDenied},
		{"legacy session", "jwt", "", "old-password", "new-password", codes.FailedPrecondition},
		{"missing proof", "jwt", authSessionTestID, "", "new-password", codes.InvalidArgument},
		{"short password", "jwt", authSessionTestID, "old-password", "short", codes.InvalidArgument},
		{"bcrypt limit", "jwt", authSessionTestID, "old-password", strings.Repeat("x", 73), codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			ctx := context.WithValue(sessionContext(tc.sid), ctxkeys.KeyAuthType, tc.authType)
			if _, err := s.ChangePassword(ctx, &commodorepb.ChangePasswordRequest{CurrentPassword: tc.current, NewPassword: tc.next}); status.Code(err) != tc.want {
				t.Fatalf("password change: %v, want %s", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
