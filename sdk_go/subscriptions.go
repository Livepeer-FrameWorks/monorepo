package frameworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// SubscriptionOptions configures a SubscriptionClient.
type SubscriptionOptions struct {
	// URL is the WebSocket endpoint, e.g. wss://bridge.example.com/graphql/ws.
	URL string
	// Token is a bearer token sent as Authorization in connection_init.
	Token string
	// TokenFunc, when set, returns the token for every connection instead of
	// Token, so a refreshed token is used after a reconnect.
	TokenFunc func(ctx context.Context) (string, error)
	// MaxReconnects bounds reconnects in a row without an acknowledged
	// connection between them; 0 means 5. A negative value disables
	// reconnecting.
	MaxReconnects int
	// ReconnectDelay returns the wait before reconnect number retries
	// (0-based); defaults to randomized exponential backoff.
	ReconnectDelay func(retries int) time.Duration
	// Dialer opens the connections; defaults to websocket.DefaultDialer.
	Dialer *websocket.Dialer
	// Header is sent with every WebSocket upgrade request.
	Header http.Header
}

// SubscriptionClient runs GraphQL subscriptions over graphql-transport-ws.
// Each subscription uses its own connection.
type SubscriptionClient struct {
	opts SubscriptionOptions
}

// NewSubscriptionClient returns a client for one WebSocket endpoint.
func NewSubscriptionClient(opts SubscriptionOptions) (*SubscriptionClient, error) {
	if opts.URL == "" {
		return nil, errors.New("frameworks: SubscriptionOptions.URL is required")
	}
	if opts.MaxReconnects == 0 {
		opts.MaxReconnects = 5
	}
	if opts.MaxReconnects < 0 {
		opts.MaxReconnects = 0
	}
	if opts.Dialer == nil {
		opts.Dialer = websocket.DefaultDialer
	}
	if opts.ReconnectDelay == nil {
		opts.ReconnectDelay = func(retries int) time.Duration {
			base := time.Second << min(retries, 5)
			return base + time.Duration(rand.Int64N(int64(3*time.Second)))
		}
	}
	return &SubscriptionClient{opts: opts}, nil
}

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// closeForbidden is graphql-transport-ws 4403 Forbidden: the gateway
// rejected the connection's Authorization value.
const closeForbidden = 4403

// connectionOutcome says how one connection ended.
type connectionOutcome int

const (
	outcomeDone      connectionOutcome = iota // completed, failed, or stopped
	outcomeReconnect                          // closed or failed; connect again
)

// Subscribe runs one subscription and yields each event's data decoded into
// T. The sequence ends when the server completes the subscription and yields
// one error when it fails: *AuthenticationError when the server closes the
// connection with 4403 before acknowledging it (an invalid or expired token,
// never retried), the mapped GraphQL error for an error message, and
// *NetworkError when reconnects run out. Any other close or network error,
// before or after the acknowledgement, reconnects with backoff; each
// acknowledged connection starts the MaxReconnects count again.
//
// Every public subscription has a generated Subscribe<Operation> function
// (SubscribeTenantEvents, SubscribeLiveStreamEvents, ...) that calls
// Subscribe with its document, response type, and variables struct; call
// Subscribe directly only for a document of your own. variables is encoded
// as the subscribe message's variables; nil sends an empty object.
func Subscribe[T any](ctx context.Context, sc *SubscriptionClient, operationName, query string, variables any) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		if variables == nil {
			variables = map[string]any{}
		}
		subscribe, err := json.Marshal(map[string]any{
			"id":   "1",
			"type": "subscribe",
			"payload": map[string]any{
				"query":         query,
				"variables":     variables,
				"operationName": operationName,
			},
		})
		if err != nil {
			yield(nil, fmt.Errorf("frameworks: encoding %s: %w", operationName, err))
			return
		}
		retries := 0
		for {
			outcome, acked, err := sc.runConnection(ctx, subscribe, func(data json.RawMessage) bool {
				v := new(T)
				if derr := json.Unmarshal(data, v); derr != nil {
					yield(nil, &ProtocolError{ErrorInfo{Message: "decoding event: " + derr.Error(), Cause: derr}})
					return false
				}
				return yield(v, nil)
			})
			if acked {
				retries = 0
			}
			if outcome == outcomeDone {
				if err != nil {
					yield(nil, err)
				}
				return
			}
			if retries >= sc.opts.MaxReconnects {
				yield(nil, err)
				return
			}
			if serr := sleepContext(ctx, sc.opts.ReconnectDelay(retries)); serr != nil {
				return
			}
			retries++
		}
	}
}

// runConnection opens one connection, subscribes, and delivers events to
// emit until the subscription ends or the connection drops. acked reports
// whether the server acknowledged the connection.
func (sc *SubscriptionClient) runConnection(ctx context.Context, subscribe []byte, emit func(json.RawMessage) bool) (outcome connectionOutcome, acked bool, err error) {
	token := sc.opts.Token
	if sc.opts.TokenFunc != nil {
		if token, err = sc.opts.TokenFunc(ctx); err != nil {
			return outcomeDone, false, fmt.Errorf("frameworks: token: %w", err)
		}
	}
	dialer := *sc.opts.Dialer
	dialer.Subprotocols = []string{"graphql-transport-ws"}
	conn, resp, err := dialer.DialContext(ctx, sc.opts.URL, sc.opts.Header)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return outcomeDone, false, nil
		}
		return outcomeReconnect, false, &NetworkError{ErrorInfo{Message: "subscription connection failed: " + err.Error(), Cause: err}}
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	params := map[string]any{}
	if token != "" {
		params["Authorization"] = "Bearer " + token
	}
	init, err := json.Marshal(map[string]any{"type": "connection_init", "payload": params})
	if err != nil {
		return outcomeDone, false, fmt.Errorf("frameworks: encoding connection_init: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, init); err != nil {
		return outcomeReconnect, false, &NetworkError{ErrorInfo{Message: "subscription connection failed: " + err.Error(), Cause: err}}
	}

	for {
		_, raw, rerr := conn.ReadMessage()
		if rerr != nil {
			if ctx.Err() != nil {
				return outcomeDone, acked, nil
			}
			var closeErr *websocket.CloseError
			if !acked && errors.As(rerr, &closeErr) && closeErr.Code == closeForbidden {
				// The gateway closes a connection whose Authorization value does
				// not authenticate with 4403 before acknowledging it;
				// reconnecting with the same token would loop.
				return outcomeDone, false, &AuthenticationError{ErrorInfo{
					Message: fmt.Sprintf("subscription connection closed before it was acknowledged (%d %s); the token is invalid or expired", closeErr.Code, closeErr.Text),
					Cause:   rerr,
				}}
			}
			// Any other close or network error, before or after the ack
			// (1013 when the gateway could not check the token, a TCP reset),
			// reconnects.
			return outcomeReconnect, acked, &NetworkError{ErrorInfo{Message: "subscription connection closed: " + rerr.Error(), Cause: rerr}}
		}
		var msg wsMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "ping":
			if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"pong"}`)); err != nil {
				return outcomeReconnect, acked, &NetworkError{ErrorInfo{Message: "pong failed: " + err.Error(), Cause: err}}
			}
		case "connection_ack":
			acked = true
			if err := conn.WriteMessage(websocket.TextMessage, subscribe); err != nil {
				return outcomeReconnect, true, &NetworkError{ErrorInfo{Message: "subscribe failed: " + err.Error(), Cause: err}}
			}
		case "next":
			var body responseBody
			if err := json.Unmarshal(msg.Payload, &body); err != nil {
				return outcomeDone, acked, &ProtocolError{ErrorInfo{Message: "decoding event: " + err.Error(), Cause: err}}
			}
			if len(body.Errors) > 0 {
				return outcomeDone, acked, graphQLError(body.Errors, body.Data, nil, nil, nil)
			}
			if !emit(body.Data) {
				// The caller stopped; telling the server is best effort because
				// the connection closes either way.
				if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"id":"1","type":"complete"}`)); err != nil {
					return outcomeDone, acked, nil
				}
				return outcomeDone, acked, nil
			}
		case "error":
			var entries []GraphQLErrorEntry
			if err := json.Unmarshal(msg.Payload, &entries); err != nil || len(entries) == 0 {
				return outcomeDone, acked, &ProtocolError{ErrorInfo{Message: "subscription error without errors: " + string(msg.Payload)}}
			}
			return outcomeDone, acked, graphQLError(entries, nil, nil, nil, nil)
		case "complete":
			// The close frame is best effort: the subscription is over either way.
			if err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")); err != nil {
				return outcomeDone, acked, nil
			}
			return outcomeDone, acked, nil
		}
	}
}
