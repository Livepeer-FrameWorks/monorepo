package frameworks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/Khan/genqlient/graphql"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

// ClientOptions configures a Client. The client never reads the
// environment; everything comes from here.
type ClientOptions struct {
	// URL is the GraphQL endpoint, e.g. https://bridge.example.com/graphql.
	URL string
	// Token is a bearer token sent as Authorization.
	Token string
	// TokenFunc, when set, returns the token for every attempt instead of
	// Token. An empty token sends no Authorization header.
	TokenFunc func(ctx context.Context) (string, error)
	// HTTPClient sends the requests; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Headers are sent with every request.
	Headers map[string]string
	// Retry replaces DefaultRetryPolicy.
	Retry *RetryPolicy
	// DisableServerCheck skips the serverInfo check. With it set, the client
	// never returns ServerTooOldError or UnsupportedOperationError.
	DisableServerCheck bool
}

// Client runs the generated operations: pass it as the graphql.Client of
// any generated function, e.g. frameworks.GetStream(ctx, client, id).
type Client struct {
	url         string
	token       string
	tokenFunc   func(ctx context.Context) (string, error)
	httpClient  *http.Client
	headers     map[string]string
	retry       RetryPolicy
	checkServer bool

	// Test hooks.
	sleep          func(ctx context.Context, d time.Duration) error
	now            func() time.Time
	operationSince map[string]string
}

var _ graphql.Client = (*Client)(nil)

// NewClient returns a client for one GraphQL endpoint.
func NewClient(opts ClientOptions) (*Client, error) {
	if opts.URL == "" {
		return nil, errors.New("frameworks: ClientOptions.URL is required")
	}
	c := &Client{
		url:         opts.URL,
		token:       opts.Token,
		tokenFunc:   opts.TokenFunc,
		httpClient:  opts.HTTPClient,
		headers:     opts.Headers,
		retry:       DefaultRetryPolicy,
		checkServer: !opts.DisableServerCheck,
		sleep:       sleepContext,
		now:         time.Now,
	}
	if c.httpClient == nil {
		c.httpClient = http.DefaultClient
	}
	if opts.Retry != nil {
		c.retry = *opts.Retry
	}
	if c.retry.MaxAttempts < 1 {
		c.retry.MaxAttempts = 1
	}
	return c, nil
}

// URL returns the client's GraphQL endpoint.
func (c *Client) URL() string { return c.url }

type ctxKey int

const (
	ctxIdempotencyKey ctxKey = iota
	ctxHeaders
)

// WithIdempotencyKey sends key as Idempotency-Key with requests made with
// the returned context. Paid mutations settled with x402 need one, and the
// gateway deduplicates those by it; reuse the same key when you retry the
// call yourself. The client retries a mutation only when the request never
// reached the server (a refused connection, a failed DNS lookup, or a 429).
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxIdempotencyKey, key)
}

// WithHeader adds a header to requests made with the returned context.
func WithHeader(ctx context.Context, name, value string) context.Context {
	next := map[string]string{}
	if prev, ok := ctx.Value(ctxHeaders).(map[string]string); ok {
		for k, v := range prev {
			next[k] = v
		}
	}
	next[name] = value
	return context.WithValue(ctx, ctxHeaders, next)
}

// WithPlaybackToken sends a viewer's playback JWT as
// X-Frameworks-Playback-JWT (resolveViewerEndpoint).
func WithPlaybackToken(ctx context.Context, token string) context.Context {
	return WithHeader(ctx, "X-Frameworks-Playback-JWT", token)
}

// parseOperation selects the operation whose retry policy will govern the request.
func parseOperation(document, operationName string) (kind, name string, err error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: document})
	if err != nil {
		return "", "", fmt.Errorf("frameworks: parse GraphQL operation: %w", err)
	}
	if operationName == "" && len(doc.Operations) != 1 {
		return "", "", errors.New("frameworks: document must select exactly one GraphQL operation")
	}
	op := doc.Operations.ForName(operationName)
	if op == nil {
		return "", "", fmt.Errorf("frameworks: operation %q not found", operationName)
	}
	return string(op.Operation), op.Name, nil
}

// MakeRequest implements graphql.Client: it runs one query or mutation and
// decodes its data into resp.Data. Failures are the SDK's typed errors.
func (c *Client) MakeRequest(ctx context.Context, req *graphql.Request, resp *graphql.Response) error {
	kind, name, err := parseOperation(req.Query, req.OpName)
	if err != nil {
		return err
	}
	if kind == "subscription" {
		return errors.New("frameworks: subscriptions run over WebSocket; use a SubscriptionClient")
	}
	if c.checkServer && name != "ServerInfo" {
		if gateErr := c.gate(ctx, name); gateErr != nil {
			return gateErr
		}
	}
	data, err := c.send(ctx, kind, name, req.Query, req.Variables)
	if err != nil {
		var mismatch *SchemaMismatchError
		if c.checkServer && name != "ServerInfo" && errors.As(err, &mismatch) {
			return c.recheck(ctx, name, err)
		}
		return err
	}
	if resp != nil && resp.Data != nil {
		if err := json.Unmarshal(data, resp.Data); err != nil {
			return &ProtocolError{ErrorInfo{Message: fmt.Sprintf("decoding %s data: %v", name, err), Cause: err, Body: json.RawMessage(data)}}
		}
	}
	return nil
}

func (c *Client) resolveToken(ctx context.Context) (string, error) {
	if c.tokenFunc != nil {
		return c.tokenFunc(ctx)
	}
	return c.token, nil
}

// send posts one GraphQL operation and returns its data, retrying per the
// policy. Queries retry network errors and 408, 429, and 5xx responses. A
// mutation, with or without an idempotency key, retries only when the
// request provably never reached the server: a refused connection, a failed
// DNS lookup, or a 429. The gateway does not deduplicate replayed mutations,
// so a mutation is never sent again after a timeout, a reset, or a 5xx.
func (c *Client) send(ctx context.Context, kind, name, query string, variables any) (json.RawMessage, error) {
	idempotencyKey := ""
	if key, ok := ctx.Value(ctxIdempotencyKey).(string); ok {
		idempotencyKey = key
	}
	vars, err := omitNulls(variables)
	if err != nil {
		return nil, fmt.Errorf("frameworks: encoding %s variables: %w", name, err)
	}
	payload := map[string]any{"query": query, "variables": vars}
	if name != "" {
		payload["operationName"] = name
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("frameworks: encoding %s: %w", name, err)
	}
	for attempt := 1; ; attempt++ {
		data, failure, retryable, unsent, err := c.attempt(ctx, body, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if failure == nil {
			return data, nil
		}
		mayRetry := retryable
		if kind != "query" {
			mayRetry = unsent || failure.Info().Status != nil && *failure.Info().Status == http.StatusTooManyRequests
		}
		if !mayRetry || attempt >= c.retry.MaxAttempts {
			return nil, failure
		}
		delay := backoffDelay(c.retry, attempt)
		if ra := failure.Info().RetryAfterSeconds; ra != nil {
			wait := time.Duration(*ra) * time.Second
			if wait > c.retry.MaxRetryAfter {
				return nil, failure
			}
			delay = wait
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// omitNulls encodes variables with every null object member removed. The
// generated argument and input types hold unset optional values as nil
// pointers, which encode as null; GraphQL treats an explicit null
// differently from an omitted value (it overrides a default), so unset
// values are left out, as the TypeScript and Python SDKs leave them out.
func omitNulls(variables any) (any, error) {
	if variables == nil {
		return map[string]any{}, nil
	}
	raw, err := json.Marshal(variables)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var strip func(any) any
	strip = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if child == nil {
					delete(t, k)
					continue
				}
				t[k] = strip(child)
			}
		case []any:
			for i, child := range t {
				t[i] = strip(child)
			}
		}
		return v
	}
	if v == nil {
		return map[string]any{}, nil
	}
	return strip(v), nil
}

// failedBeforeSend reports whether a transport error happened before any
// byte of the request was sent: the connection was refused or the host name
// did not resolve.
func failedBeforeSend(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial" && errors.Is(err, syscall.ECONNREFUSED)
}

// attempt sends one HTTP request. err is set only when the caller's context
// ended; every other failure is returned as failure. retryable says a query
// may be sent again; unsent says the request never reached the server.
func (c *Client) attempt(ctx context.Context, body []byte, idempotencyKey string) (data json.RawMessage, failure Error, retryable, unsent bool, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, false, false, fmt.Errorf("frameworks: building request: %w", err)
	}
	// Transport treats Idempotency-Key as permission to replay a POST after
	// connection loss. Only send's operation-aware policy may retry this body.
	httpReq.GetBody = nil
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/graphql-response+json, application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	if extra, ok := ctx.Value(ctxHeaders).(map[string]string); ok {
		for k, v := range extra {
			httpReq.Header.Set(k, v)
		}
	}
	token, err := c.resolveToken(ctx)
	if err != nil {
		return nil, nil, false, false, fmt.Errorf("frameworks: token: %w", err)
	}
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, false, false, ctx.Err()
		}
		return nil, &NetworkError{ErrorInfo{Message: fmt.Sprintf("request to %s failed: %v", c.url, err), Cause: err}}, true, failedBeforeSend(err), nil
	}
	defer func() { _ = res.Body.Close() }()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, false, false, ctx.Err()
		}
		return nil, &NetworkError{ErrorInfo{Message: fmt.Sprintf("reading the response from %s failed: %v", c.url, err), Cause: err}}, true, false, nil
	}
	data, failure, retryable = classify(res.StatusCode, res.Header.Get("Retry-After"), text)
	return data, failure, retryable, false, nil
}

type responseBody struct {
	Data   json.RawMessage     `json:"data"`
	Errors []GraphQLErrorEntry `json:"errors"`
}

// classify turns one HTTP response into data or a typed error.
func classify(status int, retryAfterHeader string, text []byte) (json.RawMessage, Error, bool) {
	var retryAfter *int
	if n, ok := parseRetryAfter(retryAfterHeader, time.Now()); ok {
		retryAfter = &n
	}
	retryable := retryableStatus(status)
	is2xx := status >= 200 && status < 300

	var obj map[string]json.RawMessage
	var body any
	isObject := len(text) > 0 && json.Unmarshal(text, &obj) == nil && obj != nil
	if isObject {
		if err := json.Unmarshal(text, &body); err != nil {
			body = nil
		}
	}

	if isObject {
		var parsed responseBody
		if json.Unmarshal(text, &parsed) == nil && len(parsed.Errors) > 0 {
			var httpStatus *int
			if !is2xx {
				httpStatus = intPtr(status)
			}
			return nil, graphQLError(parsed.Errors, parsed.Data, httpStatus, retryAfter, body), retryable
		}
	}
	if !is2xx {
		return nil, httpError(status, obj, text, retryAfter, body), retryable
	}
	if !isObject {
		return nil, &ProtocolError{ErrorInfo{Message: fmt.Sprintf("response is not JSON (HTTP %d)", status), Status: intPtr(status), Body: string(text)}}, false
	}
	data, ok := obj["data"]
	if !ok || string(data) == "null" {
		return nil, &ProtocolError{ErrorInfo{Message: fmt.Sprintf("response has no data (HTTP %d)", status), Status: intPtr(status), Body: body}}, false
	}
	return data, nil, false
}

func jsonString(obj map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := obj[key]; ok {
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
	}
	return s
}

func httpError(status int, obj map[string]json.RawMessage, text []byte, retryAfter *int, body any) Error {
	message := jsonString(obj, "message")
	if message == "" {
		message = jsonString(obj, "error")
	}
	if message == "" {
		message = strings.TrimSpace(string(text))
		if len(message) > 200 {
			message = message[:200]
		}
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", status)
	}
	if body == nil {
		body = string(text)
	}
	info := ErrorInfo{Message: message, Status: intPtr(status), Code: jsonString(obj, "code"), RetryAfterSeconds: retryAfter, Body: body}
	switch {
	case status == 401:
		return &AuthenticationError{info}
	case status == 402:
		return &PaymentRequiredError{info}
	case status == 429:
		return &RateLimitError{info}
	case status >= 500:
		return &ServerError{info}
	default:
		return &HTTPError{info}
	}
}

// graphQLError maps GraphQL errors by the extensions.code of the first entry.
func graphQLError(entries []GraphQLErrorEntry, data json.RawMessage, status *int, retryAfter *int, body any) Error {
	first := entries[0]
	code := ""
	if c, ok := first.Extensions["code"].(string); ok {
		code = c
	}
	message := first.Message
	if message == "" {
		message = "GraphQL error"
	}
	info := ErrorInfo{Message: message, Status: status, Code: code, RetryAfterSeconds: retryAfter, Body: body}
	gqlErr := GraphQLError{ErrorInfo: info, Errors: entries, Path: first.Path, Data: data}
	switch code {
	case "UNAUTHORIZED":
		return &AuthenticationError{info}
	case "RATE_LIMITED":
		return &RateLimitError{info}
	case "GRAPHQL_VALIDATION_FAILED", "GRAPHQL_PARSE_FAILED":
		return &SchemaMismatchError{gqlErr}
	default:
		return &gqlErr
	}
}
