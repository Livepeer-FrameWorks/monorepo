package middleware

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/goleak"
)

type terminalWSProtocol struct {
	name   string
	start  string
	result string
	stop   string
}

var terminalWSProtocols = []terminalWSProtocol{
	{name: "graphql-ws", start: "start", result: "data", stop: "stop"},
	{name: "graphql-transport-ws", start: "subscribe", result: "next", stop: "complete"},
}

type terminalWSMessage struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Payload struct {
		Data   json.RawMessage `json:"data"`
		Errors gqlerror.List   `json:"errors"`
	} `json:"payload"`
}

type terminalWSFixture struct {
	events    chan struct{}
	started   chan string
	finished  chan string
	active    atomic.Int32
	overflow  atomic.Bool
	serverURL string
}

func newTerminalWSFixture(t *testing.T, sc *clients.ServiceClients, checks ...BillingChecker) *terminalWSFixture {
	t.Helper()
	if sc == nil {
		baseline := goleak.IgnoreCurrent()
		t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	}
	f := &terminalWSFixture{events: make(chan struct{}), started: make(chan string, 8), finished: make(chan string, 8)}
	schema := gqlparser.MustLoadSchema(&ast.Source{Input: `
		type Query { serverInfo: String! }
        type Mutation { createClip: String! }
		type Subscription { name: String! }
	`})
	srv := handler.New(&graphql.ExecutableSchemaMock{
		SchemaFunc: func() *ast.Schema { return schema },
		ComplexityFunc: func(context.Context, string, string, int, map[string]any) (int, bool) {
			return 0, false
		},
		ExecFunc: func(ctx context.Context) graphql.ResponseHandler {
			if graphql.GetOperationContext(ctx).Operation.Operation == ast.Mutation {
				return graphql.OneShot(&graphql.Response{Data: []byte(`{"createClip":"unexpected"}`)})
			}
			if graphql.GetOperationContext(ctx).Operation.Operation == ast.Query {
				return graphql.OneShot(&graphql.Response{Data: []byte(`{"serverInfo":"ok"}`)})
			}
			f.active.Add(1)
			return func(context.Context) *graphql.Response {
				select {
				case <-ctx.Done():
					f.active.Add(-1)
					return nil
				case <-f.events:
					return &graphql.Response{Data: []byte(`{"name":"event"}`)}
				}
			}
		},
	})
	srv.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		name := graphql.GetOperationContext(ctx).OperationName
		responses := next(ctx)
		f.started <- name
		calls := 0
		return func(ctx context.Context) *graphql.Response {
			calls++
			// Bound the known broken producer in a failing regression so the
			// test itself cannot leave a production-style CPU spin behind.
			if calls > 8 {
				f.overflow.Store(true)
				f.finished <- name
				return nil
			}
			response := responses(ctx)
			if response == nil {
				f.finished <- name
			}
			return response
		}
	})
	srv.AroundOperations(GraphQLOperationAuth())
	if len(checks) > 0 {
		srv.AroundOperations(GraphQLOperationAccess(checks[0]))
	}
	srv.AddTransport(GraphQLWebsocketTransport(sc, wsInitSecret, nil, websocket.Upgrader{}, 0, nil, 0))
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	f.serverURL = strings.Replace(httpServer.URL, "http://", "ws://", 1)
	return f
}

func (f *terminalWSFixture) connect(t *testing.T, protocol terminalWSProtocol, authorization string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{protocol.name}}
	conn, response, err := dialer.Dial(f.serverURL, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	payload := map[string]any{}
	if authorization != "" {
		payload["Authorization"] = authorization
	}
	if err := conn.WriteJSON(map[string]any{"type": "connection_init", "payload": payload}); err != nil {
		t.Fatal(err)
	}
	if msg := readTerminalWSMessage(t, conn); msg.Type != "connection_ack" {
		t.Fatalf("init answered %s", msg.Type)
	}
	return conn
}

func readTerminalWSMessage(t *testing.T, conn *websocket.Conn) terminalWSMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		var message terminalWSMessage
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read WebSocket message: %v", err)
		}
		switch message.Type {
		case "ka", "pong":
			continue
		case "ping":
			if err := conn.WriteJSON(map[string]any{"type": "pong"}); err != nil {
				t.Fatal(err)
			}
			continue
		default:
			return message
		}
	}
}

func sendTerminalWSOperation(t *testing.T, conn *websocket.Conn, protocol terminalWSProtocol, id, name, query string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"id": id, "type": protocol.start, "payload": map[string]any{"operationName": name, "query": query}}); err != nil {
		t.Fatal(err)
	}
}

func TestGraphQLWebSocketDeniedOperationTerminates(t *testing.T) {
	for _, protocol := range terminalWSProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			f := newTerminalWSFixture(t, nil)
			conn := f.connect(t, protocol, "")
			sendTerminalWSOperation(t, conn, protocol, "denied", "Denied", `subscription Denied { name }`)
			first := readTerminalWSMessage(t, conn)
			if first.Type != protocol.result || first.ID != "denied" || len(first.Payload.Errors) != 1 || first.Payload.Errors[0].Extensions["code"] != "UNAUTHORIZED" {
				t.Fatalf("first response = %+v", first)
			}
			if next := readTerminalWSMessage(t, conn); next.Type != "complete" || next.ID != "denied" {
				t.Fatalf("denial produced another response instead of completing: %+v", next)
			}
			sendTerminalWSOperation(t, conn, protocol, "public", "Public", `query Public { serverInfo }`)
			if next := readTerminalWSMessage(t, conn); next.Type != protocol.result || len(next.Payload.Errors) != 0 || string(next.Payload.Data) != `{"serverInfo":"ok"}` {
				t.Fatalf("public operation after denial = %+v", next)
			}
			if next := readTerminalWSMessage(t, conn); next.Type != "complete" || next.ID != "public" {
				t.Fatalf("public operation did not complete: %+v", next)
			}
			if f.overflow.Load() || f.active.Load() != 0 {
				t.Fatal("denied operation retained a producer or reached a protected resolver")
			}
		})
	}
}

func TestGraphQLWebSocketDeniedDisconnectTerminates(t *testing.T) {
	for _, protocol := range terminalWSProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			f := newTerminalWSFixture(t, nil)
			conn := f.connect(t, protocol, "")
			sendTerminalWSOperation(t, conn, protocol, "denied", "Denied", `subscription Denied { name }`)
			select {
			case <-f.started:
			case <-time.After(2 * time.Second):
				t.Fatal("operation never started")
			}
			_ = conn.Close()
			select {
			case <-f.finished:
			case <-time.After(2 * time.Second):
				t.Fatal("disconnected denied operation retained its producer")
			}
			if f.overflow.Load() {
				t.Fatal("disconnected denied operation kept generating responses")
			}
		})
	}
}

func TestGraphQLWebSocketHealthySubscriptionStillStreams(t *testing.T) {
	for _, protocol := range terminalWSProtocols {
		t.Run(protocol.name, func(t *testing.T) {
			sc, _ := switchableClients(t)
			f := newTerminalWSFixture(t, sc)
			conn := f.connect(t, protocol, "Bearer fw_api_token_value")
			sendTerminalWSOperation(t, conn, protocol, "live", "Live", `subscription Live { name }`)
			for range 3 {
				select {
				case f.events <- struct{}{}:
				case <-time.After(2 * time.Second):
					t.Fatal("healthy subscription did not wait for its next event")
				}
				if next := readTerminalWSMessage(t, conn); next.Type != protocol.result || len(next.Payload.Errors) != 0 {
					t.Fatalf("healthy subscription response = %+v", next)
				}
			}
			if err := conn.WriteJSON(map[string]any{"id": "live", "type": protocol.stop}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-f.finished:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled healthy subscription did not finish")
			}
			if f.active.Load() != 0 || f.overflow.Load() {
				t.Fatal("healthy subscription retained its producer")
			}
		})
	}
}

func TestGraphQLWebSocketBillingDenialCompletesAndConnectionRemainsUsable(t *testing.T) {
	for _, protocol := range terminalWSProtocols {
		for _, tc := range []struct {
			name, code string
			checker    BillingChecker
		}{
			{"unfunded", "INSUFFICIENT_BALANCE", fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true}},
			{"billing unavailable", "BILLING_STATUS_UNAVAILABLE", fakeBillingChecker{err: context.DeadlineExceeded}},
		} {
			t.Run(protocol.name+"/"+tc.name, func(t *testing.T) {
				sc, _ := switchableClients(t)
				f := newTerminalWSFixture(t, sc, tc.checker)
				conn := f.connect(t, protocol, "Bearer fw_api_token_value")
				sendTerminalWSOperation(t, conn, protocol, "billed", "Billed", `mutation Billed { createClip }`)
				first := readTerminalWSMessage(t, conn)
				if first.Type != protocol.result || first.ID != "billed" || len(first.Payload.Errors) != 1 || first.Payload.Errors[0].Extensions["code"] != tc.code {
					t.Fatalf("billing denial=%+v", first)
				}
				if next := readTerminalWSMessage(t, conn); next.Type != "complete" || next.ID != "billed" {
					t.Fatalf("billing error response repeated: %+v", next)
				}
				sendTerminalWSOperation(t, conn, protocol, "public", "Public", `query Public { serverInfo }`)
				if next := readTerminalWSMessage(t, conn); next.Type != protocol.result || len(next.Payload.Errors) != 0 {
					t.Fatalf("connection not usable after billing denial: %+v", next)
				}
				if next := readTerminalWSMessage(t, conn); next.Type != "complete" {
					t.Fatalf("public query did not complete: %+v", next)
				}
				if f.overflow.Load() || f.active.Load() != 0 {
					t.Fatal("billing denial retained a response producer")
				}
			})
		}
	}
}
