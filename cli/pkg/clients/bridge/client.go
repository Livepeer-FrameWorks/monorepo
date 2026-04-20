// Package bridge is a minimal GraphQL client the CLI uses to talk to the
// FrameWorks Gateway (Bridge). Bridge is the operator-facing entry point;
// edge bootstrap, cluster creation, and enrollment-token issuance all
// flow through it. Cluster-internal services (Quartermaster, Foghorn)
// are not reachable from the CLI directly.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a thin GraphQL POST helper. One instance per CLI invocation
// is enough; it carries the Bridge URL but resolves auth per-call so the
// caller decides whether to attach a JWT.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a Client targeting the given Bridge base URL (e.g.
// "https://bridge.frameworks.network"). The caller is responsible for
// trimming trailing slashes; New tolerates either form.
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Do executes a GraphQL request. When jwt is empty the request is sent
// unauthenticated — Bridge's allowlist will accept that for the small
// set of public operations (bootstrapEdge, the public read-only
// queries). variables may be nil.
func (c *Client) Do(ctx context.Context, query, jwt string, variables map[string]any, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("bridge: empty base URL")
	}
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("bridge: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/graphql/", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("bridge: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if jwt != "" {
		req.Header.Set("Authorization", "Bearer "+jwt)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bridge: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("bridge: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("bridge: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []graphQLError  `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("bridge: decode envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return errorsToError(envelope.Errors)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("bridge: decode data: %w", err)
	}
	return nil
}

type graphQLError struct {
	Message string `json:"message"`
}

func errorsToError(errs []graphQLError) error {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("bridge: %s", strings.Join(msgs, "; "))
}
