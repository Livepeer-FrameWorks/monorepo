//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestAuthCodeRetention_RealPG(t *testing.T) {
	testAuthCodeRetention(t, startCommodoreRealPG(t))
}

func TestAuthCodeRetention_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_auth_code_retention", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthCodeRetention(t, db)
}

func testAuthCodeRetention(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	tenant, user := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	old, recent, future := now.Add(-48*time.Hour), now.Add(-time.Hour), now.Add(time.Hour)
	var expectedCodes []struct {
		id   string
		keep bool
	}
	for _, entry := range []struct {
		expiry   time.Time
		consumed any
		keep     bool
	}{
		{old, nil, false}, {future, old, false}, {future, nil, true}, {recent, nil, true},
	} {
		id := uuid.NewString()
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_authorization_codes
(id,tenant_id,user_id,client_id,code_hash,code_challenge,code_challenge_method,redirect_uri,expires_at,consumed_at)
VALUES($1::uuid,$2,$3,'tray-mac',$1::text,'challenge','S256','http://127.0.0.1/callback',$4,$5)`, id, tenant, user, entry.expiry, entry.consumed); err != nil {
			t.Fatal(err)
		}
		expectedCodes = append(expectedCodes, struct {
			id   string
			keep bool
		}{id, entry.keep})
	}
	ownedID := uuid.NewString()
	for i := 0; i < int(authCodeRetentionBatch)+2; i++ {
		id := uuid.NewString()
		var ownerTenant, ownerUser any
		if i == 0 {
			id = ownedID
			ownerTenant = tenant
			ownerUser = user
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_device_codes(id,tenant_id,user_id,client_id,device_code_hash,user_code,expires_at)
VALUES($1::uuid,$2,$3,'cli',$1::text,LEFT($1::text,32),$4)`, id, ownerTenant, ownerUser, old); err != nil {
			t.Fatal(err)
		}
	}
	freshID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_device_codes(id,client_id,device_code_hash,user_code,expires_at)
VALUES($1::uuid,'cli',$1::text,LEFT($1::text,32),$2)`, freshID, future); err != nil {
		t.Fatal(err)
	}
	queries := commodoredb.New(db)
	if changed, err := queries.DeleteRetainedDeviceCode(ctx, commodoredb.DeleteRetainedDeviceCodeParams{ID: ownedID, TenantID: sql.NullString{}}); err != nil || changed != 0 {
		t.Fatalf("unassigned scope deleted owned device code: count=%d err=%v", changed, err)
	}
	s := &CommodoreServer{db: db, logger: logrus.New()}
	first, err := s.sweepAuthCodeRetention(ctx)
	if err != nil || first != int64(authCodeRetentionBatch)+2 {
		t.Fatalf("bounded sweep deleted=%d want=%d err=%v", first, authCodeRetentionBatch+2, err)
	}
	second, err := s.sweepAuthCodeRetention(ctx)
	if err != nil || second != 2 {
		t.Fatalf("remaining batch deleted=%d want=2 err=%v", second, err)
	}
	third, err := s.sweepAuthCodeRetention(ctx)
	if err != nil || third != 0 {
		t.Fatalf("retention not idempotent: deleted=%d err=%v", third, err)
	}
	var exists bool
	if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.auth_device_codes WHERE id=$1 AND tenant_id IS NULL)`, freshID).Scan(&exists); err != nil || !exists {
		t.Fatalf("fresh device code removed: exists=%t err=%v", exists, err)
	}
	for _, entry := range expectedCodes {
		var retained bool
		if queryErr := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.auth_authorization_codes WHERE id=$1 AND tenant_id=$2)`, entry.id, tenant).Scan(&retained); queryErr != nil || retained != entry.keep {
			t.Errorf("authorization code retained=%t want=%t err=%v", retained, entry.keep, queryErr)
		}
	}

}
