package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaProviderStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "llama3" {
			t.Fatalf("unexpected model %q", req.Model)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOllamaProvider(Config{
		APIURL: server.URL + "/v1",
		Model:  "llama3",
	})

	stream, err := provider.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()

	var content strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		content.WriteString(chunk.Content)
	}
	if content.String() != "hi" {
		t.Fatalf("unexpected content %q", content.String())
	}
}
