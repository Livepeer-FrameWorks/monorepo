package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
)

func TestSupportMutationsNeedWriteScope(t *testing.T) {
	resolver := &Resolver{Clients: &clients.ServiceClients{}}
	for _, scope := range []string{"support:read", "billing:write", "streams:write"} {
		for _, call := range []func(context.Context) error{
			func(ctx context.Context) error {
				_, err := resolver.CreateConversation(ctx, model.CreateConversationInput{})
				return err
			},
			func(ctx context.Context) error {
				_, err := resolver.SendMessage(ctx, model.SendMessageInput{})
				return err
			},
		} {
			wantScopeError(t, call(streamScopeAPITokenCtx(scope)), "support:write")
		}
	}
	for _, call := range []func(context.Context) error{
		func(ctx context.Context) error {
			_, err := resolver.CreateConversation(ctx, model.CreateConversationInput{})
			return err
		},
		func(ctx context.Context) error {
			_, err := resolver.SendMessage(ctx, model.SendMessageInput{})
			return err
		},
	} {
		if err := call(streamScopeAPITokenCtx("support:write")); err != nil {
			t.Fatalf("support writer denied before backend validation: %v", err)
		}
	}
}
