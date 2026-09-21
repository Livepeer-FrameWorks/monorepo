// Command quickstart creates a stream and lists every stream of the tenant.
package main

import (
	"context"
	"errors"
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

	created, err := frameworks.CreateStream(ctx, client, frameworks.CreateStreamInput{Name: "launch-event"})
	if err != nil {
		log.Fatal(err)
	}
	stream, err := frameworks.ExpectResult[*frameworks.CreateStreamCreateStream](created.CreateStream)
	var invalid *frameworks.ResultError
	if errors.As(err, &invalid) {
		log.Fatalf("stream rejected: %s (%s)", invalid.Message, invalid.Field)
	} else if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created", stream.GetId(), "playback", stream.GetPlaybackId())

	streams := frameworks.PaginateRelay(ctx, func(ctx context.Context, page frameworks.RelayPageRequest) (frameworks.RelayPage[frameworks.ListStreamsStreamsConnectionNodesStream], error) {
		resp, err := frameworks.ListStreams(ctx, client, &frameworks.ConnectionInput{First: &page.First, After: page.After}, nil)
		if err != nil {
			return frameworks.RelayPage[frameworks.ListStreamsStreamsConnectionNodesStream]{}, err
		}
		conn := resp.StreamsConnection
		return frameworks.RelayPage[frameworks.ListStreamsStreamsConnectionNodesStream]{
			Nodes:       conn.Nodes,
			HasNextPage: conn.PageInfo.HasNextPage,
			EndCursor:   conn.PageInfo.EndCursor,
		}, nil
	}, frameworks.PaginateOptions{PageSize: 50})
	for s, err := range streams {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(s.GetName(), s.GetPlaybackId())
	}
}
