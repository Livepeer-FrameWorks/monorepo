package quartermasterdb

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEdgeStateQueriesOnlyUseCanonicalCapabilityRows(t *testing.T) {
	matcher := sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if expectedSQL != "canonical edge capability" {
			return nil
		}
		if !strings.Contains(actualSQL, "si.instance_id = 'edge-cap-' || si.node_id || '-' || svc.type") {
			return fmt.Errorf("query is not scoped to the canonical edge capability row: %s", actualSQL)
		}
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	queries := New(db)

	mock.ExpectQuery("canonical edge capability").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "type", "cluster_id", "health_status"}))
	if _, err := queries.ListPriorEdgeInstanceStates(context.Background(), []string{"edge-eu-1"}); err != nil {
		t.Fatalf("ListPriorEdgeInstanceStates: %v", err)
	}

	mock.ExpectExec("canonical edge capability").
		WithArgs("edge-ingest", "edge-eu-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := queries.MarkEdgeInstanceUnhealthy(context.Background(), MarkEdgeInstanceUnhealthyParams{
		ServiceType: "edge-ingest",
		NodeID:      "edge-eu-1",
	}); err != nil {
		t.Fatalf("MarkEdgeInstanceUnhealthy: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
