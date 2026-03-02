package grpc

import (
	"context"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateBootstrapToken_Success(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectExec(`INSERT INTO quartermaster\.bootstrap_tokens`).
		WithArgs(
			sqlmock.AnyArg(), // id
			"my-token",       // name
			sqlmock.AnyArg(), // token_hash
			sqlmock.AnyArg(), // token_prefix
			"service",        // kind
			nil,              // tenant_id
			nil,              // cluster_id
			nil,              // expected_ip
			nil,              // usage_limit
			sqlmock.AnyArg(), // expires_at
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Name: "my-token",
		Kind: "service",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetToken().GetName() != "my-token" {
		t.Fatalf("expected name=my-token, got %s", resp.GetToken().GetName())
	}
	if resp.GetToken().GetKind() != "service" {
		t.Fatalf("expected kind=service, got %s", resp.GetToken().GetKind())
	}
	if resp.GetToken().GetToken() == "" {
		t.Fatal("expected non-empty token value")
	}
	if resp.GetToken().GetUsageCount() != 0 {
		t.Fatalf("expected usage_count=0, got %d", resp.GetToken().GetUsageCount())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateBootstrapToken_MissingName(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Kind: "service",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateBootstrapToken_InvalidKind(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Name: "my-token",
		Kind: "invalid_kind",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateBootstrapToken_EdgeNodeRequiresTenantID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Name: "edge-token",
		Kind: "edge_node",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for edge_node without tenant_id, got %v", err)
	}
}

func TestCreateBootstrapToken_EdgeNodeWithTenantID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	tenantID := "tenant-1"
	mock.ExpectExec(`INSERT INTO quartermaster\.bootstrap_tokens`).
		WithArgs(
			sqlmock.AnyArg(), // id
			"edge-token",     // name
			sqlmock.AnyArg(), // token_hash
			sqlmock.AnyArg(), // token_prefix
			"edge_node",      // kind
			&tenantID,        // tenant_id
			nil,              // cluster_id
			nil,              // expected_ip
			nil,              // usage_limit
			sqlmock.AnyArg(), // expires_at
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Name:     "edge-token",
		Kind:     "edge_node",
		TenantId: &tenantID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetToken().GetKind() != "edge_node" {
		t.Fatalf("expected kind=edge_node, got %s", resp.GetToken().GetKind())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCreateBootstrapToken_TokenHasCorrectPrefix(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectExec(`INSERT INTO quartermaster\.bootstrap_tokens`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	resp, err := server.CreateBootstrapToken(context.Background(), &pb.CreateBootstrapTokenRequest{
		Name: "test-token",
		Kind: "infrastructure_node",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token := resp.GetToken().GetToken()
	if len(token) < 3 || token[:3] != "bt_" {
		t.Fatalf("expected token to start with 'bt_', got %q", token)
	}
}

func TestHashBootstrapToken(t *testing.T) {
	hash1 := hashBootstrapToken("my-token")
	hash2 := hashBootstrapToken("my-token")
	hash3 := hashBootstrapToken("different-token")

	if hash1 != hash2 {
		t.Fatal("same input should produce same hash")
	}
	if hash1 == hash3 {
		t.Fatal("different inputs should produce different hashes")
	}
	if len(hash1) != 64 {
		t.Fatalf("expected SHA-256 hex length 64, got %d", len(hash1))
	}
}

func TestTokenPrefix(t *testing.T) {
	prefix := tokenPrefix("bt_abcdefghijklmnop")
	if prefix != "bt_abcdefghi..." {
		t.Fatalf("expected 'bt_abcdefghi...' (first 12 chars + ellipsis), got %q", prefix)
	}

	short := tokenPrefix("abc")
	if short != "abc" {
		t.Fatalf("expected 'abc' for short token, got %q", short)
	}

	exact12 := tokenPrefix("123456789012")
	if exact12 != "123456789012" {
		t.Fatalf("expected exact 12-char string returned as-is, got %q", exact12)
	}
}
