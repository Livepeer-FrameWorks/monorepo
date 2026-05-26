package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

func TestResolveArtifactPlaybackIDRetriesRetryablePostgresErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	retryable := &pq.Error{
		Code:    "40001",
		Message: "schema version mismatch for table x: expected 2, got 1",
	}
	mock.ExpectQuery("FROM commodore.clips").
		WithArgs("playback-1").
		WillReturnError(retryable)
	rows := sqlmock.NewRows([]string{
		"clip_hash", "internal_name", "tenant_id", "user_id", "stream_id", "origin_cluster_id", "requires_auth",
	}).AddRow("clip-hash", "clip-internal", "tenant-1", "user-1", "stream-1", "cluster-origin", false)
	mock.ExpectQuery("FROM commodore.clips").
		WithArgs("playback-1").
		WillReturnRows(rows)

	server := &CommodoreServer{db: db, dbMaxIdleConns: -1, logger: logrus.New()}
	resp, err := server.ResolveArtifactPlaybackID(context.Background(), &pb.ResolveArtifactPlaybackIDRequest{
		PlaybackId: "playback-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetFound() || resp.GetArtifactHash() != "clip-hash" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestResolveIdentifierRetriesStreamIDLookupOnRetryablePostgresErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	retryable := &pq.Error{
		Code:    "40001",
		Message: "schema version mismatch for table x: expected 2, got 1",
	}
	streamID := "11111111-1111-1111-1111-111111111111"
	mock.ExpectQuery("FROM commodore.streams WHERE id").
		WithArgs(streamID).
		WillReturnError(retryable)
	mock.ExpectQuery("FROM commodore.streams WHERE id").
		WithArgs(streamID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "user_id", "internal_name", "is_recording_enabled", "requires_auth",
		}).AddRow(streamID, "tenant-1", "user-1", "live+abc", true, false))

	server := &CommodoreServer{db: db, dbMaxIdleConns: -1, logger: logrus.New()}
	resp, err := server.ResolveIdentifier(context.Background(), &pb.ResolveIdentifierRequest{
		Identifier: streamID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetFound() || resp.GetIdentifierType() != "stream_id" || resp.GetStreamId() != streamID {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
