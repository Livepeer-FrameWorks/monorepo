package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestRecoveryInventoryRejectsMalformedPages(t *testing.T) {
	item := func(id string, version int64) *commodorepb.HeldMediaAuthority {
		return &commodorepb.HeldMediaAuthority{AuthorityKind: "tenant", AuthorityId: id, AuthorityVersion: version}
	}
	for _, test := range []struct {
		name       string
		edit       func(*commodorepb.RequestMediaAuthorityReplayRequest)
		readsClock bool
	}{
		{"missing watermark", func(r *commodorepb.RequestMediaAuthorityReplayRequest) { r.AcknowledgedBefore = nil }, false},
		{"incomplete cursor", func(r *commodorepb.RequestMediaAuthorityReplayRequest) { r.AfterKind = "tenant" }, false},
		{"unknown cursor kind", func(r *commodorepb.RequestMediaAuthorityReplayRequest) { r.AfterKind, r.AfterId = "session", "x" }, false},
		{"empty nonfinal page", func(r *commodorepb.RequestMediaAuthorityReplayRequest) { r.FinalPage = false }, true},
		{"oversized page", func(r *commodorepb.RequestMediaAuthorityReplayRequest) {
			r.Held = make([]*commodorepb.HeldMediaAuthority, sharedauthority.RecoveryPageSize+1)
		}, true},
		{"duplicate identity", func(r *commodorepb.RequestMediaAuthorityReplayRequest) {
			r.Held = []*commodorepb.HeldMediaAuthority{item("a", 1), item("a", 1)}
		}, true},
		{"unordered identity", func(r *commodorepb.RequestMediaAuthorityReplayRequest) {
			r.Held = []*commodorepb.HeldMediaAuthority{item("b", 1), item("a", 1)}
		}, true},
		{"zero version", func(r *commodorepb.RequestMediaAuthorityReplayRequest) {
			r.Held = []*commodorepb.HeldMediaAuthority{item("a", 0)}
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			now := time.Now().UTC()
			request := &commodorepb.RequestMediaAuthorityReplayRequest{ControlCellId: "cell-a", InventoryPage: true, FinalPage: true, AsOf: timestamppb.New(now), AcknowledgedBefore: timestamppb.New(now.Add(-time.Second))}
			test.edit(request)
			if test.readsClock {
				mock.ExpectQuery("MediaAuthorityRecoveryTime").WillReturnRows(sqlmock.NewRows([]string{"observed_at"}).AddRow(now))
			}
			_, err := server.RequestMediaAuthorityReplay(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("malformed inventory: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
