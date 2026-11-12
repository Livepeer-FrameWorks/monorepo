//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestSecurityWalletListFiltersCurrentTenant_RealPG(t *testing.T) {
	testSecurityWalletTenant(t, startCommodoreRealPG(t))
}
func TestSecurityWalletListFiltersCurrentTenant_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_security_wallets", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testSecurityWalletTenant(t, db)
}
func testSecurityWalletTenant(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	user, tenant, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'wallet-list@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.wallet_identities(user_id,tenant_id,wallet_address,chain_type) VALUES($1,$2,'0xowned','ethereum'),($1,$3,'0xother-tenant','ethereum')`, user, tenant, other); err != nil {
		t.Fatal(err)
	}
	owner := context.WithValue(ctx, ctxkeys.KeyUserID, user)
	owner = context.WithValue(owner, ctxkeys.KeyTenantID, tenant)
	server := &CommodoreServer{db: db, logger: logrus.New()}
	result, err := server.ListWallets(owner, &commodorepb.ListWalletsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Wallets) != 1 || result.Wallets[0].WalletAddress != "0xowned" {
		t.Fatalf("security wallet list crossed tenant boundary: %+v", result.Wallets)
	}
}
