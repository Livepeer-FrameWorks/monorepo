package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

func TestResolveNodeFingerprint(t *testing.T) {
	t.Run("resolves machine id with active node mapping", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id"}).AddRow("tenant-1", "node-1"))
		mock.ExpectExec("UPDATE quartermaster.node_fingerprints").
			WithArgs("203.0.113.10", "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := server.ResolveNodeFingerprint(context.Background(), &pb.ResolveNodeFingerprintRequest{
			PeerIp:          "203.0.113.10",
			MachineIdSha256: ptrStrNF("machine-hash"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetTenantId() != "tenant-1" || resp.GetCanonicalNodeId() != "node-1" {
			t.Fatalf("unexpected response: %+v", resp)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("returns not found when mapping points to stale or inactive assignment", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-stale").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("198.51.100.4").
			WillReturnError(sql.ErrNoRows)

		_, err = server.ResolveNodeFingerprint(context.Background(), &pb.ResolveNodeFingerprintRequest{
			PeerIp:          "198.51.100.4",
			MachineIdSha256: ptrStrNF("machine-stale"),
		})
		if err == nil {
			t.Fatal("expected not found error")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.NotFound {
			t.Fatalf("expected NotFound, got %v", err)
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

func ptrStrNF(v string) *string { return &v }
