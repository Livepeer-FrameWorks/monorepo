package chat

import (
	"context"
	"io"
	"testing"

	"frameworks/pkg/llm"
)

type fakeRewriterLLM struct {
	response string
	err      error
}

func (f *fakeRewriterLLM) Complete(_ context.Context, _ []llm.Message, _ []llm.Tool) (llm.Stream, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &singleChunkStream{content: f.response}, nil
}

// singleChunkStream returns a single content chunk then EOF.
type singleChunkStream struct {
	content  string
	consumed bool
}

func (s *singleChunkStream) Recv() (llm.Chunk, error) {
	if s.consumed {
		return llm.Chunk{}, io.EOF
	}
	s.consumed = true
	return llm.Chunk{Content: s.content}, nil
}

func (s *singleChunkStream) Close() error { return nil }

func TestQueryRewriter_RewritesQuery(t *testing.T) {
	provider := &fakeRewriterLLM{response: "stream disconnection troubleshooting"}
	qr := NewQueryRewriter(provider)

	result := qr.Rewrite(context.Background(), "my stream keeps dying")
	if result != "stream disconnection troubleshooting" {
		t.Fatalf("expected rewritten query, got %q", result)
	}
}

func TestQueryRewriter_NilProvider_Passthrough(t *testing.T) {
	qr := NewQueryRewriter(nil)
	result := qr.Rewrite(context.Background(), "original query")
	if result != "original query" {
		t.Fatalf("expected passthrough, got %q", result)
	}
}

func TestQueryRewriter_NilReceiver_Passthrough(t *testing.T) {
	var qr *QueryRewriter
	result := qr.Rewrite(context.Background(), "original query")
	if result != "original query" {
		t.Fatalf("expected passthrough, got %q", result)
	}
}

func TestQueryRewriter_LLMError_Passthrough(t *testing.T) {
	provider := &fakeRewriterLLM{err: io.ErrUnexpectedEOF}
	qr := NewQueryRewriter(provider)

	result := qr.Rewrite(context.Background(), "original query")
	if result != "original query" {
		t.Fatalf("expected passthrough on error, got %q", result)
	}
}

func TestQueryRewriter_EmptyResponse_Passthrough(t *testing.T) {
	provider := &fakeRewriterLLM{response: "  "}
	qr := NewQueryRewriter(provider)

	result := qr.Rewrite(context.Background(), "original query")
	if result != "original query" {
		t.Fatalf("expected passthrough for blank response, got %q", result)
	}
}
