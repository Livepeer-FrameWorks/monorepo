package grpc

import (
	"context"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetSignedPolicyBundleIsRetiredWithoutProducerWork(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	resp, err := s.GetSignedPolicyBundle(context.Background(), &commodorepb.GetSignedPolicyBundleRequest{
		TenantId: "22222222-2222-2222-2222-222222222222",
		StreamId: "33333333-3333-3333-3333-333333333333",
	})
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v, want Unimplemented (err=%v)", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("retired RPC performed producer work: %v", err)
	}
}
