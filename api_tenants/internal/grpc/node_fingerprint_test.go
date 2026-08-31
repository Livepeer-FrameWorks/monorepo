package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestResolveNodeFingerprint(t *testing.T) {
	t.Run("refuses an ambiguous durable fingerprint without falling back", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-clone").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id", "public_key"}).
				AddRow("tenant-1", "node-1", testNodeIdentityPublicKey()).
				AddRow("tenant-1", "node-2", testNodeIdentityPublicKey()))

		_, err = server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{
			PeerIp:          "203.0.113.10",
			MachineIdSha256: strPtr("machine-clone"),
			MacsSha256:      strPtr("macs-must-not-be-used"),
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("ambiguous fingerprint code = %s, want PermissionDenied", status.Code(err))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("atomically binds the first proved key to an existing stable fingerprint", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		publicKey := testNodeIdentityPublicKey()
		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id", "public_key"}).AddRow("tenant-1", "node-1", nil))
		mock.ExpectQuery("UPDATE quartermaster.node_fingerprints").
			WithArgs("node-1", publicKey).
			WillReturnRows(sqlmock.NewRows([]string{"node_identity_public_key_ed25519"}).AddRow(publicKey))
		mock.ExpectExec("UPDATE quartermaster.node_fingerprints").
			WithArgs("203.0.113.10", "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{
			PeerIp: "203.0.113.10", MachineIdSha256: strPtr("machine-hash"),
			NodeIdentityPublicKeyEd25519: publicKey,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(resp.GetNodeIdentityPublicKeyEd25519()) != string(publicKey) {
			t.Fatal("response did not return the atomically bound node key")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("refuses to replace an existing fingerprint key", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		presented := testNodeIdentityPublicKey()
		existing := append([]byte(nil), presented...)
		existing[0] ^= 0xff
		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id", "public_key"}).AddRow("tenant-1", "node-1", existing))
		mock.ExpectQuery("UPDATE quartermaster.node_fingerprints").
			WithArgs("node-1", presented).
			WillReturnRows(sqlmock.NewRows([]string{"node_identity_public_key_ed25519"}).AddRow(existing))

		_, err = server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{
			PeerIp: "203.0.113.10", MachineIdSha256: strPtr("machine-hash"),
			NodeIdentityPublicKeyEd25519: presented,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("key replacement code = %s, want PermissionDenied", status.Code(err))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("resolves machine id with active node mapping", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("machine-hash").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id", "public_key"}).AddRow("tenant-1", "node-1", testNodeIdentityPublicKey()))
		mock.ExpectExec("UPDATE quartermaster.node_fingerprints").
			WithArgs("203.0.113.10", "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{
			PeerIp:          "203.0.113.10",
			MachineIdSha256: strPtr("machine-hash"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetTenantId() != "tenant-1" || resp.GetCanonicalNodeId() != "node-1" {
			t.Fatalf("unexpected response: %+v", resp)
		}
		if resp.GetMatchSource() != quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID {
			t.Fatalf("expected machine-id match provenance, got %s", resp.GetMatchSource())
		}

		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("labels peer IP fallback as non-durable provenance", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		defer db.Close()

		server := &QuartermasterServer{db: db, logger: logrus.New()}
		mock.ExpectQuery("FROM quartermaster.node_fingerprints nf").
			WithArgs("203.0.113.11").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "node_id", "public_key"}).AddRow("tenant-1", "node-1", testNodeIdentityPublicKey()))
		mock.ExpectExec("UPDATE quartermaster.node_fingerprints").
			WithArgs("203.0.113.11", "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{PeerIp: "203.0.113.11"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetMatchSource() != quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_PEER_IP {
			t.Fatalf("expected peer-IP match provenance, got %s", resp.GetMatchSource())
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

		_, err = server.ResolveNodeFingerprint(context.Background(), &quartermasterpb.ResolveNodeFingerprintRequest{
			PeerIp:          "198.51.100.4",
			MachineIdSha256: strPtr("machine-stale"),
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
