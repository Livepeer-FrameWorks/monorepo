//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSigningKeyRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	server := &CommodoreServer{db: db, logger: logrus.New()}
	const (
		tenantID = "10000000-0000-4000-8000-000000000001"
		otherID  = "10000000-0000-4000-8000-000000000002"
		userID   = "20000000-0000-4000-8000-000000000001"
	)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)

	first, err := server.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: "first"})
	if err != nil {
		t.Fatalf("create first signing key: %v", err)
	}
	second, err := server.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: "second"})
	if err != nil {
		t.Fatalf("create second signing key: %v", err)
	}
	if first.GetPrivateKeyPem() == "" || second.GetPrivateKeyPem() == "" || first.GetSigningKey().GetId() == second.GetSigningKey().GetId() {
		t.Fatalf("invalid create responses: first=%#v second=%#v", first, second)
	}

	got, err := server.GetSigningKey(ctx, &commodorepb.GetSigningKeyRequest{Id: first.GetSigningKey().GetId()})
	if err != nil || got.GetKid() != first.GetSigningKey().GetKid() || got.GetStatus() != "active" {
		t.Fatalf("get first key = %#v, err = %v", got, err)
	}
	otherCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	otherCtx = context.WithValue(otherCtx, ctxkeys.KeyTenantID, otherID)
	if _, err := server.GetSigningKey(otherCtx, &commodorepb.GetSigningKeyRequest{Id: first.GetSigningKey().GetId()}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant get code = %v, err = %v", status.Code(err), err)
	}

	page, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{Limit: 1})
	if err != nil || len(page.GetSigningKeys()) != 1 || page.GetNextAfterId() == "" {
		t.Fatalf("first list page = %#v, err = %v", page, err)
	}
	next, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{Limit: 1, AfterId: page.GetNextAfterId()})
	if err != nil || len(next.GetSigningKeys()) != 1 || next.GetSigningKeys()[0].GetId() == page.GetSigningKeys()[0].GetId() {
		t.Fatalf("second list page = %#v, err = %v", next, err)
	}
	active, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{StatusFilter: "active"})
	if err != nil || len(active.GetSigningKeys()) != 2 {
		t.Fatalf("active list = %#v, err = %v", active, err)
	}

	if _, err := server.RecordSigningKeyUse(context.Background(), &commodorepb.RecordSigningKeyUseRequest{
		TenantId: tenantID,
		Kid:      first.GetSigningKey().GetKid(),
	}); err != nil {
		t.Fatalf("record key use: %v", err)
	}
	used, err := server.GetSigningKey(ctx, &commodorepb.GetSigningKeyRequest{Id: first.GetSigningKey().GetId()})
	if err != nil || used.GetLastUsedAt() == "" {
		t.Fatalf("used key = %#v, err = %v", used, err)
	}

	revoked, err := server.RevokeSigningKey(ctx, &commodorepb.RevokeSigningKeyRequest{Id: first.GetSigningKey().GetId()})
	if err != nil || revoked.GetStatus() != "revoked" || revoked.GetRevokedAt() == "" {
		t.Fatalf("revoked key = %#v, err = %v", revoked, err)
	}
	revokedList, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{StatusFilter: "revoked"})
	if err != nil || len(revokedList.GetSigningKeys()) != 1 || revokedList.GetSigningKeys()[0].GetId() != revoked.GetId() {
		t.Fatalf("revoked list = %#v, err = %v", revokedList, err)
	}
	filteredAfter, err := server.ListSigningKeys(ctx, &commodorepb.ListSigningKeysRequest{
		StatusFilter: "active",
		AfterId:      page.GetNextAfterId(),
	})
	if err != nil {
		t.Fatalf("status+cursor list: %v", err)
	}
	for _, key := range filteredAfter.GetSigningKeys() {
		if key.GetStatus() != "active" {
			t.Fatalf("status+cursor returned non-active key: %#v", key)
		}
	}

	var auditCreates, auditRevokes int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE action = 'create'), COUNT(*) FILTER (WHERE action = 'revoke')
		FROM commodore.signing_key_audit WHERE tenant_id = $1::uuid
	`, tenantID).Scan(&auditCreates, &auditRevokes); err != nil {
		t.Fatal(err)
	}
	if auditCreates != 2 || auditRevokes != 1 {
		t.Fatalf("audit counts = create %d revoke %d", auditCreates, auditRevokes)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.signing_keys (tenant_id, kid, name, public_key_pem, status)
		SELECT $1::uuid, 'cap-' || n::text, 'cap', 'pem', 'active'
		FROM generate_series(1, 9) AS n
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.CreateSigningKey(ctx, &commodorepb.CreateSigningKeyRequest{Name: "over-cap"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("over-cap create code = %v, err = %v", status.Code(err), err)
	}
}
