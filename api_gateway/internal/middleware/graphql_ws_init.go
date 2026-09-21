package middleware

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gorilla/websocket"
)

// Close codes of a connection_init the gateway rejects.
const (
	// WebsocketCloseForbidden rejects an Authorization value that does not
	// authenticate: graphql-transport-ws names 4403 Forbidden for a rejected
	// connection_init. Clients treat it as final for that credential.
	WebsocketCloseForbidden = 4403
	// WebsocketCloseTryAgainLater rejects a connection whose credential could
	// not be checked because the authentication backend failed. It is RFC
	// 6455's 1013 Try Again Later; graphql-ws clients reconnect after it,
	// where they treat 4500 as fatal.
	WebsocketCloseTryAgainLater = 1013
)

// WebsocketInitError rejects a connection_init with a close code and reason.
type WebsocketInitError struct {
	Code   int
	Reason string
}

func (e *WebsocketInitError) Error() string { return e.Reason }

var (
	// ErrWebsocketInvalidAuthorization rejects an Authorization value that
	// is malformed or does not authenticate.
	ErrWebsocketInvalidAuthorization = &WebsocketInitError{Code: WebsocketCloseForbidden, Reason: "Forbidden"}
	// ErrWebsocketAuthUnavailable rejects a connection whose Authorization
	// value could not be checked; the client should reconnect.
	ErrWebsocketAuthUnavailable = &WebsocketInitError{Code: WebsocketCloseTryAgainLater, Reason: "authentication unavailable"}
)

// GraphQLWebsocketTransport is Bridge's GraphQL WebSocket transport:
// graphql-transport-ws (and legacy graphql-ws) with GraphQLWebsocketInit
// resolving the caller, and the close code of a rejected connection_init
// taken from the WebsocketInitError that rejected it.
func GraphQLWebsocketTransport(serviceClients *clients.ServiceClients, jwtSecret []byte, logger logging.Logger, upgrader websocket.Upgrader, keepAlive time.Duration) graphql.Transport {
	return closeCodeWebsocket{Websocket: transport.Websocket{
		KeepAlivePingInterval: keepAlive,
		Upgrader:              upgrader,
		InitFunc:              GraphQLWebsocketInit(serviceClients, jwtSecret, logger),
	}}
}

// closeCodeWebsocket is gqlgen's WebSocket transport with one change. gqlgen
// closes every connection whose InitFunc fails with 1000 "terminated" and
// offers no way to choose the code, so this transport wraps the hijacked
// connection and rewrites that one close frame to the code and reason of the
// WebsocketInitError the InitFunc returned. Everything else passes through.
type closeCodeWebsocket struct {
	transport.Websocket
}

func (t closeCodeWebsocket) Do(w http.ResponseWriter, r *http.Request, exec graphql.GraphExecutor) {
	rejection := &initRejection{}
	inner := t.Websocket
	if init := inner.InitFunc; init != nil {
		inner.InitFunc = func(ctx context.Context, payload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
			ctx, ack, err := init(ctx, payload)
			var initErr *WebsocketInitError
			if errors.As(err, &initErr) {
				rejection.set(initErr)
			}
			return ctx, ack, err
		}
	}
	inner.Do(&rejectionResponseWriter{ResponseWriter: w, rejection: rejection}, r, exec)
}

// initRejection carries the InitFunc's rejection to the connection wrapper.
// It is taken by the first close frame written after it is set.
type initRejection struct {
	mu  sync.Mutex
	err *WebsocketInitError
}

func (r *initRejection) set(err *WebsocketInitError) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// takeFor returns the rejection when frame is a close frame, and clears it.
func (r *initRejection) takeFor(frame []byte) *WebsocketInitError {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil || !isCloseFrame(frame) {
		return nil
	}
	err := r.err
	r.err = nil
	return err
}

// isCloseFrame reports whether frame starts with a final close frame header.
// Server frames are unmasked and gorilla writes each control frame, header
// and payload, in one Write.
func isCloseFrame(frame []byte) bool {
	const finClose = 0x80 | websocket.CloseMessage
	return len(frame) >= 2 && frame[0] == finClose
}

// closeFrame encodes an unmasked server close frame.
func closeFrame(code int, reason string) []byte {
	payload := websocket.FormatCloseMessage(code, reason)
	// Control frame payloads are at most 125 bytes: the 2-byte code plus the
	// reason.
	if len(payload) > 125 {
		payload = payload[:125]
	}
	frame := make([]byte, 0, 2+len(payload))
	frame = append(frame, 0x80|websocket.CloseMessage, byte(len(payload)))
	return append(frame, payload...)
}

type rejectionResponseWriter struct {
	http.ResponseWriter
	rejection *initRejection
}

func (w *rejectionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("websocket: response does not implement http.Hijacker")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return &rejectionConn{Conn: conn, rejection: w.rejection}, rw, nil
}

type rejectionConn struct {
	net.Conn
	rejection *initRejection
}

func (c *rejectionConn) Write(p []byte) (int, error) {
	if rejected := c.rejection.takeFor(p); rejected != nil {
		if _, err := c.Conn.Write(closeFrame(rejected.Code, rejected.Reason)); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	return c.Conn.Write(p)
}

// GraphQLWebsocketInit resolves the caller of a GraphQL WebSocket connection.
//
// Identity comes from, in order: the connection payload's Authorization
// value, the access_token cookie captured at upgrade, and the X-Wallet-*
// headers of the upgrade request.
//
// An Authorization value that is present but does not authenticate closes
// the connection with 4403 before connection_ack, so a client holding an
// expired or revoked token learns it instead of silently running as
// anonymous. This holds even when the upgrade also carries a valid cookie:
// the Authorization value names the identity the client asked for, and the
// gateway never substitutes a different credential for it. An Authorization
// value that could not be checked because the authentication backend failed
// closes with 1013, which clients retry.
//
// The cookie and wallet paths stay lenient: a connection with no valid
// credential there is anonymous, and GraphQLOperationAuth limits it to public
// operations.
func GraphQLWebsocketInit(serviceClients *clients.ServiceClients, jwtSecret []byte, logger logging.Logger) transport.WebsocketInitFunc {
	return func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
		if header := initPayload.Authorization(); header != "" {
			token, ok := bearerToken(header)
			if !ok {
				return ctx, nil, ErrWebsocketInvalidAuthorization
			}
			result, err := AuthenticateBearerToken(ctx, token, serviceClients, jwtSecret)
			if errors.Is(err, ErrAuthBackendUnavailable) {
				if logger != nil {
					logger.WithError(err).Warn("WebSocket authentication backend unavailable")
				}
				return ctx, nil, ErrWebsocketAuthUnavailable
			}
			if err != nil {
				return ctx, nil, ErrWebsocketInvalidAuthorization
			}
			return ApplyAuthToContext(ctx, result), &initPayload, nil
		}

		if cookieToken, ok := ctx.Value(ctxkeys.KeyWSCookieToken).(string); ok && cookieToken != "" {
			if result, err := AuthenticateBearerToken(ctx, cookieToken, serviceClients, jwtSecret); err == nil {
				ctx = ApplyAuthToContext(ctx, result)
			}
			return ctx, &initPayload, nil
		}

		if req, ok := ctx.Value(ctxkeys.KeyHTTPRequest).(*http.Request); ok && req != nil && req.Header.Get("X-Wallet-Address") != "" {
			if result, err := authenticateWallet(ctx, req, serviceClients, logger); err == nil {
				ctx = ApplyAuthToContext(ctx, result)
			}
		}

		return ctx, &initPayload, nil
	}
}
