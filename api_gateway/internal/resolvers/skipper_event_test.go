package resolvers

import (
	"testing"

	"frameworks/api_gateway/graph/model"

	skipperpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/skipper"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestConvertSkipperEvent_Token(t *testing.T) {
	got := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_Token{Token: &skipperpb.SkipperTokenChunk{Content: "hi"}},
	})
	tok, ok := got.(model.SkipperToken)
	if !ok || tok.Content != "hi" {
		t.Fatalf("expected SkipperToken{hi}, got %T %+v", got, got)
	}
}

func TestConvertSkipperEvent_ToolStart(t *testing.T) {
	got := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_ToolStart{ToolStart: &skipperpb.SkipperToolStart{ToolName: "search"}},
	})
	if ts, ok := got.(model.SkipperToolStartEvent); !ok || ts.Tool != "search" {
		t.Fatalf("expected SkipperToolStartEvent{search}, got %T %+v", got, got)
	}
}

// ToolEnd maps Error only when non-empty (the *string stays nil otherwise).
func TestConvertSkipperEvent_ToolEnd(t *testing.T) {
	noErr := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_ToolEnd{ToolEnd: &skipperpb.SkipperToolEnd{ToolName: "search"}},
	}).(model.SkipperToolEndEvent)
	if noErr.Tool != "search" || noErr.Error != nil {
		t.Fatalf("expected nil error pointer, got %+v", noErr)
	}
	withErr := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_ToolEnd{ToolEnd: &skipperpb.SkipperToolEnd{ToolName: "search", Error: "boom"}},
	}).(model.SkipperToolEndEvent)
	if withErr.Error == nil || *withErr.Error != "boom" {
		t.Fatalf("expected error 'boom', got %+v", withErr.Error)
	}
}

func TestConvertSkipperEvent_Done(t *testing.T) {
	got := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_Done{Done: &skipperpb.SkipperChatDone{
			ConversationId: "c1", TokensInput: 12, TokensOutput: 34,
		}},
	})
	done, ok := got.(model.SkipperDone)
	if !ok || done.ConversationID != "c1" || done.TokensInput != 12 || done.TokensOutput != 34 {
		t.Fatalf("expected SkipperDone, got %T %+v", got, got)
	}
}

// Meta maps citations/external links/details, and only materializes confidence
// blocks when there is MORE THAN ONE (a single block is treated as the whole answer).
func TestConvertSkipperEvent_Meta(t *testing.T) {
	payload, _ := structpb.NewStruct(map[string]any{"k": "v"})
	got := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_Meta{Meta: &skipperpb.SkipperChatMeta{
			Confidence:    "0.9",
			Citations:     []*skipperpb.SkipperCitation{{Label: "docA", Url: "http://a"}},
			ExternalLinks: []*skipperpb.SkipperCitation{{Label: "ext", Url: "http://e"}},
			Details:       []*skipperpb.SkipperToolDetail{{Title: "d1", Payload: payload}, {Title: "d2"}},
			Blocks: []*skipperpb.SkipperConfidenceBlock{
				{Content: "b1", Confidence: "0.5", Sources: []*skipperpb.SkipperCitation{{Label: "s1", Url: "http://s1"}}},
				{Content: "b2", Confidence: "0.8"},
			},
		}},
	})
	meta, ok := got.(model.SkipperMeta)
	if !ok {
		t.Fatalf("expected SkipperMeta, got %T", got)
	}
	if meta.Confidence != "0.9" {
		t.Errorf("confidence = %v, want 0.9", meta.Confidence)
	}
	if len(meta.Citations) != 1 || meta.Citations[0].Label != "docA" {
		t.Errorf("citations not mapped: %+v", meta.Citations)
	}
	if len(meta.ExternalLinks) != 1 || meta.ExternalLinks[0].URL != "http://e" {
		t.Errorf("external links not mapped: %+v", meta.ExternalLinks)
	}
	if len(meta.Details) != 2 || meta.Details[0].Payload == nil || meta.Details[1].Payload != nil {
		t.Errorf("details/payload not mapped (payload set only when present): %+v", meta.Details)
	}
	if len(meta.Blocks) != 2 || meta.Blocks[0].Content != "b1" || len(meta.Blocks[0].Sources) != 1 {
		t.Errorf("blocks (>1) not materialized: %+v", meta.Blocks)
	}
}

// A single confidence block is intentionally NOT materialized into Blocks.
func TestConvertSkipperEvent_Meta_SingleBlockSuppressed(t *testing.T) {
	meta := convertSkipperEvent(&skipperpb.SkipperChatEvent{
		Event: &skipperpb.SkipperChatEvent_Meta{Meta: &skipperpb.SkipperChatMeta{
			Blocks: []*skipperpb.SkipperConfidenceBlock{{Content: "only"}},
		}},
	}).(model.SkipperMeta)
	if meta.Blocks != nil {
		t.Fatalf("a single block should not be materialized, got %+v", meta.Blocks)
	}
}

// An unknown/empty event oneof yields nil (the channel relay drops it).
func TestConvertSkipperEvent_Unknown(t *testing.T) {
	if got := convertSkipperEvent(&skipperpb.SkipperChatEvent{}); got != nil {
		t.Fatalf("expected nil for empty event, got %T %+v", got, got)
	}
}
