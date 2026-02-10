package chat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ReportData is a local mirror of the heartbeat report record, avoiding an
// import cycle (heartbeat test files import chat).
type ReportData struct {
	ID              string
	Trigger         string
	Summary         string
	MetricsReviewed []string
	RootCause       string
	Recommendations []ReportRecommendation
	CreatedAt       time.Time
	ReadAt          *time.Time
}

type ReportRecommendation struct {
	Text       string
	Confidence string
}

// ReportQuerier is the read/write interface the gRPC server needs for
// investigation reports.  Satisfied by the adapter wired in main.go.
type ReportQuerier interface {
	ListPaginated(ctx context.Context, tenantID string, limit, offset int) ([]ReportData, int, error)
	GetByID(ctx context.Context, tenantID, reportID string) (ReportData, error)
	MarkRead(ctx context.Context, tenantID string, ids []string) (int, error)
	UnreadCount(ctx context.Context, tenantID string) (int, error)
}

// GRPCServerConfig holds dependencies for the gRPC chat server.
type GRPCServerConfig struct {
	Conversations      *ConversationStore
	Orchestrator       *Orchestrator
	UsageLogger        skipper.UsageLogger
	Logger             logging.Logger
	MaxHistoryMessages int
	Reports            ReportQuerier
}

// GRPCServer implements pb.SkipperChatServiceServer.
type GRPCServer struct {
	pb.UnimplementedSkipperChatServiceServer
	conversations      *ConversationStore
	orchestrator       *Orchestrator
	usageLogger        skipper.UsageLogger
	logger             logging.Logger
	maxHistoryMessages int
	reports            ReportQuerier
}

// NewGRPCServer creates a new gRPC server for the Skipper chat service.
func NewGRPCServer(cfg GRPCServerConfig) *GRPCServer {
	maxHistory := cfg.MaxHistoryMessages
	if maxHistory <= 0 {
		maxHistory = 20
	}
	return &GRPCServer{
		conversations:      cfg.Conversations,
		orchestrator:       cfg.Orchestrator,
		usageLogger:        cfg.UsageLogger,
		logger:             cfg.Logger,
		maxHistoryMessages: maxHistory,
		reports:            cfg.Reports,
	}
}

func (s *GRPCServer) Chat(req *pb.SkipperChatRequest, stream grpc.ServerStreamingServer[pb.SkipperChatEvent]) error {
	startedAt := time.Now()
	ctx := stream.Context()

	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if err := requireUserOrService(ctx); err != nil {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	userID := skipper.GetUserID(ctx)

	message := strings.TrimSpace(req.GetMessage())
	if message == "" {
		return status.Error(codes.InvalidArgument, "message is required")
	}

	conversationID := strings.TrimSpace(req.GetConversationId())
	isNewConversation := false
	if conversationID == "" {
		var err error
		conversationID, err = s.conversations.CreateConversation(ctx, tenantID, userID)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to create conversation: %v", err)
		}
		isNewConversation = true
	} else if _, err := s.conversations.GetConversation(ctx, conversationID); err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return status.Error(codes.NotFound, "conversation not found")
		}
		return status.Errorf(codes.Internal, "failed to look up conversation: %v", err)
	}

	history, err := s.conversations.GetRecentMessages(ctx, conversationID, s.maxHistoryMessages)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to load conversation history: %v", err)
	}

	if addErr := s.conversations.AddMessage(ctx, conversationID, "user", message, "", nil, nil, nil, TokenCounts{
		Input:  estimateTokens(message),
		Output: 0,
	}); addErr != nil {
		return status.Errorf(codes.Internal, "failed to persist user message: %v", addErr)
	}

	mode := req.GetMode()
	if mode != "" {
		ctx = skipper.WithMode(ctx, mode)
	}
	summary := ""
	if !isNewConversation && len(history) >= summaryThreshold {
		summary, _ = s.conversations.GetSummary(ctx, conversationID)
	}
	messages := buildPromptMessages(history, message, req.GetPageUrl(), mode, summary)

	streamer := &grpcStreamer{stream: stream}
	result, err := s.orchestrator.Run(ctx, messages, streamer)
	if err != nil {
		s.logger.WithError(err).Warn("Skipper orchestrator failed (gRPC)")
		s.logUsage(ctx, tenantID, userID, conversationID, startedAt, result.TokenCounts, true)
		return status.Errorf(codes.Internal, "orchestrator error: %v", err)
	}

	if metaEvt := buildGRPCMeta(result); metaEvt != nil {
		if sendErr := stream.Send(&pb.SkipperChatEvent{
			Event: &pb.SkipperChatEvent_Meta{Meta: metaEvt},
		}); sendErr != nil {
			return sendErr
		}
	}

	if sendErr := stream.Send(&pb.SkipperChatEvent{
		Event: &pb.SkipperChatEvent_Done{Done: &pb.SkipperChatDone{
			ConversationId: conversationID,
			TokensInput:    int32(result.TokenCounts.Input),
			TokensOutput:   int32(result.TokenCounts.Output),
		}},
	}); sendErr != nil {
		return sendErr
	}

	sourcesJSON, _ := json.Marshal(result.Sources)
	toolData := struct {
		Calls   []ToolCallRecord `json:"calls,omitempty"`
		Details []ToolDetail     `json:"details,omitempty"`
	}{result.ToolCalls, result.Details}
	toolsJSON, _ := json.Marshal(toolData)
	var blocksJSON json.RawMessage
	if len(result.Blocks) > 1 {
		blocksJSON, _ = json.Marshal(result.Blocks)
	}
	if storeErr := s.conversations.AddMessage(ctx, conversationID, "assistant", result.Content, string(result.Confidence), sourcesJSON, toolsJSON, blocksJSON, result.TokenCounts); storeErr != nil {
		s.logger.WithError(storeErr).Warn("Failed to store assistant response (gRPC)")
	}

	if isNewConversation {
		title := truncateTitle(message, 60)
		if titleErr := s.conversations.UpdateTitle(ctx, conversationID, title); titleErr != nil {
			s.logger.WithError(titleErr).Warn("Failed to set conversation title (gRPC)")
		}
	}

	s.logUsage(ctx, tenantID, userID, conversationID, startedAt, result.TokenCounts, false)
	metering.RecordLLMUsage(ctx, result.TokenCounts.Input, result.TokenCounts.Output)

	return nil
}

func (s *GRPCServer) ListConversations(ctx context.Context, req *pb.ListSkipperConversationsRequest) (*pb.ListSkipperConversationsResponse, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if err := requireUserOrService(ctx); err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}
	userID := skipper.GetUserID(ctx)

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 50
	}
	offset := int(req.GetOffset())

	summaries, err := s.conversations.ListConversations(ctx, tenantID, userID, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list conversations: %v", err)
	}

	out := make([]*pb.SkipperConversationSummary, 0, len(summaries))
	for _, sum := range summaries {
		out = append(out, &pb.SkipperConversationSummary{
			Id:        sum.ID,
			Title:     sum.Title,
			CreatedAt: timestamppb.New(sum.CreatedAt),
			UpdatedAt: timestamppb.New(sum.UpdatedAt),
		})
	}
	return &pb.ListSkipperConversationsResponse{Conversations: out}, nil
}

func (s *GRPCServer) GetConversation(ctx context.Context, req *pb.GetSkipperConversationRequest) (*pb.SkipperConversationDetail, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if err := requireUserOrService(ctx); err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation id is required")
	}

	convo, err := s.conversations.GetConversation(ctx, id)
	if errors.Is(err, ErrConversationNotFound) {
		return nil, status.Error(codes.NotFound, "conversation not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get conversation: %v", err)
	}

	msgs := make([]*pb.SkipperChatMessage, 0, len(convo.Messages))
	for _, m := range convo.Messages {
		msg := &pb.SkipperChatMessage{
			Id:               m.ID,
			Role:             m.Role,
			Content:          m.Content,
			Confidence:       m.Confidence,
			SourcesJson:      string(m.Sources),
			ToolsUsedJson:    string(m.ToolsUsed),
			TokenCountInput:  int32(m.TokenCountInput),
			TokenCountOutput: int32(m.TokenCountOutput),
			CreatedAt:        timestamppb.New(m.CreatedAt),
		}
		if len(m.ConfidenceBlocks) > 0 && string(m.ConfidenceBlocks) != "null" {
			msg.ConfidenceBlocksJson = string(m.ConfidenceBlocks)
		}
		msgs = append(msgs, msg)
	}

	return &pb.SkipperConversationDetail{
		Id:        convo.ID,
		Title:     convo.Title,
		Messages:  msgs,
		CreatedAt: timestamppb.New(convo.CreatedAt),
		UpdatedAt: timestamppb.New(convo.UpdatedAt),
	}, nil
}

func (s *GRPCServer) DeleteConversation(ctx context.Context, req *pb.DeleteSkipperConversationRequest) (*pb.DeleteSkipperConversationResponse, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if err := requireUserOrService(ctx); err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation id is required")
	}

	if err := s.conversations.DeleteConversation(ctx, id); err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to delete conversation: %v", err)
	}

	return &pb.DeleteSkipperConversationResponse{}, nil
}

func (s *GRPCServer) UpdateConversationTitle(ctx context.Context, req *pb.UpdateSkipperConversationTitleRequest) (*pb.SkipperConversationSummary, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if err := requireUserOrService(ctx); err != nil {
		return nil, status.Error(codes.Unauthenticated, err.Error())
	}

	id := req.GetId()
	title := strings.TrimSpace(req.GetTitle())
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "conversation id is required")
	}
	if title == "" {
		return nil, status.Error(codes.InvalidArgument, "title is required")
	}

	if err := s.conversations.UpdateTitle(ctx, id, title); err != nil {
		if errors.Is(err, ErrConversationNotFound) {
			return nil, status.Error(codes.NotFound, "conversation not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to update conversation: %v", err)
	}

	convo, getErr := s.conversations.GetConversation(ctx, id)
	if getErr != nil {
		return nil, status.Errorf(codes.Internal, "failed to load conversation: %v", getErr)
	}
	return &pb.SkipperConversationSummary{
		Id:        id,
		Title:     title,
		CreatedAt: timestamppb.New(convo.CreatedAt),
		UpdatedAt: timestamppb.New(convo.UpdatedAt),
	}, nil
}

func (s *GRPCServer) logUsage(ctx context.Context, tenantID, userID, conversationID string, startedAt time.Time, tokens TokenCounts, hadError bool) {
	if s.usageLogger == nil {
		return
	}
	s.usageLogger.LogChatUsage(ctx, skipper.ChatUsageEvent{
		TenantID:       tenantID,
		UserID:         userID,
		ConversationID: conversationID,
		StartedAt:      startedAt,
		TokensIn:       tokens.Input,
		TokensOut:      tokens.Output,
		HadError:       hadError,
	})
}

// grpcStreamer adapts the gRPC server stream to TokenStreamer + ToolEventStreamer.
type grpcStreamer struct {
	stream grpc.ServerStreamingServer[pb.SkipperChatEvent]
}

func (s *grpcStreamer) SendToken(token string) error {
	return s.stream.Send(&pb.SkipperChatEvent{
		Event: &pb.SkipperChatEvent_Token{
			Token: &pb.SkipperTokenChunk{Content: token},
		},
	})
}

func (s *grpcStreamer) SendToolStart(toolName string) error {
	return s.stream.Send(&pb.SkipperChatEvent{
		Event: &pb.SkipperChatEvent_ToolStart{
			ToolStart: &pb.SkipperToolStart{ToolName: toolName},
		},
	})
}

func (s *grpcStreamer) SendToolEnd(toolName string, errMsg string) error {
	return s.stream.Send(&pb.SkipperChatEvent{
		Event: &pb.SkipperChatEvent_ToolEnd{
			ToolEnd: &pb.SkipperToolEnd{ToolName: toolName, Error: errMsg},
		},
	})
}

// bridgeAuthContext copies auth values set by the gRPC auth middleware (ctxkeys)
// into the skipper-specific context keys used by the orchestrator and conversation store.
func bridgeAuthContext(ctx context.Context) context.Context {
	if v := ctxkeys.GetTenantID(ctx); v != "" {
		ctx = skipper.WithTenantID(ctx, v)
	}
	if v := ctxkeys.GetUserID(ctx); v != "" {
		ctx = skipper.WithUserID(ctx, v)
	}
	if v := ctxkeys.GetJWTToken(ctx); v != "" {
		ctx = skipper.WithJWTToken(ctx, v)
	}
	if v := ctxkeys.GetRole(ctx); v != "" {
		ctx = skipper.WithRole(ctx, v)
	}
	if v := ctxkeys.GetAuthType(ctx); v != "" {
		ctx = skipper.WithAuthType(ctx, v)
	}
	return ctx
}

func buildGRPCMeta(result OrchestratorResult) *pb.SkipperChatMeta {
	citations := make([]*pb.SkipperCitation, 0)
	external := make([]*pb.SkipperCitation, 0)
	for _, source := range result.Sources {
		if source.URL == "" {
			continue
		}
		item := &pb.SkipperCitation{Label: source.Title, Url: source.URL}
		switch source.Type {
		case SourceTypeKnowledgeBase:
			citations = append(citations, item)
		case SourceTypeWeb:
			external = append(external, item)
		}
	}
	if len(citations) == 0 && len(external) == 0 {
		for _, source := range result.Sources {
			if source.URL == "" {
				continue
			}
			citations = append(citations, &pb.SkipperCitation{Label: source.Title, Url: source.URL})
		}
	}

	details := make([]*pb.SkipperToolDetail, 0, len(result.Details))
	for _, d := range result.Details {
		payload, err := toStruct(d.Payload)
		if err != nil {
			continue
		}
		details = append(details, &pb.SkipperToolDetail{
			Title:   d.Title,
			Payload: payload,
		})
	}

	meta := &pb.SkipperChatMeta{
		Confidence:    string(result.Confidence),
		Citations:     citations,
		ExternalLinks: external,
		Details:       details,
	}
	if len(result.Blocks) > 1 {
		for _, b := range result.Blocks {
			pbBlock := &pb.SkipperConfidenceBlock{
				Content:    b.Content,
				Confidence: string(b.Confidence),
			}
			for _, s := range b.Sources {
				if s.URL != "" {
					pbBlock.Sources = append(pbBlock.Sources, &pb.SkipperCitation{
						Label: s.Title, Url: s.URL,
					})
				}
			}
			meta.Blocks = append(meta.Blocks, pbBlock)
		}
	}
	return meta
}

func toStruct(v any) (*structpb.Struct, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return structpb.NewStruct(m)
}

// ---------------------------------------------------------------------------
// Investigation reports
// ---------------------------------------------------------------------------

func (s *GRPCServer) ListReports(ctx context.Context, req *pb.ListSkipperReportsRequest) (*pb.ListSkipperReportsResponse, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if s.reports == nil {
		return nil, status.Error(codes.Unavailable, "report store unavailable")
	}

	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = 20
	}
	offset := int(req.GetOffset())

	reports, total, err := s.reports.ListPaginated(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list reports: %v", err)
	}

	unread, err := s.reports.UnreadCount(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count unread: %v", err)
	}

	out := make([]*pb.SkipperReport, 0, len(reports))
	for _, r := range reports {
		out = append(out, reportDataToProto(r))
	}
	return &pb.ListSkipperReportsResponse{
		Reports:     out,
		TotalCount:  int32(total),
		UnreadCount: int32(unread),
	}, nil
}

func (s *GRPCServer) GetReport(ctx context.Context, req *pb.GetSkipperReportRequest) (*pb.SkipperReport, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if s.reports == nil {
		return nil, status.Error(codes.Unavailable, "report store unavailable")
	}

	id := req.GetId()
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "report id is required")
	}

	r, err := s.reports.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get report: %v", err)
	}
	return reportDataToProto(r), nil
}

func (s *GRPCServer) MarkReportsRead(ctx context.Context, req *pb.MarkSkipperReportsReadRequest) (*pb.MarkSkipperReportsReadResponse, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if s.reports == nil {
		return nil, status.Error(codes.Unavailable, "report store unavailable")
	}

	n, err := s.reports.MarkRead(ctx, tenantID, req.GetIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "mark read: %v", err)
	}
	return &pb.MarkSkipperReportsReadResponse{MarkedCount: int32(n)}, nil
}

func (s *GRPCServer) GetUnreadReportCount(ctx context.Context, _ *pb.GetUnreadReportCountRequest) (*pb.GetUnreadReportCountResponse, error) {
	ctx = bridgeAuthContext(ctx)
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return nil, status.Error(codes.Unauthenticated, "tenant_id missing")
	}
	if s.reports == nil {
		return nil, status.Error(codes.Unavailable, "report store unavailable")
	}

	count, err := s.reports.UnreadCount(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "count unread: %v", err)
	}
	return &pb.GetUnreadReportCountResponse{Count: int32(count)}, nil
}

func reportDataToProto(r ReportData) *pb.SkipperReport {
	recs := make([]*pb.SkipperReportRecommendation, 0, len(r.Recommendations))
	for _, rec := range r.Recommendations {
		recs = append(recs, &pb.SkipperReportRecommendation{
			Text:       rec.Text,
			Confidence: rec.Confidence,
		})
	}
	out := &pb.SkipperReport{
		Id:              r.ID,
		Trigger:         r.Trigger,
		Summary:         r.Summary,
		MetricsReviewed: r.MetricsReviewed,
		RootCause:       r.RootCause,
		Recommendations: recs,
		CreatedAt:       timestamppb.New(r.CreatedAt),
	}
	if r.ReadAt != nil {
		out.ReadAt = timestamppb.New(*r.ReadAt)
	}
	return out
}
