package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"
)

// DoSkipperChat opens a streaming gRPC Chat call to Skipper and relays
// events to a channel suitable for a GraphQL subscription.
func (r *Resolver) DoSkipperChat(ctx context.Context, input model.SkipperChatInput) (<-chan model.SkipperChatEvent, error) {
	if r.Clients.Skipper == nil {
		return nil, fmt.Errorf("skipper service unavailable")
	}
	user, err := middleware.RequireAuth(ctx)
	if err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}

	// Build the gRPC request with user context already on ctx (from middleware).
	req := &pb.SkipperChatRequest{
		Message: input.Message,
	}
	if input.ConversationID != nil {
		req.ConversationId = *input.ConversationID
	}
	if input.PageURL != nil {
		req.PageUrl = *input.PageURL
	}
	if input.Mode != nil {
		switch *input.Mode {
		case model.SkipperModeDocs:
			req.Mode = "docs"
		}
	}

	_ = user // auth context is already propagated via gRPC interceptor

	stream, err := r.Clients.Skipper.Chat(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to start skipper chat: %w", err)
	}

	ch := make(chan model.SkipperChatEvent, 32)
	go func() {
		defer close(ch)
		for {
			evt, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					r.Logger.WithError(err).Warn("Skipper gRPC stream error")
				}
				return
			}
			gqlEvt := convertSkipperEvent(evt)
			if gqlEvt == nil {
				continue
			}
			select {
			case ch <- gqlEvt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func convertSkipperEvent(evt *pb.SkipperChatEvent) model.SkipperChatEvent {
	switch e := evt.GetEvent().(type) {
	case *pb.SkipperChatEvent_Token:
		return model.SkipperToken{Content: e.Token.GetContent()}
	case *pb.SkipperChatEvent_ToolStart:
		return model.SkipperToolStartEvent{Tool: e.ToolStart.GetToolName()}
	case *pb.SkipperChatEvent_ToolEnd:
		m := model.SkipperToolEndEvent{Tool: e.ToolEnd.GetToolName()}
		if e.ToolEnd.GetError() != "" {
			s := e.ToolEnd.GetError()
			m.Error = &s
		}
		return m
	case *pb.SkipperChatEvent_Meta:
		meta := e.Meta
		citations := make([]*model.SkipperCitation, 0, len(meta.GetCitations()))
		for _, c := range meta.GetCitations() {
			citations = append(citations, &model.SkipperCitation{Label: c.GetLabel(), URL: c.GetUrl()})
		}
		external := make([]*model.SkipperCitation, 0, len(meta.GetExternalLinks()))
		for _, c := range meta.GetExternalLinks() {
			external = append(external, &model.SkipperCitation{Label: c.GetLabel(), URL: c.GetUrl()})
		}
		details := make([]*model.SkipperToolDet, 0, len(meta.GetDetails()))
		for _, d := range meta.GetDetails() {
			var payload any
			if d.GetPayload() != nil {
				payload = d.GetPayload().AsMap()
			}
			details = append(details, &model.SkipperToolDet{Title: d.GetTitle(), Payload: payload})
		}
		return model.SkipperMeta{
			Confidence:    meta.GetConfidence(),
			Citations:     citations,
			ExternalLinks: external,
			Details:       details,
		}
	case *pb.SkipperChatEvent_Done:
		return model.SkipperDone{
			ConversationID: e.Done.GetConversationId(),
			TokensInput:    int(e.Done.GetTokensInput()),
			TokensOutput:   int(e.Done.GetTokensOutput()),
		}
	default:
		return nil
	}
}

// DoSkipperConversations lists Skipper conversations for the current user.
func (r *Resolver) DoSkipperConversations(ctx context.Context, limit *int, offset *int) ([]*model.SkipperConversationSummary, error) {
	if r.Clients.Skipper == nil {
		return nil, fmt.Errorf("skipper service unavailable")
	}
	if _, err := middleware.RequireAuth(ctx); err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}

	l := int32(50)
	if limit != nil && *limit > 0 {
		l = int32(*limit)
	}
	o := int32(0)
	if offset != nil && *offset > 0 {
		o = int32(*offset)
	}
	resp, err := r.Clients.Skipper.ListConversations(ctx, l, o)
	if err != nil {
		return nil, fmt.Errorf("failed to list conversations: %w", err)
	}

	out := make([]*model.SkipperConversationSummary, 0, len(resp.GetConversations()))
	for _, c := range resp.GetConversations() {
		out = append(out, &model.SkipperConversationSummary{
			ID:        c.GetId(),
			Title:     c.GetTitle(),
			CreatedAt: c.GetCreatedAt().AsTime(),
			UpdatedAt: c.GetUpdatedAt().AsTime(),
		})
	}
	return out, nil
}

// DoSkipperConversation gets a single Skipper conversation with messages.
func (r *Resolver) DoSkipperConversation(ctx context.Context, id string) (*model.SkipperConversation, error) {
	if r.Clients.Skipper == nil {
		return nil, fmt.Errorf("skipper service unavailable")
	}
	if _, err := middleware.RequireAuth(ctx); err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}

	resp, err := r.Clients.Skipper.GetConversation(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get conversation: %w", err)
	}

	msgs := make([]*model.SkipperMessage, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		msg := &model.SkipperMessage{
			ID:           m.GetId(),
			Role:         m.GetRole(),
			Content:      m.GetContent(),
			TokensInput:  int(m.GetTokenCountInput()),
			TokensOutput: int(m.GetTokenCountOutput()),
			CreatedAt:    m.GetCreatedAt().AsTime(),
		}
		if conf := m.GetConfidence(); conf != "" {
			msg.Confidence = &conf
		}
		if src := m.GetSourcesJson(); src != "" {
			var parsed any
			if json.Unmarshal([]byte(src), &parsed) == nil {
				msg.Sources = parsed
			}
		}
		if tools := m.GetToolsUsedJson(); tools != "" {
			var parsed any
			if json.Unmarshal([]byte(tools), &parsed) == nil {
				msg.ToolsUsed = parsed
			}
		}
		msgs = append(msgs, msg)
	}

	return &model.SkipperConversation{
		ID:        resp.GetId(),
		Title:     resp.GetTitle(),
		Messages:  msgs,
		CreatedAt: resp.GetCreatedAt().AsTime(),
		UpdatedAt: resp.GetUpdatedAt().AsTime(),
	}, nil
}

// DoDeleteSkipperConversation deletes a Skipper conversation.
func (r *Resolver) DoDeleteSkipperConversation(ctx context.Context, id string) (bool, error) {
	if r.Clients.Skipper == nil {
		return false, fmt.Errorf("skipper service unavailable")
	}
	if _, err := middleware.RequireAuth(ctx); err != nil {
		return false, fmt.Errorf("authentication required: %w", err)
	}

	if _, err := r.Clients.Skipper.DeleteConversation(ctx, id); err != nil {
		return false, fmt.Errorf("failed to delete conversation: %w", err)
	}
	return true, nil
}

// DoUpdateSkipperConversation updates the title of a Skipper conversation.
func (r *Resolver) DoUpdateSkipperConversation(ctx context.Context, id, title string) (*model.SkipperConversationSummary, error) {
	if r.Clients.Skipper == nil {
		return nil, fmt.Errorf("skipper service unavailable")
	}
	if _, err := middleware.RequireAuth(ctx); err != nil {
		return nil, fmt.Errorf("authentication required: %w", err)
	}

	resp, err := r.Clients.Skipper.UpdateConversationTitle(ctx, id, title)
	if err != nil {
		return nil, fmt.Errorf("failed to update conversation: %w", err)
	}
	return &model.SkipperConversationSummary{
		ID:        resp.GetId(),
		Title:     resp.GetTitle(),
		CreatedAt: resp.GetCreatedAt().AsTime(),
		UpdatedAt: resp.GetUpdatedAt().AsTime(),
	}, nil
}
