package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/deckhand"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	navclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	skipperclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/skipper"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/sirupsen/logrus"

	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
)

// Fakes embed the client interface (nil), so they satisfy it automatically and
// any method we DON'T override panics on call — gqlgen's recover turns that into
// a field error, still covering the resolver's call site + error path. Methods we
// DO override (in realpath_canned_test.go) return canned protos so the resolver's
// response-mapping code runs too.
type fakeCommodore struct{ commodore.Interface }
type fakePurser struct{ purser.Interface }
type fakeQuartermaster struct{ quartermaster.Interface }
type fakePeriscope struct{ periscope.Interface }
type fakeDeckhand struct{ deckhand.Interface }
type fakeDecklog struct{ decklog.Interface }
type fakeNavigator struct{ navclient.Interface }
type fakeSignalman struct{ signalman.Interface }
type fakeSkipper struct{ skipperclient.Interface }

func fakeServiceClients() *clients.ServiceClients {
	return &clients.ServiceClients{
		Commodore:     &fakeCommodore{},
		Purser:        &fakePurser{},
		Quartermaster: &fakeQuartermaster{},
		Periscope:     &fakePeriscope{},
		Deckhand:      &fakeDeckhand{},
		Decklog:       &fakeDecklog{},
		Navigator:     &fakeNavigator{},
		Signalman:     &fakeSignalman{},
		Skipper:       &fakeSkipper{},
	}
}

// newRealPathTestServer mirrors newPlaygroundTestServer but injects fake clients,
// so resolvers run their REAL (non-demo) path against canned gRPC responses.
func newRealPathTestServer(sc *clients.ServiceClients) playgroundTestHarness {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	var complexity generated.ComplexityRoot
	SetupComplexity(&complexity)

	root := &Resolver{
		Resolver: &resolvers.Resolver{
			Clients: sc,
			Logger:  logger,
		},
	}
	executable := generated.NewExecutableSchema(generated.Config{
		Resolvers:  root,
		Complexity: complexity,
	})
	srv := handler.New(executable)
	srv.AddTransport(transport.POST{})
	srv.SetRecoverFunc(func(ctx context.Context, err any) error {
		return errRecovered
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithNodeHealthCache(ctx))
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		return next(resolvers.WithStoragePricingCache(ctx))
	})

	return playgroundTestHarness{handler: srv, schema: executable.Schema()}
}

var errRecovered = httpError("internal error: recovered")

type httpError string

func (e httpError) Error() string { return string(e) }

// realPathContext is an authenticated, NON-demo context: resolvers pass auth and
// tenant guards and then hit the fake clients.
func realPathContext(ctx context.Context) context.Context {
	user := &middleware.UserContext{
		UserID:   "user-realpath-1",
		TenantID: "tenant-realpath-1",
		Email:    "realpath@example.test",
		Role:     "owner",
		Permissions: []string{
			"streams:read", "streams:write", "analytics:read",
			"billing:read", "billing:write",
			"infrastructure:read", "infrastructure:write",
		},
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, user.UserID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, user.TenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyEmail, user.Email)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, user.Role)
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, user.Permissions)
	return ctx
}

// tryExecuteRealPath executes a query with the authenticated non-demo context.
func tryExecuteRealPath(srv playgroundTestHarness, query string, vars map[string]any) (gqlResponse, int) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return gqlResponse{}, 0
	}
	ctx := realPathContext(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/graphql", bytes.NewReader(body))
	ctx = context.WithValue(ctx, ctxkeys.KeyHTTPRequest, req)
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return gqlResponse{}, rec.Code
	}
	var resp gqlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return gqlResponse{}, rec.Code
	}
	return resp, rec.Code
}
