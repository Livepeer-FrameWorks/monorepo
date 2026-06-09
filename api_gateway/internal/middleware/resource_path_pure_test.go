package middleware

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/google/uuid"
)

// graphqlResourcePath maps a mutation/query + its variables to the resource URI
// that x402 payment and rate limiting are attributed to. A wrong or empty path
// here either over-charges, mis-attributes, or fails to gate a paid resource.
func TestGraphqlResourcePath(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		variables map[string]any
		want      string
	}{
		{"empty operation", "", map[string]any{"id": "x"}, ""},
		{"unknown operation", "someQuery", map[string]any{"id": "x"}, ""},
		{"viewer endpoint", "resolveViewerEndpoint", map[string]any{"contentId": "abc"}, "viewer://abc"},
		{"viewer endpoint snake", "resolveviewerendpoint", map[string]any{"content_id": "abc"}, "viewer://abc"},
		{"update stream", "updateStream", map[string]any{"id": "s1"}, "stream://s1"},
		{"delete stream alt key", "deleteStream", map[string]any{"streamId": "s2"}, "stream://s2"},
		{"create clip nested", "createClip", map[string]any{"input": map[string]any{"streamId": "s3"}}, "stream://s3"},
		{"start dvr", "startDvr", map[string]any{"streamId": "s4"}, "stream://s4"},
		{"stop dvr", "stopDvr", map[string]any{"dvrHash": "h1"}, "dvr://h1"},
		{"delete vod", "deleteVodAsset", map[string]any{"id": "v1"}, "vod://v1"},
		{"known op missing var", "updateStream", map[string]any{}, ""},
		{"known op nil vars", "updateStream", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := graphqlResourcePath(tt.operation, tt.variables); got != tt.want {
				t.Errorf("graphqlResourcePath(%q) = %q, want %q", tt.operation, got, tt.want)
			}
		})
	}
}

func TestGetGraphQLString(t *testing.T) {
	vars := map[string]any{
		"empty":  "",
		"id":     "value-1",
		"number": 42, // non-string ignored
	}
	if got := getGraphQLString(nil, "id"); got != "" {
		t.Errorf("nil vars → %q, want empty", got)
	}
	if got := getGraphQLString(vars, "missing"); got != "" {
		t.Errorf("missing key → %q, want empty", got)
	}
	if got := getGraphQLString(vars, "empty"); got != "" {
		t.Errorf("empty value → %q, want empty (treated as absent)", got)
	}
	if got := getGraphQLString(vars, "number"); got != "" {
		t.Errorf("non-string value → %q, want empty", got)
	}
	// First non-empty match across the alias list wins.
	if got := getGraphQLString(vars, "missing", "id"); got != "value-1" {
		t.Errorf("alias fallthrough = %q, want value-1", got)
	}
}

func TestGetGraphQLNestedString(t *testing.T) {
	vars := map[string]any{
		"input":       map[string]any{"streamId": "s9"},
		"notAnObject": "scalar",
	}
	if got := getGraphQLNestedString(vars, "input", "streamId"); got != "s9" {
		t.Errorf("nested lookup = %q, want s9", got)
	}
	if got := getGraphQLNestedString(vars, "missing", "streamId"); got != "" {
		t.Errorf("missing parent → %q, want empty", got)
	}
	if got := getGraphQLNestedString(vars, "notAnObject", "streamId"); got != "" {
		t.Errorf("non-object parent → %q, want empty", got)
	}
}

// graphqlClipResourceID extracts a clip *hash* for resource attribution. It must
// reject wrong-type global IDs and reject clip IDs whose backing value is a raw
// UUID (those are DB row IDs, not the public hash used as the paid resource key).
func TestGraphqlClipResourceID(t *testing.T) {
	if got := graphqlClipResourceID(""); got != "" {
		t.Errorf("empty → %q, want empty", got)
	}
	// Global clip ID with a hash backing → the hash.
	clipGID := globalid.Encode(globalid.TypeClip, "cliphash123")
	if got := graphqlClipResourceID(clipGID); got != "cliphash123" {
		t.Errorf("clip global id = %q, want cliphash123", got)
	}
	// Global clip ID whose backing is a UUID → rejected.
	uuidGID := globalid.Encode(globalid.TypeClip, uuid.NewString())
	if got := graphqlClipResourceID(uuidGID); got != "" {
		t.Errorf("clip UUID backing = %q, want empty", got)
	}
	// Global ID of a different type → rejected.
	streamGID := globalid.Encode(globalid.TypeStream, "s1")
	if got := graphqlClipResourceID(streamGID); got != "" {
		t.Errorf("wrong-type global id = %q, want empty", got)
	}
	// Non-global-id string is passed through as the raw clip hash.
	if got := graphqlClipResourceID("rawhash"); got != "rawhash" {
		t.Errorf("raw string = %q, want rawhash", got)
	}
}

func TestNetworkToChainType(t *testing.T) {
	tests := map[string]string{
		"base":             "base",
		"BASE-MAINNET":     "base", // case + trim handled
		"base-sepolia":     "base",
		"arbitrum":         "arbitrum",
		"arbitrum-one":     "arbitrum",
		"ethereum":         "ethereum",
		"mainnet":          "ethereum",
		"  base  ":         "base",
		"some-other-chain": "ethereum", // unknown defaults to ethereum
		"":                 "ethereum",
	}
	for in, want := range tests {
		if got := NetworkToChainType(in); got != want {
			t.Errorf("NetworkToChainType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveContentID(t *testing.T) {
	// Variables take precedence and accept the three key spellings.
	for _, key := range []string{"contentId", "contentID", "content_id"} {
		env := graphqlRequestEnvelope{Variables: map[string]any{key: "cid-" + key}}
		if got := resolveContentID(env); got != "cid-"+key {
			t.Errorf("resolveContentID via %s = %q, want cid-%s", key, got, key)
		}
	}
	// Falls back to parsing the inline query argument when no variable is set.
	env := graphqlRequestEnvelope{Query: `query { resolveViewerEndpoint(contentId: "inline-cid") { url } }`}
	if got := resolveContentID(env); got != "inline-cid" {
		t.Errorf("resolveContentID from query = %q, want inline-cid", got)
	}
	// Nothing to resolve → empty.
	if got := resolveContentID(graphqlRequestEnvelope{}); got != "" {
		t.Errorf("empty envelope → %q, want empty", got)
	}
}

func TestRequireAuth(t *testing.T) {
	// No user in context → error, nil user.
	if user, err := RequireAuth(context.Background()); err == nil || user != nil {
		t.Errorf("RequireAuth(empty) = (%v,%v), want (nil, error)", user, err)
	}
	// User present → returned, no error.
	want := &UserContext{UserID: "u1", TenantID: "t1"}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUser, want)
	got, err := RequireAuth(ctx)
	if err != nil || got != want {
		t.Errorf("RequireAuth(authed) = (%v,%v), want (%v, nil)", got, err, want)
	}
}
