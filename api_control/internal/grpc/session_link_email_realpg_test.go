//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLinkEmailPreservesCurrentSession_RealPG(t *testing.T) {
	testLinkEmailPreservesCurrentSession(t, startCommodoreRealPG(t))
}

func TestLinkEmailPreservesCurrentSession_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_link_email", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testLinkEmailPreservesCurrentSession(t, db)
}

func testLinkEmailPreservesCurrentSession(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, failure := range []string{"", "domain_event_outbox", "account_email_outbox"} {
		t.Run("rollback_"+failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			user, tenant, foreignUser := uuid.NewString(), uuid.NewString(), uuid.NewString()
			for _, owner := range []string{user, foreignUser} {
				if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,is_active,verified,reset_token,reset_token_expires)
VALUES($1,$2,true,false,'outstanding-reset',NOW()+INTERVAL '1 hour')`, owner, tenant); err != nil {
					t.Fatal(err)
				}
			}
			s := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: refreshTokenTestSettings, accountEmailKickFn: func() {}}
			issue := func(owner string) *commodorepb.AuthResponse {
				t.Helper()
				var response *commodorepb.AuthResponse
				err := s.withEventTx(ctx, func(tx *sql.Tx) error {
					var err error
					response, err = s.issueUserSessionTx(ctx, tx, owner, tenant, SessionContext{AuthMethod: "wallet", ClientID: "web", DeviceLabel: "Wallet browser"})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			current, other, foreign := issue(user), issue(user), issue(foreignUser)
			claims, err := auth.ValidateInteractiveJWT(current.Token, s.settings().JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			caller := context.WithValue(ctxAs(user, tenant, "owner"), ctxkeys.KeyAuthType, "jwt")
			caller = context.WithValue(caller, ctxkeys.KeySessionID, claims.SessionID)
			legacy := "link-email-legacy-" + uuid.NewString()
			if _, err = db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at)
VALUES($1,$2,$3,$4,NOW()+INTERVAL '1 hour')`, uuid.NewString(), tenant, user, hashToken(legacy)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET revoked=true,rotated_at=NOW(),replaced_by=
(SELECT id FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$3)
WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$4`, tenant, user, hashToken(legacy), hashToken(current.RefreshToken)); err != nil {
				t.Fatal(err)
			}
			current.RefreshToken = legacy
			if failure != "" {
				if _, err = db.ExecContext(ctx, `ALTER TABLE commodore.`+failure+` RENAME TO link_email_unavailable_outbox`); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, restoreErr := db.ExecContext(ctx, `ALTER TABLE commodore.link_email_unavailable_outbox RENAME TO `+failure); restoreErr != nil {
						t.Errorf("restore %s: %v", failure, restoreErr)
					}
				}()
			}
			email := user + "@example.test"
			var response *commodorepb.LinkEmailResponse
			if failure == "" {
				type result struct {
					response *commodorepb.LinkEmailResponse
					err      error
					email    string
				}
				results := make(chan result, 2)
				start := make(chan struct{})
				for _, candidate := range []string{email, user + ".second@example.test"} {
					go func() {
						<-start
						callResponse, callErr := s.LinkEmail(caller, &commodorepb.LinkEmailRequest{Email: candidate, Password: "new-password"})
						results <- result{callResponse, callErr, candidate}
					}()
				}
				close(start)
				wins, conflicts := 0, 0
				for range 2 {
					entry := <-results
					if entry.err == nil {
						wins++
						response = entry.response
						email = entry.email
					} else if status.Code(entry.err) == codes.AlreadyExists {
						conflicts++
					} else {
						t.Fatalf("concurrent LinkEmail: %v", entry.err)
					}
				}
				if wins != 1 || conflicts != 1 {
					t.Fatalf("credential-link winners=%d conflicts=%d, want exactly one of each", wins, conflicts)
				}
			} else {
				response, err = s.LinkEmail(caller, &commodorepb.LinkEmailRequest{Email: email, Password: "new-password"})
			}
			if failure != "" {
				if status.Code(err) != codes.Unavailable {
					t.Fatalf("acknowledged incomplete credential/session transaction: response=%v err=%v", response, err)
				}
			} else if err != nil || !response.GetSuccess() {
				t.Fatalf("link email: response=%v err=%v", response, err)
			}
			for _, entry := range []struct {
				name, token string
				valid       bool
			}{
				{"current", current.Token, true}, {"other", other.Token, failure != ""}, {"foreign owner", foreign.Token, true},
			} {
				state, validationErr := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: entry.token})
				if validationErr != nil || state.GetValid() != entry.valid {
					t.Fatalf("%s access valid=%v want=%t err=%v", entry.name, state.GetValid(), entry.valid, validationErr)
				}
			}
			var savedEmail, savedHash, reset sql.NullString
			if err = db.QueryRowContext(ctx, `SELECT email,password_hash,reset_token FROM commodore.users WHERE tenant_id=$1 AND id=$2`, tenant, user).Scan(&savedEmail, &savedHash, &reset); err != nil {
				t.Fatal(err)
			}
			if failure != "" {
				if savedEmail.Valid || savedHash.Valid || !reset.Valid {
					t.Fatal("credentials committed without revocation and email obligations")
				}
				return
			}
			if savedEmail.String != email || !auth.CheckPassword("new-password", savedHash.String) || reset.Valid {
				t.Fatal("email/password/reset state did not commit together")
			}
			if _, err = s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: other.RefreshToken}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("other device refreshed after LinkEmail: %v", err)
			}
			if _, err = s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: current.RefreshToken}); err != nil {
				t.Fatalf("current legacy successor could not refresh: %v", err)
			}
			var queued int
			if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.account_email_outbox WHERE tenant_id=$1 AND user_id=$2 AND purpose='verification' AND status='pending'`, tenant, user).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("verification delivery not durable: count=%d err=%v", queued, err)
			}
		})
	}
}
