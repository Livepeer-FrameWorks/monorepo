// Command webhooks receives FrameWorks webhooks and handles clip.ready.
package main

import (
	"io"
	"log"
	"net/http"
	"os"

	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

func main() {
	receiver, err := frameworks.NewWebhookReceiver(os.Getenv("FRAMEWORKS_WEBHOOK_SECRET"))
	if err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/webhooks/frameworks", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		event, err := receiver.Receive(body, r.Header)
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		if clip, ok := event.Data.(*publicv1.ClipReady); ok {
			log.Printf("clip %s ready: %d bytes", clip.GetArtifact().GetArtifactId(), clip.GetSizeBytes())
		} else if !event.Known {
			log.Printf("event type %s is newer than this SDK", event.Type)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
