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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type securityActivityGraphBackend struct {
	periscope.Interface
	t     *testing.T
	calls atomic.Int32
}

func (b *securityActivityGraphBackend) GetSecurityActivity(ctx context.Context, page *commonpb.CursorPaginationRequest) (*periscopepb.GetSecurityActivityResponse, error) {
	b.calls.Add(1)
	if ctxkeys.GetAuthType(ctx) != "jwt" || ctxkeys.GetUserID(ctx) == "" || ctxkeys.GetTenantID(ctx) == "" || page.GetFirst() != 1 {
		b.t.Error("activity request lost its interactive owner or page size")
	}
	return &periscopepb.GetSecurityActivityResponse{Events: []*periscopepb.SecurityActivityEvent{{Id: "event-1", EventType: "user.sessions_revoked", Timestamp: timestamppb.Now(), Reason: "SESSION_REVOCATION_REASON_PASSWORD_RESET"}}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: 1}}, nil
}

func TestSecurityActivityGraphQLHTTP(t *testing.T) {
	backend := &securityActivityGraphBackend{t: t}
	srv := newRealPathTestServer(&clients.ServiceClients{Periscope: backend})
	query := `{ securityActivityConnection(first:1) { totalCount nodes { id kind timestamp authMethod sessionId reason } pageInfo { hasNextPage } } }`
	response, code := tryExecuteRealPath(srv, query, nil)
	if code != http.StatusOK || len(response.Errors) != 0 || !strings.Contains(string(response.Data), `"kind":"PASSWORD_RESET"`) || !strings.Contains(string(response.Data), `"authMethod":"UNKNOWN"`) {
		t.Fatalf("security activity HTTP: code=%d data=%s errors=%+v", code, response.Data, response.Errors)
	}
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(realPathContext(context.Background()), ctxkeys.KeyAuthType, "api_token")
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.handler.ServeHTTP(recorder, request)
	if backend.calls.Load() != 1 || !strings.Contains(recorder.Body.String(), "Interactive sign-in required") {
		t.Fatalf("non-interactive activity call reached Periscope: calls=%d body=%s", backend.calls.Load(), recorder.Body.String())
	}
}
