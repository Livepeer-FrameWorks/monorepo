package llm

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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
	Role       string `json:"role"`
	Content    string `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
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
	resp   *http.Response
	reader *bufio.Reader
	decode func([]byte) (Chunk, error)
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
		data, err := s.readEvent()
		if err != nil {
			return Chunk{}, err
		}
		payload := strings.TrimSpace(string(data))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
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

func (s *sseStream) readEvent() ([]byte, error) {
	var dataLines []string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) > 0 {
				return []byte(strings.Join(dataLines, "\n")), nil
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
				return []byte(strings.Join(dataLines, "\n")), nil
			}
			return nil, io.EOF
		}
	}
}
