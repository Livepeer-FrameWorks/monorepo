package grpc

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// enrollmentOriginMatcher does regexp matching like the default sqlmock matcher,
// but additionally enforces the CreateNode provenance invariants on the upsert:
//   - the fresh INSERT stamps enrollment_origin = 'runtime_enrolled', and
//   - the ON CONFLICT DO UPDATE path never touches enrollment_origin (so an
//     existing gitops_seed / adopted_local row is not downgraded).
var enrollmentOriginMatcher = sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
	re, err := regexp.Compile(expectedSQL)
	if err != nil {
		return err
	}
	if !re.MatchString(actualSQL) {
		return fmt.Errorf("query %q does not match %q", actualSQL, expectedSQL)
	}
	if strings.Contains(actualSQL, "INSERT INTO quartermaster.infrastructure_nodes") {
		conflictIdx := strings.Index(actualSQL, "ON CONFLICT")
		insertPart := actualSQL
		if conflictIdx >= 0 {
			insertPart = actualSQL[:conflictIdx]
		}
		if !strings.Contains(insertPart, "'runtime_enrolled'") {
			return fmt.Errorf("fresh INSERT must stamp enrollment_origin='runtime_enrolled': %s", actualSQL)
		}
		if conflictIdx >= 0 && strings.Contains(actualSQL[conflictIdx:], "enrollment_origin") {
			return fmt.Errorf("ON CONFLICT clause must not modify enrollment_origin: %s", actualSQL[conflictIdx:])
		}
	}
	return nil
})

func TestCreateNode_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// Cluster existence check
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// INSERT ... ON CONFLICT (upsert)
	mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
		WithArgs(
			"node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil, // internal_ip, external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port
			nil, nil, // region, availability_zone
			nil, nil, // latitude, longitude
			nil, nil, nil, // cpu_cores, memory_gb, disk_gb
			sqlmock.AnyArg(), // now
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// queryNode re-read
	now := time.Now()
	mock.ExpectQuery(`SELECT n\.id, n\.node_id, n\.cluster_id, n\.node_name, n\.node_type`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
			"uuid-1", "node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, "runtime_enrolled", nil, "active", now, now,
			"tenant-1",
			nil, nil, nil, nil, nil, nil, nil,
		}...))

	resp, err := server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
		NodeId:    "node-1",
		ClusterId: "cluster-1",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetNode().GetNodeId() != "node-1" {
		t.Fatalf("expected node_id=node-1, got %s", resp.GetNode().GetNodeId())
	}
	if resp.GetNode().GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster_id=cluster-1, got %s", resp.GetNode().GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateNode_MissingNodeID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
		ClusterId: "cluster-1",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateNode_MissingClusterID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
		NodeId:   "node-1",
		NodeName: "my-node",
		NodeType: "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateNode_Idempotent(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	now := time.Now()
	extIP := "1.2.3.4"

	req := &quartermasterpb.CreateNodeRequest{
		NodeId:     "node-1",
		ClusterId:  "cluster-1",
		NodeName:   "my-node",
		NodeType:   "core",
		ExternalIp: &extIP,
	}

	// Two identical calls should both succeed via ON CONFLICT DO UPDATE.
	for i := range 2 {
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("cluster-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
			WithArgs(
				"node-1", "cluster-1", "my-node", "core",
				nil, &extIP, nil, nil, nil,
				nil, nil,
				nil, nil,
				nil, nil, nil,
				sqlmock.AnyArg(),
			).
			WillReturnResult(sqlmock.NewResult(0, 1))

		mock.ExpectQuery(`SELECT n\.id, n\.node_id, n\.cluster_id, n\.node_name, n\.node_type`).
			WithArgs("node-1").
			WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
				"uuid-1", "node-1", "cluster-1", "my-node", "core",
				nil, "1.2.3.4", nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
				nil, "gitops_seed", nil, "active", now, now,
				"tenant-1",
				nil, nil, nil, nil, nil, nil, nil,
			}...))

		resp, callErr := server.CreateNode(context.Background(), req)
		if callErr != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, callErr)
		}
		if resp.GetNode().GetNodeId() != "node-1" {
			t.Fatalf("call %d: expected node_id=node-1, got %s", i+1, resp.GetNode().GetNodeId())
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestCreateNode_StampsRuntimeEnrolled asserts a fresh CreateNode INSERT stamps
// enrollment_origin='runtime_enrolled' rather than falling to the schema DEFAULT
// 'gitops_seed'. Privateer auto-recovers a lost node row by calling CreateNode;
// the recreated row must be reconciler-protected, not bootstrap-movable.
func TestCreateNode_StampsRuntimeEnrolled(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(enrollmentOriginMatcher))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	now := time.Now()

	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	// The matcher enforces that this INSERT stamps 'runtime_enrolled' in the
	// VALUES list and leaves the ON CONFLICT clause's origin untouched.
	mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
		WithArgs(
			"node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil,
			nil, nil,
			nil, nil,
			nil, nil, nil,
			sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT n\.id, n\.node_id, n\.cluster_id, n\.node_name, n\.node_type`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
			"uuid-1", "node-1", "cluster-1", "my-node", "core",
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, "runtime_enrolled", nil, "active", now, now,
			"tenant-1",
			nil, nil, nil, nil, nil, nil, nil,
		}...))

	resp, err := server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
		NodeId:    "node-1",
		ClusterId: "cluster-1",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.GetNode().GetEnrollmentOrigin(); got != "runtime_enrolled" {
		t.Fatalf("expected enrollment_origin=runtime_enrolled, got %q", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestCreateNode_PreservesExistingOrigin asserts that re-running CreateNode on an
// existing node does not downgrade its enrollment_origin. The matcher guards that
// the ON CONFLICT DO UPDATE clause never assigns enrollment_origin, so a
// gitops_seed (and by the same clause an adopted_local) row keeps its origin.
func TestCreateNode_PreservesExistingOrigin(t *testing.T) {
	for _, origin := range []string{"gitops_seed", "adopted_local"} {
		t.Run(origin, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(enrollmentOriginMatcher))
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			now := time.Now()

			mock.ExpectQuery(`SELECT EXISTS`).
				WithArgs("cluster-1").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

			mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes`).
				WithArgs(
					"node-1", "cluster-1", "my-node", "core",
					nil, nil, nil, nil, nil,
					nil, nil,
					nil, nil,
					nil, nil, nil,
					sqlmock.AnyArg(),
				).
				WillReturnResult(sqlmock.NewResult(0, 1))

			// The conflict-update leaves origin untouched; the re-read reflects the
			// pre-existing origin value.
			mock.ExpectQuery(`SELECT n\.id, n\.node_id, n\.cluster_id, n\.node_name, n\.node_type`).
				WithArgs("node-1").
				WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow([]driver.Value{
					"uuid-1", "node-1", "cluster-1", "my-node", "core",
					nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
					nil, origin, nil, "active", now, now,
					"tenant-1",
					nil, nil, nil, nil, nil, nil, nil,
				}...))

			resp, err := server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
				NodeId:    "node-1",
				ClusterId: "cluster-1",
				NodeName:  "my-node",
				NodeType:  "core",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := resp.GetNode().GetEnrollmentOrigin(); got != origin {
				t.Fatalf("expected enrollment_origin preserved as %q, got %q", origin, got)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestCreateNode_ClusterNotFound(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("nonexistent-cluster").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = server.CreateNode(context.Background(), &quartermasterpb.CreateNodeRequest{
		NodeId:    "node-1",
		ClusterId: "nonexistent-cluster",
		NodeName:  "my-node",
		NodeType:  "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
