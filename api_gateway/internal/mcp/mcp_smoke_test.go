package mcp

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/api_gateway/internal/resolvers"
)

// stubGRPCAddr starts an in-process gRPC server that answers every method with an
// empty response, so the real clients pointed at it return zero-value protos and
// MCP tool/resource handlers run their full bodies without a live backend.
func stubGRPCAddr(t *testing.T) string {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		_ = stream.RecvMsg(&emptypb.Empty{})
		return stream.SendMsg(&emptypb.Empty{})
	}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func stubClients(t *testing.T, addr string) *clients.ServiceClients {
	t.Helper()
	log := logging.NewLogger()
	const to = 5 * time.Second
	mk := func(err error) {
		if err != nil {
			t.Fatalf("build client: %v", err)
		}
	}
	commo, err := commodore.NewGRPCClient(commodore.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	purs, err := purser.NewGRPCClient(purser.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	qm, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	peri, err := periscope.NewGRPCClient(periscope.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	deck, err := deckhand.NewGRPCClient(deckhand.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	dlog, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{Target: addr, AllowInsecure: true, Source: "test", Timeout: to}, log)
	mk(err)
	nav, err := navclient.NewClient(navclient.Config{Addr: addr, AllowInsecure: true, Logger: log, Timeout: to})
	mk(err)
	sig, err := signalman.NewGRPCClient(signalman.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	skip, err := skipperclient.NewGRPCClient(skipperclient.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	mk(err)
	return &clients.ServiceClients{
		Commodore: commo, Purser: purs, Quartermaster: qm, Periscope: peri,
		Deckhand: deck, Decklog: dlog, Navigator: nav, Signalman: sig, Skipper: skip,
	}
}

func mcpAuthCtx() context.Context {
	user := &middleware.UserContext{
		UserID: "u1", TenantID: "t1", Email: "m@example.test", Role: "owner",
		Permissions: []string{"streams:read", "streams:write", "analytics:read", "billing:read", "billing:write", "infrastructure:read", "infrastructure:write"},
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, user.UserID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, user.TenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, user.Role)
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, user.Permissions)
	return ctx
}

// TestMCPSmokeSweep is the MCP analogue of the GraphQL smoke sweep: it stands up
// the real MCP server with stub-backed clients, lists every tool and resource,
// and invokes each. Coverage of the tool/resource handler bodies comes en masse;
// the assertion is that no handler crashes the server.
func TestMCPSmokeSweep(t *testing.T) {
	addr := stubGRPCAddr(t)
	sc := stubClients(t, addr)
	logger := logging.NewLogger()

	rl := middleware.NewRateLimiter(middleware.RateLimitConfig{Logger: logger})
	t.Cleanup(rl.Stop)
	srv, err := NewServer(Config{
		ServiceClients: sc,
		Resolver:       &resolvers.Resolver{Clients: sc, Logger: logger},
		Logger:         logger,
		JWTSecret:      []byte("test-secret"),
		RateLimiter:    rl,
		TenantCache:    middleware.NewTenantCache(sc.Quartermaster, logger),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := mcpAuthCtx()
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := srv.mcpServer.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	toolList, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(toolList.Tools) == 0 {
		t.Fatal("no tools registered")
	}
	calledTools := 0
	for _, tool := range toolList.Tools {
		if _, callErr := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}}); callErr != nil {
			// transport/protocol error (not a tool-level error result) — log, continue
			t.Logf("tool %q transport error: %v", tool.Name, callErr)
			continue
		}
		calledTools++
	}

	resList, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("list resources: %v", err)
	}
	readResources := 0
	for _, r := range resList.Resources {
		if _, readErr := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI}); readErr != nil {
			t.Logf("resource %q error: %v", r.URI, readErr)
			continue
		}
		readResources++
	}

	t.Logf("MCP smoke: %d/%d tools called, %d/%d resources read",
		calledTools, len(toolList.Tools), readResources, len(resList.Resources))
	if calledTools == 0 && readResources == 0 {
		t.Error("MCP smoke swept nothing")
	}
}
