//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestSessionRetention_RealPG(t *testing.T) {
	testSessionRetention(t, startCommodoreRealPG(t))
}

func TestSessionRetention_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_session_retention", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testSessionRetention(t, db)
}

func testSessionRetention(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	s := &CommodoreServer{db: db, logger: logrus.New()}
	tenant, user := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC()
	old, recent, future := now.Add(-40*24*time.Hour), now.Add(-time.Hour), now.Add(24*time.Hour)
	addSession := func(owner, sid string, expiry time.Time, revoked any) sessionRetentionKey {
		t.Helper()
		var reason any
		if revoked != nil {
			reason = "user"
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions
(id,tenant_id,user_id,auth_method,client_id,created_at,idle_expires_at,absolute_expires_at,revoked_at,revoke_reason)
VALUES($1::uuid,$2,$3,'password','web',$4,$5,$6,$7,$8)`, sid, tenant, owner, old.Add(-24*time.Hour), expiry, future, revoked, reason); err != nil {
			t.Fatal(err)
		}
		return sessionRetentionKey{ID: sid, TenantID: tenant, UserID: owner}
	}
	addToken := func(owner string, sid any, expiry time.Time) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
(id,tenant_id,user_id,session_id,token_hash,created_at,expires_at)
VALUES($1::uuid,$2,$3,$4,$1::text,$5,$6)`, id, tenant, owner, sid, old.Add(-24*time.Hour), expiry); err != nil {
			t.Fatal(err)
		}
		return id
	}
	existsSession := func(key sessionRetentionKey, want bool) {
		t.Helper()
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3)`, key.TenantID, key.UserID, key.ID).Scan(&exists); err != nil || exists != want {
			t.Fatalf("session retained=%t want=%t err=%v", exists, want, err)
		}
	}
	ended := addSession(user, uuid.NewString(), old, nil)
	for i := 0; i < int(sessionRetentionBatch)+5; i++ {
		addToken(user, ended.ID, old)
	}
	if count, tokens, err := s.retainEndedSession(ctx, ended, sessionRetentionBatch); err != nil || tokens != sessionRetentionBatch || count != int64(sessionRetentionBatch) {
		t.Fatalf("family deletion exceeded/ignored batch: deleted=%d tokens=%d err=%v", count, tokens, err)
	}
	existsSession(ended, true)
	wrongOwner := ended
	wrongOwner.TenantID = uuid.NewString()
	if count, _, err := s.retainEndedSession(ctx, wrongOwner, sessionRetentionBatch); err != nil || count != 0 {
		t.Fatalf("cross-tenant retention changed data: deleted=%d err=%v", count, err)
	}
	if count, tokens, err := s.retainEndedSession(ctx, ended, sessionRetentionBatch); err != nil || count != 6 || tokens != 5 {
		t.Fatalf("remaining family not removed atomically: deleted=%d tokens=%d err=%v", count, tokens, err)
	}
	existsSession(ended, false)
	for _, key := range []sessionRetentionKey{
		addSession(user, uuid.NewString(), future, nil),
		addSession(user, uuid.NewString(), recent, nil),
		addSession(user, uuid.NewString(), future, recent),
	} {
		if count, _, err := s.retainEndedSession(ctx, key, sessionRetentionBatch); err != nil || count != 0 {
			t.Fatalf("fresh session removed: deleted=%d err=%v", count, err)
		}
		existsSession(key, true)
	}
	revoked := addSession(user, uuid.NewString(), future, old)
	if count, _, err := s.retainEndedSession(ctx, revoked, sessionRetentionBatch); err != nil || count != 1 {
		t.Fatalf("old revocation retained: deleted=%d err=%v", count, err)
	}
	legacyUser := uuid.NewString()
	protected := addSession(legacyUser, uuid.NewString(), old, nil)
	ancestor := addToken(legacyUser, protected.ID, future)
	rotation, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := rotation.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Errorf("rollback rotation: %v", rollbackErr)
		}
	}()
	if _, err = rotation.ExecContext(ctx, `SELECT id FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, tenant, legacyUser, ancestor); err != nil {
		t.Fatal(err)
	}
	// A mixed-version issuer owns this row, but has not committed its sid-less
	// successor. Cleanup must skip it and leave the parent age anchor intact.
	if count, _, sweepErr := s.retainEndedSession(ctx, protected, sessionRetentionBatch); sweepErr != nil || count != 0 {
		t.Fatalf("in-flight old rotation lost its anchor: deleted=%d err=%v", count, sweepErr)
	}
	head := uuid.NewString()
	if _, err = rotation.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at)
VALUES($1::uuid,$2,$3,$1::text,$4)`, head, tenant, legacyUser, future); err != nil {
		t.Fatal(err)
	}
	if _, err = rotation.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET revoked=true,replaced_by=$4 WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenant, legacyUser, ancestor, head); err != nil {
		t.Fatal(err)
	}
	if err = rotation.Commit(); err != nil {
		t.Fatal(err)
	}
	if count, _, sweepErr := s.retainEndedSession(ctx, protected, sessionRetentionBatch); sweepErr != nil || count != 0 {
		t.Fatalf("committed legacy successor lost original age: deleted=%d err=%v", count, sweepErr)
	}
	existsSession(protected, true)
	legacyAncestor := addToken(legacyUser, nil, old)
	legacyKey := sessionRetentionKey{ID: legacyAncestor, TenantID: tenant, UserID: legacyUser}
	if count, sweepErr := s.retainUnassignedToken(ctx, legacyKey); sweepErr != nil || count != 0 {
		t.Fatalf("live legacy family lost sid-less ancestor: deleted=%d err=%v", count, sweepErr)
	}
	orphanUser := uuid.NewString()
	orphan := sessionRetentionKey{ID: addToken(orphanUser, nil, old), TenantID: tenant, UserID: orphanUser}
	if count, sweepErr := s.retainUnassignedToken(ctx, orphan); sweepErr != nil || count != 1 {
		t.Fatalf("dead legacy owner retained: deleted=%d err=%v", count, sweepErr)
	}
	// The first protected candidate must not stop the next eligible owner.
	collectable := addSession(uuid.NewString(), uuid.NewString(), old.Add(time.Hour), nil)
	activeSession := addSession(user, uuid.NewString(), future, nil)
	staleRotation := addToken(user, activeSession.ID, old)
	liveRotation := addToken(user, activeSession.ID, future)
	var progress sessionRetentionProgress
	if _, err = s.sweepSessionRetention(ctx, &progress); err != nil {
		t.Fatal(err)
	}
	existsSession(collectable, false)
	existsSession(protected, true)
	existsSession(activeSession, true)
	for _, entry := range []struct {
		id   string
		keep bool
	}{{staleRotation, false}, {liveRotation, true}} {
		var retained bool
		if queryErr := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND id=$3)`, tenant, user, entry.id).Scan(&retained); queryErr != nil || retained != entry.keep {
			t.Fatalf("rotation retention on a live named session: retained=%t want=%t err=%v", retained, entry.keep, queryErr)
		}
	}
	if _, err = s.sweepSessionRetention(ctx, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Expired.ID != "" {
		t.Fatalf("keyset failed to wrap for deferred families: %+v", progress.Expired)
	}
}
