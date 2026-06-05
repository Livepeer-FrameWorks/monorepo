package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newRetentionServer(t *testing.T) (*FoghornGRPCServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	return NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil), mock, func() {
		_ = db.Close()
	}
}

func TestResolveRetentionUntilRejectsHorizonPastTierBound(t *testing.T) {
	server, mock, cleanup := newRetentionServer(t)
	defer cleanup()

	endedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT ended_at").
		WithArgs("artifact-a", "tenant-a", "dvr").
		WillReturnRows(sqlmock.NewRows([]string{"ended_at"}).AddRow(endedAt))

	_, err := server.resolveRetentionUntil(context.Background(), "tenant-a", "artifact-a", "dvr", &foghornpb.OverrideArtifactRetentionRequest{
		RetentionUntil:   timestamppb.New(endedAt.Add(31 * 24 * time.Hour)),
		MaxRetentionDays: 30,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestResolveRetentionUntilAnchorsDaysToEndedAt(t *testing.T) {
	server, mock, cleanup := newRetentionServer(t)
	defer cleanup()

	endedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT ended_at").
		WithArgs("artifact-a", "tenant-a", "clip").
		WillReturnRows(sqlmock.NewRows([]string{"ended_at"}).AddRow(endedAt))

	got, err := server.resolveRetentionUntil(context.Background(), "tenant-a", "artifact-a", "clip", &foghornpb.OverrideArtifactRetentionRequest{
		RetentionDays:   14,
		AnchorToEndedAt: true,
	})
	if err != nil {
		t.Fatalf("resolveRetentionUntil: %v", err)
	}
	want := endedAt.Add(14 * 24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
