// SDK smoke for github.com/Livepeer-FrameWorks/sdk-go: list, a tenantEvents
// subscription with a bearer token and no Origin, createStream observed on the
// subscription, deleteStream. Prints PASS/FAIL lines; exits 1 on any failure.
// 07-api-sdk.sh builds it in a temporary module that replaces sdk-go with the
// repository's sdk_go.
//
//	go run . <graphql url> <ws url> <token>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

var failed int

func report(ok bool, msg string) {
	status := "PASS"
	if !ok {
		status = "FAIL"
		failed++
	}
	fmt.Printf("%s  sdk-go: %s\n", status, msg)
}

func main() {
	url, wsURL, token := os.Args[1], os.Args[2], os.Args[3]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	client, err := frameworks.NewClient(frameworks.ClientOptions{URL: url, Token: token})
	if err != nil {
		report(false, "NewClient: "+err.Error())
		os.Exit(1)
	}
	_, err = frameworks.ListStreams(ctx, client, nil, nil)
	report(err == nil, fmt.Sprintf("ListStreams (err=%v)", err))

	sc, err := frameworks.NewSubscriptionClient(frameworks.SubscriptionOptions{URL: wsURL, Token: token})
	if err != nil {
		report(false, "NewSubscriptionClient: "+err.Error())
		os.Exit(1)
	}
	name := fmt.Sprintf("stack-sdk-go-%d", time.Now().Unix())
	seen := make(chan bool, 1)
	subCtx, stopSub := context.WithTimeout(ctx, 25*time.Second)
	defer stopSub()
	go func() {
		for ev, subErr := range frameworks.SubscribeTenantEvents(subCtx, sc, []string{"stream.created"}, nil) {
			if subErr != nil {
				fmt.Printf("  subscription ended: %v\n", subErr)
				seen <- false
				return
			}
			raw, marshalErr := json.Marshal(ev)
			if marshalErr == nil && strings.Contains(string(raw), name) {
				seen <- true
				return
			}
		}
		seen <- false
	}()
	time.Sleep(1500 * time.Millisecond)

	created, err := frameworks.CreateStream(ctx, client, frameworks.CreateStreamInput{Name: name})
	var createdID string
	if err == nil {
		var shape struct {
			CreateStream struct {
				ID string `json:"id"`
			} `json:"createStream"`
		}
		raw, marshalErr := json.Marshal(created)
		if marshalErr == nil && json.Unmarshal(raw, &shape) == nil {
			createdID = shape.CreateStream.ID
		}
	}
	report(err == nil && createdID != "", fmt.Sprintf("CreateStream (err=%v)", err))
	report(<-seen, "tenantEvents delivered stream.created (bearer, no Origin)")
	if createdID != "" {
		_, err = frameworks.DeleteStream(ctx, client, createdID)
		report(err == nil, fmt.Sprintf("DeleteStream (err=%v)", err))
	}
	if failed > 0 {
		os.Exit(1)
	}
}
