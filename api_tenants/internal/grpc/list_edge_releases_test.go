package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListEdgeReleasesNormalizesChannelFilter(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	publishedAt := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`SELECT channel, version, components::text, published_at\s+FROM quartermaster\.edge_releases\s+WHERE TRUE AND channel = \$1`).
		WithArgs("rc").
		WillReturnRows(sqlmock.NewRows([]string{"channel", "version", "components", "published_at"}).
			AddRow("rc", "v1.2.3", `{}`, publishedAt))

	resp, err := server.ListEdgeReleases(context.Background(), &pb.ListEdgeReleasesRequest{Channel: " RC "})
	if err != nil {
		t.Fatalf("ListEdgeReleases: %v", err)
	}
	if got := resp.GetReleases()[0].GetChannel(); got != "rc" {
		t.Fatalf("channel = %q, want rc", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
