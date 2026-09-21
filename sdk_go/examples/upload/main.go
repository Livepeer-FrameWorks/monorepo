// Command upload uploads a video file as a VOD asset.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

func main() {
	ctx := context.Background()
	client, err := frameworks.NewClient(frameworks.ClientOptions{
		URL:   "https://bridge.example.com/graphql",
		Token: os.Getenv("FRAMEWORKS_API_TOKEN"),
	})
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open("talk.mp4")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		log.Fatal(err)
	}

	asset, err := frameworks.UploadVod(ctx, client, f, info.Size(), frameworks.UploadVodOptions{
		Filename:    "talk.mp4",
		ContentType: "video/mp4",
		Title:       "Conference talk",
		OnProgress: func(done, total int64) {
			fmt.Printf("\r%d/%d bytes", done, total)
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("\nasset", asset.GetId(), "status", asset.GetStatus())
}
