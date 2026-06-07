package turnstile

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// roundTripFunc lets a test stand in for the HTTP transport so Verify can be
// driven against canned responses without reaching Cloudflare. It ignores the
// request URL (the real verifyURL is a const), which is exactly what we want:
// we're testing Verify's handling of responses, not the endpoint.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newTestValidator(secret string, rt roundTripFunc) *Validator {
	v := NewValidator(secret)
	v.httpClient = &http.Client{Transport: rt}
	return v
}

func respondJSON(status int, body string) roundTripFunc {
	return func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	}
}

// Empty secret is a deliberate dev fail-OPEN bypass: with no configured key
// Verify reports success without ever hitting the network. Pinned so it can't
// silently flip to fail-closed (which would lock out dev) or start making
// requests with an empty secret.
func TestVerify_EmptySecretFailsOpen(t *testing.T) {
	called := false
	v := newTestValidator("", func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("must not be called")
	})

	resp, err := v.Verify(context.Background(), "any-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success {
		t.Error("empty secret must report Success=true (dev bypass)")
	}
	if called {
		t.Error("empty secret must not make an HTTP request")
	}
}

// Empty token is a failed verification, NOT a transport error: callers get
// (resp, nil) with Success=false so they can show "please retry the
// challenge" rather than a 500.
func TestVerify_EmptyTokenIsVerificationFailure(t *testing.T) {
	v := newTestValidator("secret", func(*http.Request) (*http.Response, error) {
		t.Fatal("must not make an HTTP request for an empty token")
		return nil, nil
	})

	resp, err := v.Verify(context.Background(), "", "1.2.3.4")
	if err != nil {
		t.Fatalf("empty token must not surface as an error: %v", err)
	}
	if resp.Success {
		t.Error("empty token must report Success=false")
	}
	if len(resp.ErrorCodes) != 1 || resp.ErrorCodes[0] != "missing-input-response" {
		t.Errorf("ErrorCodes = %v, want [missing-input-response]", resp.ErrorCodes)
	}
}

func TestVerify_SuccessParsesResponse(t *testing.T) {
	v := newTestValidator("secret", respondJSON(http.StatusOK,
		`{"success":true,"challenge_ts":"2026-06-06T00:00:00Z","hostname":"example.com","error-codes":[]}`))

	resp, err := v.Verify(context.Background(), "good-token", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Success || resp.Hostname != "example.com" {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestVerify_FailureCarriesErrorCodes(t *testing.T) {
	v := newTestValidator("secret", respondJSON(http.StatusOK,
		`{"success":false,"error-codes":["invalid-input-response","timeout-or-duplicate"]}`))

	resp, err := v.Verify(context.Background(), "bad-token", "")
	if err != nil {
		t.Fatalf("a failed challenge is not a transport error: %v", err)
	}
	if resp.Success {
		t.Error("Success should be false")
	}
	if len(resp.ErrorCodes) != 2 {
		t.Errorf("ErrorCodes = %v, want 2 entries", resp.ErrorCodes)
	}
}

// A non-200 from Cloudflare is a transport-class failure and must surface as
// an error (nil resp). The caller must NOT read this as "the human failed the
// challenge" — that distinction is the whole contract.
func TestVerify_Non200IsError(t *testing.T) {
	v := newTestValidator("secret", respondJSON(http.StatusBadGateway, "upstream down"))

	resp, err := v.Verify(context.Background(), "token", "")
	if err == nil {
		t.Fatal("non-200 must return an error")
	}
	if resp != nil {
		t.Errorf("non-200 must return nil response, got %+v", resp)
	}
}

func TestVerify_MalformedBodyIsError(t *testing.T) {
	v := newTestValidator("secret", respondJSON(http.StatusOK, "{not json"))

	resp, err := v.Verify(context.Background(), "token", "")
	if err == nil {
		t.Fatal("malformed body must return an error")
	}
	if resp != nil {
		t.Errorf("malformed body must return nil response, got %+v", resp)
	}
}

func TestVerify_TransportErrorIsError(t *testing.T) {
	v := newTestValidator("secret", func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})

	resp, err := v.Verify(context.Background(), "token", "")
	if err == nil {
		t.Fatal("transport error must return an error")
	}
	if resp != nil {
		t.Errorf("transport error must return nil response, got %+v", resp)
	}
}

// The remoteip form field is sent only when a remote IP is supplied. Cloudflare
// scores differently with/without it, so an accidental empty "remoteip=" is a
// behavior change worth guarding.
func TestVerify_RemoteIPFormField(t *testing.T) {
	tests := []struct {
		name     string
		remoteIP string
		wantKey  bool
	}{
		{"with remote ip", "203.0.113.7", true},
		{"without remote ip", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotForm url.Values
			v := newTestValidator("secret", func(r *http.Request) (*http.Response, error) {
				body, _ := io.ReadAll(r.Body)
				gotForm, _ = url.ParseQuery(string(body))
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
					Header:     make(http.Header),
				}, nil
			})

			if _, err := v.Verify(context.Background(), "token", tt.remoteIP); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotForm.Get("secret") != "secret" || gotForm.Get("response") != "token" {
				t.Errorf("secret/response not sent: %v", gotForm)
			}
			_, present := gotForm["remoteip"]
			if present != tt.wantKey {
				t.Errorf("remoteip present=%v, want %v", present, tt.wantKey)
			}
		})
	}
}
