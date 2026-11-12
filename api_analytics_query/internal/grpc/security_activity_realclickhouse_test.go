//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/google/uuid"
)

func verifySecurityActivityProjection(t *testing.T, ctx context.Context, db *sql.DB, server *PeriscopeServer) {
	t.Helper()
	tenant, owner, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ownLogin, ownRevoke := uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, row := range []struct {
		id, tenant, subject, actor, kind string
		at                               time.Time
	}{
		{ownLogin, tenant, owner, "", "auth_login_succeeded", now.Add(-time.Hour)},
		{ownLogin, tenant, owner, "", "auth_login_succeeded", now.Add(-time.Hour)},
		{ownRevoke, tenant, owner, other, "auth_session.revoked", now.Add(-2 * time.Hour)},
		{uuid.NewString(), tenant, other, owner, "auth_login_succeeded", now.Add(-time.Minute)},
		{uuid.NewString(), uuid.NewString(), owner, owner, "auth_login_succeeded", now.Add(-time.Minute)},
		{uuid.NewString(), tenant, owner, owner, "auth_login_failed", now.Add(-91 * 24 * time.Hour)},
		{uuid.NewString(), tenant, owner, owner, "auth_login_failed", now.Add(time.Hour)},
		{uuid.NewString(), tenant, owner, owner, "stream.created", now.Add(-time.Minute)},
	} {
		payload, _ := json.Marshal(map[string]string{"user_id": row.subject, "auth_type": "password", "session_id": ownRevoke, "reason": "SESSION_REVOCATION_REASON_LOGOUT", "error": "private-detail-not-for-API"})
		var actor any
		if row.actor != "" {
			actor = row.actor
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO periscope.api_events(event_id,tenant_id,event_type,source,user_id,resource_type,resource_id,details,timestamp)
VALUES(?,?,?,'commodore',?,'user',?,?,?)`, row.id, row.tenant, row.kind, actor, row.subject, string(payload), row.at); err != nil {
			t.Fatal(err)
		}
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, owner)
	first, err := server.GetSecurityActivity(ctx, &periscopepb.GetSecurityActivityRequest{Pagination: &commonpb.CursorPaginationRequest{First: 1}})
	if err != nil || first.GetPagination().GetTotalCount() != 2 || len(first.GetEvents()) != 1 || first.Events[0].Id != ownLogin || !first.GetPagination().GetHasNextPage() {
		t.Fatalf("own subject activity lost, duplicated or leaked: response=%+v err=%v", first, err)
	}
	after := first.GetPagination().GetEndCursor()
	second, err := server.GetSecurityActivity(ctx, &periscopepb.GetSecurityActivityRequest{Pagination: &commonpb.CursorPaginationRequest{First: 1, After: &after}})
	if err != nil || len(second.GetEvents()) != 1 || second.Events[0].Id != ownRevoke || second.GetPagination().GetHasNextPage() {
		t.Fatalf("subject cursor page: response=%+v err=%v", second, err)
	}
	if second.Events[0].GetReason() != "SESSION_REVOCATION_REASON_LOGOUT" || second.Events[0].GetSessionId() != ownRevoke {
		t.Fatal("typed activity metadata was not decoded")
	}
}
