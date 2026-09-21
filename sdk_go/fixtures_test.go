package frameworks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// loadFixture reads one of the language-neutral fixtures every SDK's tests
// share: from the monorepo's sdk_conformance, or, in the sdk-go mirror,
// from the copy the release commit places in testdata/sdk_conformance.
func loadFixture(t *testing.T, name string, v any) {
	t.Helper()
	dir := filepath.Join("..", "sdk_conformance")
	if _, err := os.Stat(dir); err != nil {
		dir = filepath.Join("testdata", "sdk_conformance")
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

type fixtureResponse struct {
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
	BodyText     *string           `json:"bodyText"`
	NetworkError networkFailure    `json:"networkError"`
}

// networkFailure is a fixture networkError: refused, dns, reset, or timeout;
// true means reset.
type networkFailure string

func (n *networkFailure) UnmarshalJSON(data []byte) error {
	switch string(data) {
	case "true":
		*n = "reset"
		return nil
	case "false", "null":
		*n = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*n = networkFailure(s)
	return nil
}

// err is the error net/http's transport returns for the failure.
func (n networkFailure) err() error {
	switch n {
	case "refused":
		return &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	case "dns":
		return &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "retry.test", IsNotFound: true}}
	case "reset":
		return &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)}
	case "timeout":
		return &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	}
	panic("unknown fixture networkError " + string(n))
}

type recordedRequest struct {
	OperationName string
	Header        http.Header
	Body          map[string]any
}

// scriptedTransport answers each request from the queue of its operation
// name ("*" when there is none) and records every request.
type scriptedTransport struct {
	mu       sync.Mutex
	queues   map[string][]fixtureResponse
	requests []recordedRequest
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	name, _ := body["operationName"].(string)
	s.mu.Lock()
	s.requests = append(s.requests, recordedRequest{OperationName: name, Header: req.Header.Clone(), Body: body})
	key := name
	if _, ok := s.queues[key]; !ok {
		key = "*"
	}
	queue := s.queues[key]
	if len(queue) == 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("no scripted response left for %q", name)
	}
	next := queue[0]
	s.queues[key] = queue[1:]
	s.mu.Unlock()
	if next.NetworkError != "" {
		return nil, next.NetworkError.err()
	}
	text := ""
	switch {
	case next.BodyText != nil:
		text = *next.BodyText
	case len(next.Body) > 0:
		text = string(next.Body)
	}
	status := next.Status
	if status == 0 {
		status = 200
	}
	h := http.Header{}
	for k, v := range next.Headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(text)), Request: req}, nil
}

func (s *scriptedTransport) sent(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.OperationName == name {
			n++
		}
	}
	return n
}

func newScripted(queues map[string][]fixtureResponse) *scriptedTransport {
	copied := map[string][]fixtureResponse{}
	for k, v := range queues {
		copied[k] = append([]fixtureResponse(nil), v...)
	}
	return &scriptedTransport{queues: copied}
}

// recordSleeps replaces the client's sleep with one that records the waits.
func recordSleeps(c *Client) *[]int64 {
	delays := &[]int64{}
	c.sleep = func(_ context.Context, d time.Duration) error {
		*delays = append(*delays, d.Milliseconds())
		return nil
	}
	return delays
}

// errorFields extracts the fields a fixture can name from an SDK error.
func errorFields(err error) (string, map[string]any) {
	var sdkErr Error
	if !errors.As(err, &sdkErr) {
		return fmt.Sprintf("%T", err), map[string]any{}
	}
	info := sdkErr.Info()
	fields := map[string]any{"message": info.Message}
	if info.Status != nil {
		fields["status"] = *info.Status
	}
	if info.Code != "" {
		fields["code"] = info.Code
	}
	if info.RetryAfterSeconds != nil {
		fields["retryAfterSeconds"] = *info.RetryAfterSeconds
	}
	var resultErr *ResultError
	var gqlErr *GraphQLError
	var tooOld *ServerTooOldError
	var unsupported *UnsupportedOperationError
	switch {
	case errors.As(err, &resultErr):
		fields["typename"] = resultErr.Typename
		for k, v := range map[string]string{"field": resultErr.Field, "resourceType": resultErr.ResourceType, "resourceId": resultErr.ResourceID} {
			if v != "" {
				fields[k] = v
			}
		}
	case errors.As(err, &gqlErr):
		if gqlErr.Path != nil {
			fields["path"] = gqlErr.Path
		}
	case errors.As(err, &tooOld):
		if tooOld.ServerVersion != nil {
			fields["serverVersion"] = *tooOld.ServerVersion
		}
		fields["minimumVersion"] = tooOld.MinimumVersion
	case errors.As(err, &unsupported):
		fields["operation"] = unsupported.Operation
		fields["since"] = unsupported.Since
		fields["serverVersion"] = unsupported.ServerVersion
	}
	return sdkErr.Kind(), fields
}

// checkError reports every difference between err and a fixture's expected
// error. A field listed as null must be absent.
func checkError(t *testing.T, err error, expected map[string]any) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want %v", expected["kind"])
	}
	kind, fields := errorFields(err)
	if kind != expected["kind"] {
		t.Errorf("kind %s (%v), want %v", kind, err, expected["kind"])
	}
	for key, want := range expected {
		if key == "kind" {
			continue
		}
		got, _ := json.Marshal(fields[key])
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("%s = %s, want %s", key, got, wantJSON)
		}
	}
}
