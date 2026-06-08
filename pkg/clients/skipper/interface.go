package skipper

import (
	"context"
	skipperpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/skipper"
	"google.golang.org/grpc"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	Chat(ctx context.Context, req *skipperpb.SkipperChatRequest) (grpc.ServerStreamingClient[skipperpb.SkipperChatEvent], error)
	ListConversations(ctx context.Context, limit, offset int32) (*skipperpb.ListSkipperConversationsResponse, error)
	GetConversation(ctx context.Context, id string) (*skipperpb.SkipperConversationDetail, error)
	DeleteConversation(ctx context.Context, id string) (*skipperpb.DeleteSkipperConversationResponse, error)
	UpdateConversationTitle(ctx context.Context, id, title string) (*skipperpb.SkipperConversationSummary, error)
	ListReports(ctx context.Context, limit, offset int32) (*skipperpb.ListSkipperReportsResponse, error)
	GetReport(ctx context.Context, id string) (*skipperpb.SkipperReport, error)
	MarkReportsRead(ctx context.Context, ids []string) (*skipperpb.MarkSkipperReportsReadResponse, error)
	GetUnreadReportCount(ctx context.Context) (*skipperpb.GetUnreadReportCountResponse, error)
}

var _ Interface = (*GRPCClient)(nil)
