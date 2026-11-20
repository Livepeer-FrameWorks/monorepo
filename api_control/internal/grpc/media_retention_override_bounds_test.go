package grpc

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestStreamRetentionOverridesRejectAboveCap(t *testing.T) {
	for _, target := range []string{"dvr", "clip"} {
		t.Run(target, func(t *testing.T) {
			s, mock, done := newRetentionServer(t)
			defer done()
			mock.ExpectQuery(`SELECT id::text AS stream_id`).WithArgs("stream-1", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow("stream-1", nil, nil))
			req := &commodorepb.SetStreamRetentionOverridesRequest{StreamId: "stream-1"}
			if target == "dvr" {
				req.DvrRetentionDaysOverride = proto.Int32(31)
			} else {
				req.ClipRetentionDaysOverride = proto.Int32(31)
			}
			_, err := s.SetStreamRetentionOverrides(retentionAuthCtx("tenant-1"), req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("over-cap %s override = %v; want InvalidArgument before any write", target, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStreamRetentionZeroMapsToCap(t *testing.T) {
	s, mock, done := newRetentionServer(t)
	defer done()
	mock.ExpectQuery(`SELECT id::text AS stream_id`).WithArgs("stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow("stream-1", nil, nil))
	mock.ExpectQuery(`UPDATE commodore.streams`).WithArgs(true, int32(30), true, int32(30), "stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_retention_days_override", "clip_retention_days_override"}).AddRow(30, 30))
	resp, err := s.SetStreamRetentionOverrides(retentionAuthCtx("tenant-1"), &commodorepb.SetStreamRetentionOverridesRequest{
		StreamId: "stream-1", DvrRetentionDaysOverride: proto.Int32(0), ClipRetentionDaysOverride: proto.Int32(0),
	})
	if err != nil || resp.GetDvrRetentionDaysOverride() != 30 || resp.GetClipRetentionDaysOverride() != 30 {
		t.Fatalf("zero overrides = %v, %v; want tier cap 30", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
