package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListNodes_ServiceAuthUsesAllActiveClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	serviceScope := `(?s)WHERE n\.cluster_id IN \(\s*SELECT c\.cluster_id FROM quartermaster\.infrastructure_clusters c\s*WHERE c\.is_active = true\s*\)`

	mock.ExpectQuery(serviceScope).
		WithArgs().
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(serviceScope).
		WithArgs(51).
		WillReturnRows(sqlmock.NewRows(nodeColumns).
			AddRow(newNodeRow("uuid-1", "node-1", "core-central-primary", "node-1", "core", "1.2.3.4")...))

	resp, err := server.ListNodes(ctx, &quartermasterpb.ListNodesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
	if got := resp.GetNodes()[0].GetClusterId(); got != "core-central-primary" {
		t.Fatalf("expected core-central-primary, got %s", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListNodes_AnonymousUsesPublicTopologyClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	publicScope := `(?s)WHERE n\.cluster_id IN \(\s*SELECT c\.cluster_id FROM quartermaster\.infrastructure_clusters c\s*WHERE c\.public_topology = true AND c\.is_active = true\s*\)`

	mock.ExpectQuery(publicScope).
		WithArgs().
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(publicScope).
		WithArgs(51).
		WillReturnRows(sqlmock.NewRows(nodeColumns).
			AddRow(newNodeRow("uuid-1", "node-1", "media-central-primary", "node-1", "edge", "1.2.3.4")...))

	resp, err := server.ListNodes(context.Background(), &quartermasterpb.ListNodesRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
	if got := resp.GetNodes()[0].GetClusterId(); got != "media-central-primary" {
		t.Fatalf("expected media-central-primary, got %s", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
