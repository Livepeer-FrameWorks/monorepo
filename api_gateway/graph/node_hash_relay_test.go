package graph

import (
	"context"
	"io"
	"testing"

	"frameworks/api_gateway/internal/resolvers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/sirupsen/logrus"
)

// 32-hex artifact hash whose own base64 decode is a "prefix:rest" pair. The
// node resolver must not decode the already-decoded hash a second time.
const nodeHashDecodingToColonPair = "8f050f485bcdfac7db979fc6606613c0"

func TestNodeResolvesHexHashRelayIDs(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	q := &queryResolver{&Resolver{Resolver: &resolvers.Resolver{Logger: logger}}}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)

	for _, typ := range []string{globalid.TypeVodAsset, globalid.TypeClip} {
		t.Run(typ, func(t *testing.T) {
			node, err := q.Node(ctx, globalid.Encode(typ, nodeHashDecodingToColonPair))
			if err != nil {
				t.Fatalf("node(%s relay id of %s) error: %v", typ, nodeHashDecodingToColonPair, err)
			}
			if node == nil {
				t.Fatalf("node(%s relay id) returned nil", typ)
			}
		})
	}

	vod, err := q.VodAsset(ctx, globalid.Encode(globalid.TypeVodAsset, nodeHashDecodingToColonPair))
	if err != nil || vod == nil {
		t.Fatalf("vodAsset(relay id of 32-hex hash) = (%v, %v), want asset", vod, err)
	}
}
