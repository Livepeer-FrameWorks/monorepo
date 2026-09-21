// Command playbacktoken signs a five-minute viewer token for content with a
// JWT playback policy.
package main

import (
	"fmt"
	"log"
	"os"
	"time"

	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

func main() {
	token, err := frameworks.SignPlaybackToken(frameworks.PlaybackTokenOptions{
		PrivateKeyPEM: os.Getenv("FRAMEWORKS_SIGNING_PRIVATE_KEY"),
		Kid:           os.Getenv("FRAMEWORKS_SIGNING_KID"),
		ExpiresIn:     5 * time.Minute,
		Subject:       "viewer-42",
		Audience:      []string{"viewer"},
		Claims:        map[string]any{"tier": "pro"},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(token)
}
