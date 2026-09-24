package grpc

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const jwtKeysTenant = "10000000-0000-0000-0000-000000000001"

func expectActiveSigningKeys(mock sqlmock.Sqlmock, tenantID string, kids ...string) {
	rows := sqlmock.NewRows([]string{"kid", "algorithm", "public_key_pem"})
	for _, kid := range kids {
		rows.AddRow(kid, "ES256", "public-key-"+kid)
	}
	mock.ExpectQuery("SELECT kid, algorithm, public_key_pem").WithArgs(tenantID).WillReturnRows(rows)
}

// A JWT policy no key can satisfy is refused before any write: the tenant must
// hold an active signing key and every allowed kid must name one.
func TestSetPlaybackPolicyJWTRequiresUsableSigningKey(t *testing.T) {
	cases := []struct {
		name       string
		activeKids []string
		allowed    []string
		wantMsg    string
	}{
		{"no active keys", nil, nil, "create a playback signing key first"},
		{"no active keys with allow-list", nil, []string{"kid-1"}, "create a playback signing key first"},
		{"unknown allowed kid", []string{"kid-1"}, []string{"kid-1", "kid-gone"}, "kid-gone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			expectActiveSigningKeys(mock, "t1", tc.activeKids...)

			_, err := s.SetPlaybackPolicy(ctxAs("u1", "t1", "owner"), &commodorepb.SetPlaybackPolicyRequest{
				StreamId: "stream-1",
				Type:     "jwt",
				Jwt:      &commodorepb.PlaybackJwtPolicy{AllowedKids: tc.allowed},
			})
			wantCode(t, err, codes.FailedPrecondition)
			if !strings.Contains(status.Convert(err).Message(), tc.wantMsg) {
				t.Fatalf("message = %q, want it to contain %q", status.Convert(err).Message(), tc.wantMsg)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet: %v", err)
			}
		})
	}
}

// With no usable key the compiler publishes a protected DENY policy instead
// of parking the authority; revoked kids are pruned from the allow-list and a
// list that loses all of its kids never widens to the remaining keys.
func TestCompileJWTPlaybackPolicyWithoutUsableKey(t *testing.T) {
	cases := []struct {
		name        string
		activeKids  []string
		policy      string
		wantKind    mediaauthoritypb.PlaybackPolicyKind
		wantAllowed []string
	}{
		{"no active keys", nil, `{"type":"jwt","jwt":{}}`, mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY, nil},
		{"every allowed kid revoked", []string{"kid-2"}, `{"type":"jwt","jwt":{"allowed_kids":["kid-1"]}}`, mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY, nil},
		{"revoked kid pruned", []string{"kid-2"}, `{"type":"jwt","jwt":{"allowed_kids":["kid-1","kid-2"]}}`, mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, []string{"kid-2"}},
		{"empty allow-list admits active keys", []string{"kid-2"}, `{"type":"jwt","jwt":{}}`, mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			expectActiveSigningKeys(mock, jwtKeysTenant, tc.activeKids...)

			policy, err := s.compilePlaybackPolicy(context.Background(), jwtKeysTenant, true, tc.policy)
			if err != nil {
				t.Fatalf("compilePlaybackPolicy: %v", err)
			}
			if policy.GetKind() != tc.wantKind {
				t.Fatalf("kind = %v, want %v", policy.GetKind(), tc.wantKind)
			}
			if !equalStrings(policy.GetJwt().GetAllowedKeyIds(), tc.wantAllowed) {
				t.Fatalf("allowed kids = %v, want %v", policy.GetJwt().GetAllowedKeyIds(), tc.wantAllowed)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("unmet: %v", err)
			}
		})
	}
}
