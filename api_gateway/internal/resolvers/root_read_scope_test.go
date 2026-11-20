package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestTenantReadScopesRejectUnrelatedBillingToken(t *testing.T) {
	r := &Resolver{Logger: logging.NewLogger(), Clients: &clients.ServiceClients{}}
	cases := []struct {
		name, scope string
		call        func(context.Context) error
	}{
		{"tenant", "account:read", func(ctx context.Context) error { _, err := r.DoGetTenant(ctx); return err }},
		{"validateStreamKey", "streams:read", func(ctx context.Context) error { _, err := r.DoValidateStreamKey(ctx, "key"); return err }},
		{"streamingConfig", "streams:read", func(ctx context.Context) error { _, err := r.DoGetStreamingConfig(ctx); return err }},
		{"clustersAccess", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoGetClustersAccess(ctx, nil, nil); return err }},
		{"clustersAccessConnection", "infrastructure:read", func(ctx context.Context) error {
			_, err := r.DoGetClustersAccessConnection(ctx, nil, nil, nil, nil)
			return err
		}},
		{"clustersAvailable", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoGetClustersAvailable(ctx, nil, nil); return err }},
		{"clustersAvailableConnection", "infrastructure:read", func(ctx context.Context) error {
			_, err := r.DoGetClustersAvailableConnection(ctx, nil, nil, nil, nil)
			return err
		}},
		{"mySubscriptions", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoListMySubscriptions(ctx, nil, nil); return err }},
		{"mySubscriptionsConnection", "infrastructure:read", func(ctx context.Context) error {
			_, err := r.DoGetMySubscriptionsConnection(ctx, nil, nil, nil, nil)
			return err
		}},
		{"marketplaceClusters", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoListMarketplaceClusters(ctx, nil, nil); return err }},
		{"marketplaceCluster", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoGetMarketplaceCluster(ctx, "cluster", nil); return err }},
		{"marketplaceClustersConnection", "infrastructure:read", func(ctx context.Context) error {
			_, err := r.DoGetMarketplaceClustersConnection(ctx, nil, nil, nil, nil)
			return err
		}},
		{"myClusterInvites", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoListMyClusterInvites(ctx); return err }},
		{"myClusterInvitesConnection", "infrastructure:read", func(ctx context.Context) error {
			_, err := r.DoGetMyClusterInvitesConnection(ctx, nil, nil, nil, nil)
			return err
		}},
		{"skipperChat", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperChat(ctx, model.SkipperChatInput{}); return err }},
		{"skipperConversations", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperConversations(ctx, nil, nil); return err }},
		{"skipperConversation", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperConversation(ctx, "conversation"); return err }},
		{"skipperReports", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperReports(ctx, nil, nil); return err }},
		{"skipperReport", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperReport(ctx, "report"); return err }},
		{"skipperUnreadReportCount", "consultant:use", func(ctx context.Context) error { _, err := r.DoSkipperUnreadReportCount(ctx); return err }},
		{"conversationsConnection", "support:read", func(ctx context.Context) error { _, err := r.ConversationsConnection(ctx, nil); return err }},
		{"conversation", "support:read", func(ctx context.Context) error { _, err := r.Conversation(ctx, "conversation"); return err }},
		{"messagesConnection", "support:read", func(ctx context.Context) error { _, err := r.MessagesConnection(ctx, "conversation", nil); return err }},
		{"liveSystemHealth", "infrastructure:read", func(ctx context.Context) error { _, err := r.DoSystemUpdates(ctx); return err }},
		{"liveMessageReceived", "support:read", func(ctx context.Context) error { _, err := r.DoMessageUpdates(ctx, "conversation"); return err }},
		{"liveConversationUpdates", "support:read", func(ctx context.Context) error { _, err := r.DoConversationUpdates(ctx, nil); return err }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { wantScopeError(t, test.call(streamScopeAPITokenCtx("billing:read")), test.scope) })
	}
}

func TestWalletMutationsRequireSecurityWrite(t *testing.T) {
	resolver := &Resolver{}
	_, err := resolver.DoLinkWallet(streamScopeAPITokenCtx("billing:read"), model.WalletLoginInput{})
	wantScopeError(t, err, "security:write")
	_, err = resolver.DoUnlinkWallet(streamScopeAPITokenCtx("billing:read"), "wallet")
	wantScopeError(t, err, "security:write")
}
