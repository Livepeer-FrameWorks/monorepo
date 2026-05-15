package llm

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Provider interface {
	Complete(ctx context.Context, messages []Message, tools []Tool) (Stream, error)
}

type Stream interface {
	Recv() (Chunk, error)
	Close() error
}

type Chunk struct {
	Content   string
	ToolCalls []ToolCall
}

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"-"`
}

type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type sseStream struct {
	resp    *http.Response
	reader  *bufio.Reader
	decode  func([]byte) (Chunk, error)
	pending [][]byte
}

func newSSEStream(resp *http.Response, decode func([]byte) (Chunk, error)) Stream {
	return &sseStream{
		resp:   resp,
		reader: bufio.NewReader(resp.Body),
		decode: decode,
	}
}

func (s *sseStream) Close() error {
	return s.resp.Body.Close()
}

func (s *sseStream) Recv() (Chunk, error) {
	for {
		data, err := s.nextPayload()
		if err != nil {
			return Chunk{}, err
		}
		payload := strings.TrimSpace(string(data))
		if payload == "" {
			continue
		}
		if isSSEDonePayload(payload) {
			return Chunk{}, io.EOF
		}
		chunk, err := s.decode(data)
		if err != nil {
			return Chunk{}, err
		}
		if chunk.Content == "" && len(chunk.ToolCalls) == 0 {
			continue
		}
		return chunk, nil
	}
}

func (s *sseStream) nextPayload() ([]byte, error) {
	if len(s.pending) > 0 {
		data := s.pending[0]
		s.pending = s.pending[1:]
		return data, nil
	}
	payloads, err := s.readEvent()
	if err != nil {
		return nil, err
	}
	if len(payloads) == 0 {
		return []byte{}, nil
	}
	s.pending = append(s.pending, payloads[1:]...)
	return payloads[0], nil
}

func isSSEDonePayload(payload string) bool {
	switch strings.ToUpper(strings.TrimSpace(payload)) {
	case "[DONE]", "DONE":
		return true
	default:
		return false
	}
}

const (
	maxRetries     = 3
	retryBaseDelay = 1 * time.Second
)

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusInternalServerError ||
		code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// doWithRetry executes an HTTP request with exponential backoff on retryable
// errors (429, 500-504, connection errors). It respects the Retry-After
// header and the context deadline.
func doWithRetry(ctx context.Context, client *http.Client, buildReq func() (*http.Request, error), baseDelay ...time.Duration) (*http.Response, error) {
	delay := retryBaseDelay
	if len(baseDelay) > 0 {
		delay = baseDelay[0]
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < maxRetries {
				backoff(ctx, attempt, nil, delay)
				continue
			}
			return nil, err
		}
		if !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}
		lastErr = &retryableError{statusCode: resp.StatusCode}
		retryAfter := resp.Header.Get("Retry-After")
		_ = resp.Body.Close()
		if attempt < maxRetries {
			backoff(ctx, attempt, parseRetryAfter(retryAfter), delay)
		}
	}
	return nil, lastErr
}

type retryableError struct {
	statusCode int
}

func (e *retryableError) Error() string {
	return "retryable HTTP status: " + strconv.Itoa(e.statusCode)
}

func backoff(ctx context.Context, attempt int, retryAfter *time.Duration, baseDelay time.Duration) {
	delay := baseDelay * (1 << attempt)
	if retryAfter != nil && *retryAfter > delay {
		delay = *retryAfter
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func parseRetryAfter(value string) *time.Duration {
	if value == "" {
		return nil
	}
	seconds, err := strconv.Atoi(value)
	if err == nil && seconds > 0 {
		d := time.Duration(seconds) * time.Second
		return &d
	}
	return nil
}

func (s *sseStream) readEvent() ([][]byte, error) {
	var dataLines []string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 {
				return normalizeSSEDataLines(dataLines), nil
			}
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
		if errors.Is(err, io.EOF) {
			if len(dataLines) > 0 {
				return normalizeSSEDataLines(dataLines), nil
			}
			return nil, io.EOF
		}
	}
}

func normalizeSSEDataLines(dataLines []string) [][]byte {
	nonDone := make([]string, 0, len(dataLines))
	sawDone := false
	for _, line := range dataLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isSSEDonePayload(line) {
			sawDone = true
			continue
		}
		nonDone = append(nonDone, line)
	}
	if len(nonDone) == 0 {
		if sawDone {
			return [][]byte{[]byte("[DONE]")}
		}
		return nil
	}
	if len(nonDone) == 1 {
		return [][]byte{[]byte(nonDone[0])}
	}
	if allCompleteJSONPayloads(nonDone) {
		payloads := make([][]byte, 0, len(nonDone))
		for _, line := range nonDone {
			payloads = append(payloads, []byte(line))
		}
		return payloads
	}
	return [][]byte{[]byte(strings.Join(nonDone, "\n"))}
}

func allCompleteJSONPayloads(lines []string) bool {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) < 2 {
			return false
		}
		if (!strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}")) &&
			(!strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]")) {
			return false
		}
	}
	return true
}
