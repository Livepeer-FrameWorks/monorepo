package graph

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestLineItemDimensionsMarshalAsJSONObject(t *testing.T) {
	dimensions, err := structpb.NewStruct(map[string]any{"output_codec": "h264"})
	if err != nil {
		t.Fatal(err)
	}

	value, err := (&lineItemResolver{}).Dimensions(context.Background(), &purserpb.LineItem{Dimensions: dimensions})
	if err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer
	graphql.MarshalAny(value).MarshalGQL(&encoded)
	if got, want := strings.TrimSpace(encoded.String()), `{"output_codec":"h264"}`; got != want {
		t.Fatalf("dimensions JSON = %s, want %s", got, want)
	}
}

func TestClipRequestedParamsPreserveJSONArray(t *testing.T) {
	raw := `[{"profile":"source"},{"profile":"720p"}]`
	value, err := (&clipResolver{}).RequestedParams(context.Background(), &sharedpb.ClipInfo{RequestedParams: &raw})
	if err != nil {
		t.Fatal(err)
	}

	var encoded bytes.Buffer
	graphql.MarshalAny(value).MarshalGQL(&encoded)
	if got := strings.TrimSpace(encoded.String()); got != raw {
		t.Fatalf("requested params JSON = %s, want %s", got, raw)
	}
}
