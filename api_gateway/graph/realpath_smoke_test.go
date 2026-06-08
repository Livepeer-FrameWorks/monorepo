package graph

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"frameworks/api_gateway/internal/clients"
)

// startStubGRPC stands up an in-process gRPC server whose catch-all handler
// answers EVERY method with an empty response. An empty proto marshals to zero
// bytes, which any client unmarshals into a zero-value (non-nil) response — so
// real clients pointed here never error at the transport and every resolver runs
// its full request-build → call → response-mapping path.
func startStubGRPC(t *testing.T) string {
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

// startStubGRPCError is like startStubGRPC but fails every RPC, so resolvers run
// their error-handling branches.
func startStubGRPCError(t *testing.T) string {
	t.Helper()
	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		_ = stream.RecvMsg(&emptypb.Empty{})
		return status.Error(codes.Unavailable, "stub backend error")
	}))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// realServiceClients builds the actual gRPC clients pointed at the stub server.
func realServiceClients(t *testing.T, addr string) *clients.ServiceClients {
	t.Helper()
	log := logging.NewLogger()
	const to = 5 * time.Second

	commo, err := commodore.NewGRPCClient(commodore.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	purs, err := purser.NewGRPCClient(purser.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	qm, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	peri, err := periscope.NewGRPCClient(periscope.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	deck, err := deckhand.NewGRPCClient(deckhand.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	dlog, err := decklog.NewBatchedClient(decklog.BatchedClientConfig{Target: addr, AllowInsecure: true, Source: "test", Timeout: to}, log)
	must(t, err)
	nav, err := navclient.NewClient(navclient.Config{Addr: addr, AllowInsecure: true, Logger: log, Timeout: to})
	must(t, err)
	sig, err := signalman.NewGRPCClient(signalman.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)
	skip, err := skipperclient.NewGRPCClient(skipperclient.GRPCConfig{GRPCAddr: addr, AllowInsecure: true, Timeout: to})
	must(t, err)

	return &clients.ServiceClients{
		Commodore:     commo,
		Purser:        purs,
		Quartermaster: qm,
		Periscope:     peri,
		Deckhand:      deck,
		Decklog:       dlog,
		Navigator:     nav,
		Signalman:     sig,
		Skipper:       skip,
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
}

// TestRealPathSmokeSweep is the one wide smoke test: every Query+Mutation field
// executed through real clients against a stub backend. It proves the gateway's
// whole GraphQL surface runs end-to-end without crashing on empty/zero backend
// data, and covers the resolver real paths (request build + response mapping) en
// masse — no per-method canning. Assertion: no field 500s / panics.
func TestRealPathSmokeSweep(t *testing.T) {
	addr := startStubGRPC(t)
	srv := newRealPathTestServer(realServiceClients(t, addr))

	q := realPathSweep(t, srv, srv.schema.Query, "query")
	m := realPathSweep(t, srv, srv.schema.Mutation, "mutation")
	reportSweep(t, "smoke-query", q)
	reportSweep(t, "smoke-mutation", m)

	// No resolver may crash on empty/degenerate backend data — gqlgen resolves
	// sibling fields concurrently, so this also guards against panics surfaced only
	// under that concurrency. A floor additionally catches a broad regression where
	// the whole surface stops resolving.
	const smokeFloor = 90
	ok := 0
	var crashed []string
	for _, r := range append(append([]sweepResult{}, q...), m...) {
		if r.ok {
			ok++
		}
		if strings.Contains(r.detail, "internal error") {
			crashed = append(crashed, r.field+": "+r.detail)
		}
	}
	if len(crashed) > 0 {
		t.Errorf("smoke: %d field(s) panicked on empty backend data:\n  %s", len(crashed), strings.Join(crashed, "\n  "))
	}
	if ok < smokeFloor {
		t.Errorf("smoke: only %d fields resolved cleanly, want >= %d (surface broadly broken?)", ok, smokeFloor)
	}
}

// TestRealPathErrorSweep drives every field with a backend that fails every RPC,
// exercising the resolver error-handling branches (the `if err != nil` paths the
// happy-path smoke sweep skips). Assertion: failures are handled gracefully — no
// resolver panics on a backend error.
func TestRealPathErrorSweep(t *testing.T) {
	addr := startStubGRPCError(t)
	srv := newRealPathTestServer(realServiceClients(t, addr))

	q := realPathSweep(t, srv, srv.schema.Query, "query")
	m := realPathSweep(t, srv, srv.schema.Mutation, "mutation")

	var crashed []string
	for _, r := range append(append([]sweepResult{}, q...), m...) {
		if strings.Contains(r.detail, "internal error") {
			crashed = append(crashed, r.field+": "+r.detail)
		}
	}
	if len(crashed) > 0 {
		t.Errorf("error-path: %d field(s) panicked on a backend error:\n  %s", len(crashed), strings.Join(crashed, "\n  "))
	}
}
