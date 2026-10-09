package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	presenceOwner      = "33333333-3333-4333-8333-000000000001"
	presenceSubscriber = "33333333-3333-4333-8333-000000000002"
)

const gatewayPresenceQuery = `SELECT c\.owner_tenant_id,[\s\S]*FROM quartermaster\.infrastructure_clusters c[\s\S]*tenant_cluster_access tca`

func gatewayPresenceServer(t *testing.T) (*QuartermasterServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil), mock
}

// A tenant that neither owns nor holds active access to a cluster is refused,
// and the refusal names no inventory.
func TestGetClusterGatewayPresenceRefusesANonSubscriber(t *testing.T) {
	server, mock := gatewayPresenceServer(t)
	mock.ExpectQuery(gatewayPresenceQuery).
		WithArgs("staging-media-eu", false, sql.NullString{String: "33333333-3333-4333-8333-000000000009", Valid: true}).
		WillReturnError(sql.ErrNoRows)

	_, err := server.GetClusterGatewayPresence(tenantCtx("33333333-3333-4333-8333-000000000009", "owner"), &quartermasterpb.GetClusterGatewayPresenceRequest{ClusterId: "staging-media-eu"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-subscriber: err = %v, want PermissionDenied", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A subscriber of a private cluster learns its owner and gateway presence,
// checked against its own tenant identity.
func TestGetClusterGatewayPresenceAnswersASubscriber(t *testing.T) {
	server, mock := gatewayPresenceServer(t)
	mock.ExpectQuery(gatewayPresenceQuery).
		WithArgs("staging-media-eu", false, sql.NullString{String: presenceSubscriber, Valid: true}).
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "has_livepeer_gateway"}).AddRow(presenceOwner, true))

	resp, err := server.GetClusterGatewayPresence(tenantCtx(presenceSubscriber, "member"), &quartermasterpb.GetClusterGatewayPresenceRequest{ClusterId: "staging-media-eu"})
	if err != nil {
		t.Fatalf("subscriber: %v", err)
	}
	if resp.GetClusterId() != "staging-media-eu" || resp.GetOwnerTenantId() != presenceOwner || !resp.GetHasLivepeerGateway() {
		t.Fatalf("presence = %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Callers without a tenant identity are refused before any read.
func TestGetClusterGatewayPresenceRequiresATenantCaller(t *testing.T) {
	server, mock := gatewayPresenceServer(t)
	for name, ctx := range map[string]context.Context{
		"anonymous":          context.Background(),
		"jwt without tenant": context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"),
	} {
		_, err := server.GetClusterGatewayPresence(ctx, &quartermasterpb.GetClusterGatewayPresenceRequest{ClusterId: "staging-media-eu"})
		if code := status.Code(err); code != codes.Unauthenticated {
			t.Fatalf("%s: err = %v, want Unauthenticated", name, err)
		}
	}
	if _, err := server.GetClusterGatewayPresence(tenantCtx(presenceSubscriber, "owner"), &quartermasterpb.GetClusterGatewayPresenceRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing cluster_id: err = %v, want InvalidArgument", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A service caller may ask about any cluster; a cluster that does not exist
// is NotFound for it.
func TestGetClusterGatewayPresenceServiceCallerSeesAnyCluster(t *testing.T) {
	server, mock := gatewayPresenceServer(t)
	mock.ExpectQuery(gatewayPresenceQuery).
		WithArgs("staging-media-eu", true, sql.NullString{}).
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "has_livepeer_gateway"}).AddRow(presenceOwner, false))
	mock.ExpectQuery(gatewayPresenceQuery).
		WithArgs("gone", true, sql.NullString{}).
		WillReturnError(sql.ErrNoRows)

	resp, err := server.GetClusterGatewayPresence(serviceCtx(), &quartermasterpb.GetClusterGatewayPresenceRequest{ClusterId: "staging-media-eu"})
	if err != nil || resp.GetHasLivepeerGateway() || resp.GetOwnerTenantId() != presenceOwner {
		t.Fatalf("service caller: resp=%+v err=%v", resp, err)
	}
	if _, err := server.GetClusterGatewayPresence(serviceCtx(), &quartermasterpb.GetClusterGatewayPresenceRequest{ClusterId: "gone"}); status.Code(err) != codes.NotFound {
		t.Fatalf("service caller, missing cluster: err = %v, want NotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
