//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestLegacySessionAdoptionEnforcesCap_RealPG(t *testing.T) {
	for _, count := range []int{110, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { testLegacySessionAdoptionCap(t, startCommodoreRealPG(t), count) })
	}
}
func TestLegacySessionAdoptionEnforcesCap_RealYugabyte(t *testing.T) {
	for _, count := range []int{110, 250} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db, ok := dockerpg.OpenSharedYugabyteBaseline(t, fmt.Sprintf("commodore_legacy_cap_%d", count), "commodore")
			if !ok {
				t.Skip("requires the shared Yugabyte contract fixture")
			}
			testLegacySessionAdoptionCap(t, db, count)
		})
	}
}

func testLegacySessionAdoptionCap(t *testing.T, db *sql.DB, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	tenant, user, head := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,verified,is_active,role) VALUES($1,$2,'legacy-cap@example.test',true,true,'owner')`, user, tenant); err != nil {
		t.Fatal(err)
	}
	// Later issuer timestamps can outlive a waiting refresh's transaction time.
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '5 minutes',NOW()+INTERVAL '1 day',NOW()+INTERVAL '2 days' FROM generate_series(1,$3)`, tenant, user, count); err != nil {
		t.Fatal(err)
	}
	original := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, head, tenant, user, hashToken("legacy-cap-refresh"), original, original.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db, logger: logrus.New(), runtimeSettings: refreshTokenTestSettings, tokenHasher: testTokenHasher(t)}
	response, err := server.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: "legacy-cap-refresh"})
	if err != nil {
		t.Fatalf("legacy refresh failed during cap adoption: %v", err)
	}
	var active int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>NOW()`, tenant, user).Scan(&active); err != nil || active != 100 {
		t.Fatalf("legacy adoption escaped the cap: active=%d err=%v", active, err)
	}
	claims, err := auth.ValidateInteractiveJWT(response.Token, server.settings().JWTSecret)
	if err != nil || claims.SessionID != head || claims.AuthTime != original.Unix() {
		t.Fatalf("legacy adoption changed original identity or age: claims=%+v err=%v", claims, err)
	}
	validity, err := server.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: response.Token})
	if err != nil || !validity.GetValid() {
		t.Fatalf("cap evicted the credential currently being adopted: response=%+v err=%v", validity, err)
	}
	if _, err = server.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: response.RefreshToken}); err != nil {
		t.Fatalf("adopted family did not remain refreshable: %v", err)
	}
}
