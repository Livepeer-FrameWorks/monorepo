package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TestResolvePlaybackPolicyRequiresExactlyOneIdentifier pins the XOR guard:
// callers must supply exactly one of playback_id / internal_name. Both-empty
// and both-set are equally invalid and must fail before any DB access.
func TestResolvePlaybackPolicyRequiresExactlyOneIdentifier(t *testing.T) {
	s := &CommodoreServer{logger: logrus.New()} // nil db: guard runs first

	cases := []struct {
		name string
		req  *commodorepb.ResolvePlaybackPolicyRequest
	}{
		{"both_empty", &commodorepb.ResolvePlaybackPolicyRequest{}},
		{"both_set", &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "p1", InternalName: "n1"}},
		{"whitespace_only", &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ResolvePlaybackPolicy(context.Background(), tc.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("err = %v, want InvalidArgument", err)
			}
		})
	}
}

// TestResolvePlaybackPolicyPublicWhenNoPolicy pins that an asset with a NULL
// playback_policy resolves as "public" (the open-by-default contract).
func TestResolvePlaybackPolicyPublicWhenNoPolicy(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectQuery("FROM commodore.streams").
		WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_policy", "playback_webhook_secret_enc", "tenant_id"}).
			AddRow(nil, nil, "tenant-1"))

	s := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := s.ResolvePlaybackPolicy(context.Background(), &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "p1"})
	if err != nil {
		t.Fatalf("ResolvePlaybackPolicy: %v", err)
	}
	if resp.GetType() != "public" {
		t.Fatalf("type = %q, want public", resp.GetType())
	}
	if resp.GetTenantId() != "tenant-1" {
		t.Fatalf("tenant_id = %q, want tenant-1", resp.GetTenantId())
	}
}

// TestResolvePlaybackPolicyDecodeError pins that a corrupt stored policy fails
// closed with Internal rather than serving a half-parsed policy.
func TestResolvePlaybackPolicyDecodeError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectQuery("FROM commodore.streams").
		WithArgs("p1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_policy", "playback_webhook_secret_enc", "tenant_id"}).
			AddRow([]byte("{not-json"), nil, "tenant-1"))

	s := &CommodoreServer{db: db, logger: logrus.New()}
	_, err = s.ResolvePlaybackPolicy(context.Background(), &commodorepb.ResolvePlaybackPolicyRequest{PlaybackId: "p1"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("err = %v, want Internal", err)
	}
}
