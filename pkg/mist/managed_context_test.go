package mist

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestManagedMutationContextCancelsBeforeAndDuringDispatch(t *testing.T) {
	for _, operation := range []string{"add", "save", "delete"} {
		for _, phase := range []string{"authentication", "command"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				started := make(chan struct{}, 1)
				var mutations atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					auth := strings.Contains(r.URL.Query().Get("command"), "authorize")
					if auth && phase == "command" {
						_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
						return
					}
					if !auth {
						mutations.Add(1)
					}
					started <- struct{}{}
					<-r.Context().Done()
				}))
				defer server.Close()
				client := NewClient(logging.NewLogger(), ClientConfig{BaseURL: server.URL})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					switch operation {
					case "add":
						result <- client.AddStreamsContext(ctx, map[string]map[string]interface{}{"internal": {"source": "file:/input.ts"}})
					case "delete":
						result <- client.DeleteStreamContext(ctx, "internal")
					default:
						result <- client.SaveContext(ctx)
					}
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("request did not start")
				}
				cancel()
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("canceled mutation reported success")
					}
				case <-time.After(time.Second):
					t.Fatal("mutation ignored its context")
				}
				if phase == "authentication" && mutations.Load() != 0 {
					t.Fatal("expired authentication proceeded to media mutation")
				}
			})
		}
	}
}
