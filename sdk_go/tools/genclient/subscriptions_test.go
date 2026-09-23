package main

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	gqlast "github.com/vektah/gqlparser/v2/ast"
)

// genqlientSubscriptions is genqlient v0.8 output for one subscription with
// a variable and one without, trimmed to the declarations the rewrite reads.
const genqlientSubscriptions = "package frameworks\n\nimport (\n\t\"context\"\n\t\"encoding/json\"\n\t\"errors\"\n\n\t\"github.com/Khan/genqlient/graphql\"\n)\n\n" +
	"type __ViewsInput struct {\n\tStreamId *string `json:\"streamId\"`\n}\n\n" +
	"type ViewsResponse struct {\n\tViews int `json:\"views\"`\n}\n\n" +
	"type FirehoseResponse struct {\n\tFirehose int `json:\"firehose\"`\n}\n\n" +
	"// The subscription executed by Views.\nconst Views_Operation = `\nsubscription Views ($streamId: ID) {\n\tviews(streamId: $streamId)\n}\n`\n\n" +
	`// To unsubscribe, use [graphql.WebSocketClient.Unsubscribe]
func Views(
	ctx_ context.Context,
	client_ graphql.WebSocketClient,
	streamId *string,
) (dataChan_ chan ViewsWsResponse, subscriptionID_ string, err_ error) {
	req_ := &graphql.Request{
		OpName: "Views",
		Query:  Views_Operation,
		Variables: &__ViewsInput{
			StreamId: streamId,
		},
	}

	dataChan_ = make(chan ViewsWsResponse)
	subscriptionID_, err_ = client_.Subscribe(req_, dataChan_, ViewsForwardData)

	return dataChan_, subscriptionID_, err_
}

type ViewsWsResponse graphql.BaseResponse[*ViewsResponse]

func ViewsForwardData(interfaceChan interface{}, jsonRawMsg json.RawMessage) error {
	dataChan_, ok := interfaceChan.(chan ViewsWsResponse)
	if !ok {
		return errors.New("failed to cast interface into 'chan ViewsWsResponse'")
	}
	var wsResp ViewsWsResponse
	if err := json.Unmarshal(jsonRawMsg, &wsResp); err != nil {
		return err
	}
	dataChan_ <- wsResp
	return nil
}
` + "\n// The subscription executed by Firehose.\nconst Firehose_Operation = `\nsubscription Firehose {\n\tfirehose\n}\n`\n\n" +
	`// To unsubscribe, use [graphql.WebSocketClient.Unsubscribe]
func Firehose(
	ctx_ context.Context,
	client_ graphql.WebSocketClient,
) (dataChan_ chan FirehoseWsResponse, subscriptionID_ string, err_ error) {
	req_ := &graphql.Request{
		OpName: "Firehose",
		Query:  Firehose_Operation,
	}

	dataChan_ = make(chan FirehoseWsResponse)
	subscriptionID_, err_ = client_.Subscribe(req_, dataChan_, FirehoseForwardData)

	return dataChan_, subscriptionID_, err_
}

type FirehoseWsResponse graphql.BaseResponse[*FirehoseResponse]

func FirehoseForwardData(interfaceChan interface{}, jsonRawMsg json.RawMessage) error {
	dataChan_, ok := interfaceChan.(chan FirehoseWsResponse)
	if !ok {
		return errors.New("failed to cast interface into 'chan FirehoseWsResponse'")
	}
	var wsResp FirehoseWsResponse
	if err := json.Unmarshal(jsonRawMsg, &wsResp); err != nil {
		return err
	}
	dataChan_ <- wsResp
	return nil
}

func Ping(ctx_ context.Context, client_ graphql.Client) error { return nil }
`

func TestSubscriptionFunctions(t *testing.T) {
	t.Parallel()
	schema := gqlparser.MustLoadSchema(&gqlast.Source{Input: `
type Query { ping: Int }
type Subscription {
  """View count updates of one stream."""
  views(streamId: ID): Int!
  firehose: Int!
}
`})
	out, err := subscriptionFunctions([]byte(genqlientSubscriptions), schema, map[string]string{
		"Views":    "subscription.views",
		"Firehose": "subscription.firehose",
	}, map[string]string{
		"Firehose": "Experimental until v0.4.0: The firehose shape is not final.",
	})
	if err != nil {
		t.Fatal(err)
	}
	src := string(out)
	for _, want := range []string{
		"\t\"iter\"\n",
		"// The subscription executed by SubscribeViews.\n",
		"// SubscribeViews runs the Views subscription",
		"// View count updates of one stream.\n",
		"//\n// Experimental until v0.4.0: The firehose shape is not final.\nfunc SubscribeFirehose(",
		"func SubscribeViews(ctx context.Context, sc *SubscriptionClient, streamId *string) iter.Seq2[*ViewsResponse, error] {",
		"return Subscribe[ViewsResponse](ctx, sc, \"Views\", Views_Operation, &__ViewsInput{\n\t\tStreamId: streamId,\n\t})",
		"func SubscribeFirehose(ctx context.Context, sc *SubscriptionClient) iter.Seq2[*FirehoseResponse, error] {",
		"return Subscribe[FirehoseResponse](ctx, sc, \"Firehose\", Firehose_Operation, nil)",
		"func Ping(ctx_ context.Context, client_ graphql.Client) error",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("output lacks %q:\n%s", want, src)
		}
	}
	for _, gone := range []string{"WebSocketClient", "WsResponse", "ForwardData", "\"errors\"", "func Views(", "func Firehose("} {
		if strings.Contains(src, gone) {
			t.Errorf("output still contains %q:\n%s", gone, src)
		}
	}
}

func TestSubscriptionVariableClashesWithWrapperParameter(t *testing.T) {
	t.Parallel()
	schema := gqlparser.MustLoadSchema(&gqlast.Source{Input: `
type Query { ping: Int }
type Subscription { views(sc: ID): Int! }
`})
	src := strings.NewReplacer("streamId", "sc", "StreamId", "Sc").Replace(genqlientSubscriptions)
	targets := map[string]string{"Views": "subscription.views", "Firehose": "subscription.views"}
	if _, err := subscriptionFunctions([]byte(src), schema, targets, nil); err == nil || !strings.Contains(err.Error(), "variable named sc") {
		t.Fatalf("err = %v, want a variable named sc", err)
	}
}
