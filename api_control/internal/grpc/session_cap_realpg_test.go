//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthSessionCapBatches_RealPG(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		name := "ordinary"
		if interrupt {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) { testAuthSessionCapBatches(t, startCommodoreRealPG(t), interrupt, true) })
	}
}
func TestAuthSessionCapBatches_RealYugabyte(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		name := "ordinary"
		if interrupt {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_cap_"+name, "commodore")
			if !ok {
				t.Skip("requires the shared Yugabyte contract fixture")
			}
			testAuthSessionCapBatches(t, db, interrupt, false)
		})
	}
}

func testAuthSessionCapBatches(t *testing.T, db *sql.DB, interrupt, probe bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	tenant, user, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,verified,is_active,role) VALUES($1,$2,'cap-owner@example.test',true,true,'owner'),($3,$2,'cap-other@example.test',true,true,'member')`, user, tenant, other); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct {
		id    string
		count int
	}{{user, 250}, {other, 5}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,device_label,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown','Legacy sign-in',NOW()-INTERVAL '2 hours'+n*INTERVAL '1 second',NOW()-INTERVAL '2 hours'+n*INTERVAL '1 second',NOW()+INTERVAL '1 day',NOW()+INTERVAL '2 days' FROM generate_series(1,$3) AS n`, tenant, owner.id, owner.count); err != nil {
			t.Fatal(err)
		}
	}
	if probe {
		if _, err := db.ExecContext(ctx, `CREATE TABLE commodore.cap_eviction_probe(tx BIGINT PRIMARY KEY,n INTEGER NOT NULL);
CREATE FUNCTION commodore.observe_cap_eviction() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE changed INTEGER;
BEGIN
 IF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL THEN
  INSERT INTO commodore.cap_eviction_probe(tx,n) VALUES(txid_current(),1)
  ON CONFLICT(tx) DO UPDATE SET n=commodore.cap_eviction_probe.n+1 RETURNING n INTO changed;
  IF changed>20 THEN RAISE EXCEPTION 'cap eviction batch exceeded 20'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER observe_cap_eviction AFTER UPDATE ON commodore.auth_sessions FOR EACH ROW EXECUTE FUNCTION commodore.observe_cap_eviction();`); err != nil {
			t.Fatal(err)
		}
	}
	settings := refreshTokenTestSettings()
	settings.Branding = config.EmailBranding{WebAppURL: "https://console.example.com"}
	server := &CommodoreServer{db: db, logger: logrus.New(), runtimeSettings: func() RuntimeSettings { return settings }, tokenHasher: testTokenHasher(t)}
	ownerCtx := context.WithValue(ctx, ctxkeys.KeyUserID, user)
	ownerCtx = context.WithValue(ownerCtx, ctxkeys.KeyTenantID, tenant)
	ownerCtx = context.WithValue(ownerCtx, ctxkeys.KeyAuthType, "jwt")
	const verifier = "cap-normalization-pkce-verifier-with-sufficient-entropy"
	challenge := sha256.Sum256([]byte(verifier))
	granted, err := server.CompleteAuthorization(ownerCtx, &commodorepb.CompleteAuthorizationRequest{ClientId: "tray-mac", RedirectUri: "http://127.0.0.1:45678/callback", CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), CodeChallengeMethod: "S256", Scope: "account", State: "cap-proof"})
	if err != nil {
		t.Fatal(err)
	}
	request := &commodorepb.ExchangeAuthorizationCodeRequest{Code: granted.GetCode(), CodeVerifier: verifier, ClientId: "tray-mac", RedirectUri: "http://127.0.0.1:45678/callback"}
	active := func(owner string) int {
		t.Helper()
		var count int
		if queryErr := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>NOW()`, tenant, owner).Scan(&count); queryErr != nil {
			t.Fatal(queryErr)
		}
		return count
	}
	if interrupt {
		stoppedCtx, stop := context.WithCancel(ctx)
		server.domainEventsCommitted = stop
		_, err = server.ExchangeAuthorizationCode(stoppedCtx, request)
		stop()
		server.domainEventsCommitted = nil
		if err == nil || active(user) != 230 {
			t.Fatalf("interrupted cleanup must commit only one bounded step and leave sign-in retryable: active=%d err=%v", active(user), err)
		}
		var consumed bool
		if queryErr := db.QueryRowContext(ctx, `SELECT consumed_at IS NOT NULL FROM commodore.auth_authorization_codes WHERE tenant_id=$1 AND user_id=$2 AND code_hash=$3`, tenant, user, hashToken(granted.Code)).Scan(&consumed); queryErr != nil || consumed {
			t.Fatalf("interrupted cleanup consumed the grant: consumed=%t err=%v", consumed, queryErr)
		}
	}
	signedIn, err := server.ExchangeAuthorizationCode(ctx, request)
	if err != nil {
		t.Fatalf("large legacy owner must finish sign-in with bounded eviction transactions: %v", err)
	}
	if active(user) != 100 || active(other) != 5 {
		t.Fatalf("cap or ownership violated: owner=%d other=%d", active(user), active(other))
	}
	claims, err := auth.ValidateInteractiveJWT(signedIn.Token, settings.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if validity, validateErr := server.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: signedIn.Token}); validateErr != nil || !validity.GetValid() || claims.SessionID == "" {
		t.Fatalf("new session was evicted: response=%+v err=%v", validity, validateErr)
	}
	if _, err = server.ExchangeAuthorizationCode(ctx, request); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("successful sign-in grant was reused: %v", err)
	}
	if probe {
		var largest, total, batches int
		if err = db.QueryRowContext(ctx, `SELECT max(n),sum(n),count(*) FROM commodore.cap_eviction_probe`).Scan(&largest, &total, &batches); err != nil || largest > 20 || total != 151 || batches < 2 {
			t.Fatalf("eviction transaction budget: largest=%d total=%d batches=%d err=%v", largest, total, batches, err)
		}
	}
	if !interrupt {
		if _, err = db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown',NOW()-INTERVAL '3 hours',NOW()-INTERVAL '3 hours',NOW()+INTERVAL '1 day',NOW()+INTERVAL '2 days' FROM generate_series(1,151)`, tenant, user); err != nil {
			t.Fatal(err)
		}
		walletAddress := "0x" + strings.ReplaceAll(user, "-", "") + "00000000"
		if _, err = db.ExecContext(ctx, `INSERT INTO commodore.wallet_identities(user_id,tenant_id,wallet_address,chain_type) VALUES($1,$2,$3,'ethereum')`, user, tenant, walletAddress); err != nil {
			t.Fatal(err)
		}
		var walletSession *commodorepb.AuthResponse
		err = server.withEventTx(ctx, func(tx *sql.Tx) error {
			var issueErr error
			walletSession, issueErr = server.issueUserSessionTx(ctx, tx, user, tenant, SessionContext{AuthMethod: "wallet", walletAddress: walletAddress, ClientID: "web", DeviceLabel: "Wallet browser"})
			return issueErr
		})
		if err != nil || walletSession == nil || active(user) != 100 || active(other) != 5 {
			t.Fatalf("event transaction cap recovery: active=%d err=%v", active(user), err)
		}
		if validity, validateErr := server.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: signedIn.Token}); validateErr != nil || !validity.GetValid() {
			t.Fatalf("housekeeping evicted a newer native sign-in: response=%+v err=%v", validity, validateErr)
		}
		if probe {
			var largest, total int
			if err = db.QueryRowContext(ctx, `SELECT max(n),sum(n) FROM commodore.cap_eviction_probe`).Scan(&largest, &total); err != nil || largest > 20 || total != 303 {
				t.Fatalf("event transaction eviction budget: largest=%d total=%d err=%v", largest, total, err)
			}
		}
	}

}
