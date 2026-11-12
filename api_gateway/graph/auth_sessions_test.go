package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type securityGraphBackend struct {
	commodore.Interface
	t     *testing.T
	calls atomic.Int32
}

func (b *securityGraphBackend) session() *commodorepb.AuthSession {
	now := timestamppb.Now()
	return &commodorepb.AuthSession{Id: "current", ClientId: "cli", AuthMethod: "device_code", DeviceLabel: "CLI laptop", LastIp: "203.0.113.47", CreatedAt: now, LastSeenAt: now, ExpiresAt: now, Current: true}
}

func (b *securityGraphBackend) ListSessions(ctx context.Context, page *commonpb.CursorPaginationRequest) (*commodorepb.ListSessionsResponse, error) {
	b.calls.Add(1)
	if ctxkeys.GetAuthType(ctx) != "jwt" || ctxkeys.GetTenantID(ctx) == "" || ctxkeys.GetUserID(ctx) == "" || page.GetFirst() != 1 {
		b.t.Errorf("lost verified owner or page size: %+v", page)
	}
	return &commodorepb.ListSessionsResponse{Sessions: []*commodorepb.AuthSession{b.session()}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: 7}}, nil
}

func (b *securityGraphBackend) RevokeSession(_ context.Context, id string) (*commodorepb.RevokeSessionResponse, error) {
	b.calls.Add(1)
	if id != "current" {
		b.t.Errorf("wrong session revoked: %q", id)
	}
	return &commodorepb.RevokeSessionResponse{Success: true, Current: true}, nil
}

func (b *securityGraphBackend) RevokeOtherSessions(context.Context) (*commodorepb.RevokeOtherSessionsResponse, error) {
	b.calls.Add(1)
	return &commodorepb.RevokeOtherSessionsResponse{RevokedCount: 3}, nil
}

func (b *securityGraphBackend) RenameSession(_ context.Context, id, label string) (*commodorepb.AuthSession, error) {
	b.calls.Add(1)
	if id != "current" || label != "New label" {
		b.t.Errorf("rename input changed: %q %q", id, label)
	}
	row := b.session()
	row.DeviceLabel = label
	return row, nil
}

func (b *securityGraphBackend) ChangePassword(_ context.Context, current, next string) (*commodorepb.ChangePasswordResponse, error) {
	b.calls.Add(1)
	if current != "old proof" || next != "new proof" {
		b.t.Error("password proof changed")
	}
	return &commodorepb.ChangePasswordResponse{Success: true, RevokedCount: 2}, nil
}

func TestAccountSecurityGraphQLHTTP(t *testing.T) {
	backend := &securityGraphBackend{t: t}
	srv := newRealPathTestServer(&clients.ServiceClients{Commodore: backend})
	srv.handler = securitySessionContextHandler(srv.handler)
	for _, tc := range []struct {
		query, want string
	}{
		{`{ authSessionsConnection(first:1) { totalCount nodes { id clientKind authMethod label ipAddress current } } }`, `"totalCount":7`},
		{`mutation { renameAuthSession(id:"current",label:"New label") { __typename ... on AuthSession { label } } }`, `"label":"New label"`},
		{`mutation { revokeOtherAuthSessions { __typename ... on RevokeOtherAuthSessionsPayload { revokedCount } } }`, `"revokedCount":3`},
		{`mutation { changePassword(currentPassword:"old proof",newPassword:"new proof") { __typename ... on ChangePasswordPayload { success revokedCount } } }`, `"revokedCount":2`},
		{`mutation { revokeAuthSession(id:"current") { __typename ... on RevokeAuthSessionPayload { success current } } }`, `"current":true`},
	} {
		response, code := tryExecuteRealPath(srv, tc.query, nil)
		if code != http.StatusOK || len(response.Errors) != 0 || !strings.Contains(string(response.Data), tc.want) {
			t.Fatalf("security HTTP response: status=%d data=%s errors=%+v", code, response.Data, response.Errors)
		}
		if strings.Contains(tc.query, "authSessionsConnection") {
			data := string(response.Data)
			if strings.Contains(data, "203.0.113.47") || !strings.Contains(data, `"ipAddress":"203.0.113.x"`) || !strings.Contains(data, `"clientKind":"CLI"`) || !strings.Contains(data, `"authMethod":"DEVICE_CODE"`) {
				t.Fatalf("session privacy/enum contract lost: %s", data)
			}
		}
	}
	if backend.calls.Load() != 5 {
		t.Fatalf("unexpected RPC count=%d", backend.calls.Load())
	}
	query := `mutation { revokeOtherAuthSessions { __typename ... on AuthError { message } } }`
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(realPathContext(context.Background()), ctxkeys.KeyAuthType, "api_token")
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"__typename":"AuthError"`) || backend.calls.Load() != 5 {
		t.Fatalf("programmatic credential reached security RPC: status=%d calls=%d body=%s", recorder.Code, backend.calls.Load(), recorder.Body.String())
	}
}

func securitySessionContextHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx := context.WithValue(req.Context(), ctxkeys.KeySessionID, "current")
		next.ServeHTTP(w, req.WithContext(ctx))
	})
}
