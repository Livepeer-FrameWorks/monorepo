// Command subscription prints the tenant's stream lifecycle events as they
// happen.
package main

import (
	"context"
	"errors"
	"log"
	"os"

	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

func main() {
	sc, err := frameworks.NewSubscriptionClient(frameworks.SubscriptionOptions{
		URL:   "wss://bridge.example.com/graphql/ws",
		Token: os.Getenv("FRAMEWORKS_API_TOKEN"),
	})
	if err != nil {
		log.Fatal(err)
	}
	types := []string{"stream.live", "stream.idle"}
	for ev, err := range frameworks.SubscribeTenantEvents(context.Background(), sc, types, nil) {
		var authErr *frameworks.AuthenticationError
		if errors.As(err, &authErr) {
			log.Fatal("token rejected: ", authErr.Message)
		} else if err != nil {
			log.Fatal(err)
		}
		log.Printf("%s %s at %s", ev.TenantEvents.Type, ev.TenantEvents.Subject, ev.TenantEvents.Time)
	}
}
