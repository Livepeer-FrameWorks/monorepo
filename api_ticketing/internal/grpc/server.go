package grpc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/api_ticketing/internal/chatwoot"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ServerMetrics holds Prometheus metrics for the gRPC server
type ServerMetrics struct {
	GRPCRequests *prometheus.CounterVec
	GRPCDuration *prometheus.HistogramVec
}

// Config holds configuration for the gRPC server
type Config struct {
	Logger          logging.Logger
	Metrics         *ServerMetrics
	ChatwootBaseURL string
	ChatwootToken   string
	ChatwootAccount int
	ChatwootInbox   int
	Quartermaster   *qmclient.GRPCClient
	Purser          *purserclient.GRPCClient
}

// Server implements the DeckhandService gRPC server
type Server struct {
	pb.UnimplementedDeckhandServiceServer
	logger        logging.Logger
	metrics       *ServerMetrics
	chatwoot      *chatwoot.Client
	quartermaster *qmclient.GRPCClient
	purser        *purserclient.GRPCClient
}

// NewServer creates a new Deckhand gRPC server
func NewServer(cfg Config) *Server {
	return &Server{
		logger:  cfg.Logger,
		metrics: cfg.Metrics,
		chatwoot: chatwoot.NewClient(chatwoot.Config{
			BaseURL:   cfg.ChatwootBaseURL,
			APIToken:  cfg.ChatwootToken,
			AccountID: cfg.ChatwootAccount,
			InboxID:   cfg.ChatwootInbox,
		}),
		quartermaster: cfg.Quartermaster,
		purser:        cfg.Purser,
	}
}

// ListConversations returns all conversations for the authenticated tenant
func (s *Server) ListConversations(ctx context.Context, req *pb.ListConversationsRequest) (*pb.ListConversationsResponse, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("ListConversations").Observe(time.Since(start).Seconds())
	}()

	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		s.metrics.GRPCRequests.WithLabelValues("ListConversations", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	// Find contact by tenant_id (source_id in Chatwoot)
	contact, err := s.chatwoot.GetContactBySourceID(ctx, tenantID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to find contact")
		s.metrics.GRPCRequests.WithLabelValues("ListConversations", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to find contact")
	}
	if contact == nil {
		// No contact means no conversations
		s.metrics.GRPCRequests.WithLabelValues("ListConversations", "ok").Inc()
		return &pb.ListConversationsResponse{
			Conversations: []*pb.DeckhandConversation{},
			TotalCount:    0,
		}, nil
	}

	// Get conversations for contact
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	perPage := int(req.GetPerPage())
	if perPage < 1 {
		perPage = 20
	}

	convs, total, err := s.chatwoot.ListConversations(ctx, contact.ID, page, perPage)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list conversations")
		s.metrics.GRPCRequests.WithLabelValues("ListConversations", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to list conversations")
	}

	// Convert to proto
	result := make([]*pb.DeckhandConversation, len(convs))
	for i, conv := range convs {
		result[i] = s.chatwootConvToProto(&conv)
	}

	s.metrics.GRPCRequests.WithLabelValues("ListConversations", "ok").Inc()
	return &pb.ListConversationsResponse{
		Conversations: result,
		TotalCount:    int32(total),
	}, nil
}

// SearchConversations searches conversations for the authenticated tenant.
func (s *Server) SearchConversations(ctx context.Context, req *pb.SearchConversationsRequest) (*pb.SearchConversationsResponse, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("SearchConversations").Observe(time.Since(start).Seconds())
	}()

	tenantID := req.GetTenantId()
	if tenantID == "" {
		tenantID = middleware.GetTenantID(ctx)
	}
	if tenantID == "" {
		s.metrics.GRPCRequests.WithLabelValues("SearchConversations", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "tenant_id required")
	}

	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		s.metrics.GRPCRequests.WithLabelValues("SearchConversations", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "query required")
	}

	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	perPage := int(req.GetPerPage())
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 100 {
		perPage = 100
	}

	// Offset within matched results to support paging.
	skip := (page - 1) * perPage
	results := make([]*pb.DeckhandConversation, 0, perPage)

	const maxPages = 20
	for chatwootPage := 1; chatwootPage <= maxPages; chatwootPage++ {
		convs, _, err := s.chatwoot.SearchConversations(ctx, query, chatwootPage)
		if err != nil {
			s.logger.WithError(err).Error("Failed to search conversations")
			s.metrics.GRPCRequests.WithLabelValues("SearchConversations", "error").Inc()
			return nil, status.Error(codes.Internal, "failed to search conversations")
		}
		if len(convs) == 0 {
			break
		}

		for _, conv := range convs {
			if err := s.verifyConversationTenant(ctx, &conv); err != nil {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			results = append(results, s.chatwootConvToProto(&conv))
			if len(results) >= perPage {
				break
			}
		}

		if len(results) >= perPage {
			break
		}
	}

	s.metrics.GRPCRequests.WithLabelValues("SearchConversations", "ok").Inc()
	return &pb.SearchConversationsResponse{
		Conversations: results,
		TotalCount:    int32(len(results)),
	}, nil
}

// GetConversation returns a single conversation by ID
func (s *Server) GetConversation(ctx context.Context, req *pb.GetConversationRequest) (*pb.DeckhandConversation, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("GetConversation").Observe(time.Since(start).Seconds())
	}()

	convID, err := strconv.ParseInt(req.GetConversationId(), 10, 64)
	if err != nil {
		s.metrics.GRPCRequests.WithLabelValues("GetConversation", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "invalid conversation_id")
	}

	conv, err := s.chatwoot.GetConversation(ctx, convID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to get conversation")
		s.metrics.GRPCRequests.WithLabelValues("GetConversation", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to get conversation")
	}
	if conv == nil {
		s.metrics.GRPCRequests.WithLabelValues("GetConversation", "not_found").Inc()
		return nil, status.Error(codes.NotFound, "conversation not found")
	}

	if err := s.verifyConversationTenant(ctx, conv); err != nil {
		s.metrics.GRPCRequests.WithLabelValues("GetConversation", "forbidden").Inc()
		return nil, err
	}

	s.metrics.GRPCRequests.WithLabelValues("GetConversation", "ok").Inc()
	return s.chatwootConvToProto(conv), nil
}

// CreateConversation creates a new conversation
func (s *Server) CreateConversation(ctx context.Context, req *pb.CreateConversationRequest) (*pb.DeckhandConversation, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("CreateConversation").Observe(time.Since(start).Seconds())
	}()

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		s.metrics.GRPCRequests.WithLabelValues("CreateConversation", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "tenant_id required in context")
	}

	// Find or create contact with tenant-enriched identity
	contactName, contactEmail := s.lookupTenantIdentity(ctx, tenantID)
	if contactName == "" {
		contactName = tenantID
	}
	contact, err := s.chatwoot.FindOrCreateContact(ctx, tenantID, contactName, contactEmail)
	if err != nil {
		s.logger.WithError(err).Error("Failed to find/create contact")
		s.metrics.GRPCRequests.WithLabelValues("CreateConversation", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to create contact")
	}

	// Create conversation
	customAttrs := req.GetCustomAttributes()
	if customAttrs == nil {
		customAttrs = make(map[string]string)
	}
	customAttrs["tenant_id"] = tenantID

	conv, err := s.chatwoot.CreateConversation(ctx, contact.ID, req.GetSubject(), customAttrs)
	if err != nil {
		s.logger.WithError(err).Error("Failed to create conversation")
		s.metrics.GRPCRequests.WithLabelValues("CreateConversation", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to create conversation")
	}

	// Send initial message if provided
	if req.GetInitialMessage() != "" {
		_, err := s.chatwoot.SendMessage(ctx, conv.ID, req.GetInitialMessage(), false)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to send initial message")
			// Don't fail the whole operation
		}
	}

	s.metrics.GRPCRequests.WithLabelValues("CreateConversation", "ok").Inc()
	return s.chatwootConvToProto(conv), nil
}

// ListMessages returns messages for a conversation
func (s *Server) ListMessages(ctx context.Context, req *pb.ListMessagesRequest) (*pb.ListMessagesResponse, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("ListMessages").Observe(time.Since(start).Seconds())
	}()

	convID, err := strconv.ParseInt(req.GetConversationId(), 10, 64)
	if err != nil {
		s.metrics.GRPCRequests.WithLabelValues("ListMessages", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "invalid conversation_id")
	}

	if err = s.verifyConversationTenantByID(ctx, convID); err != nil {
		s.metrics.GRPCRequests.WithLabelValues("ListMessages", "forbidden").Inc()
		return nil, err
	}

	msgs, err := s.chatwoot.ListMessages(ctx, convID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to list messages")
		s.metrics.GRPCRequests.WithLabelValues("ListMessages", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to list messages")
	}

	// Convert to proto
	result := make([]*pb.DeckhandMessage, len(msgs))
	for i, msg := range msgs {
		result[i] = s.chatwootMsgToProto(&msg)
	}

	s.metrics.GRPCRequests.WithLabelValues("ListMessages", "ok").Inc()
	return &pb.ListMessagesResponse{
		Messages:   result,
		TotalCount: int32(len(msgs)),
	}, nil
}

// SendMessage sends a message in a conversation
func (s *Server) SendMessage(ctx context.Context, req *pb.SendMessageRequest) (*pb.DeckhandMessage, error) {
	start := time.Now()
	defer func() {
		s.metrics.GRPCDuration.WithLabelValues("SendMessage").Observe(time.Since(start).Seconds())
	}()

	convID, err := strconv.ParseInt(req.GetConversationId(), 10, 64)
	if err != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendMessage", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "invalid conversation_id")
	}

	if req.GetContent() == "" {
		s.metrics.GRPCRequests.WithLabelValues("SendMessage", "error").Inc()
		return nil, status.Error(codes.InvalidArgument, "content required")
	}

	if err = s.verifyConversationTenantByID(ctx, convID); err != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendMessage", "forbidden").Inc()
		return nil, err
	}

	msg, err := s.chatwoot.SendMessage(ctx, convID, req.GetContent(), false)
	if err != nil {
		s.logger.WithError(err).Error("Failed to send message")
		s.metrics.GRPCRequests.WithLabelValues("SendMessage", "error").Inc()
		return nil, status.Error(codes.Internal, "failed to send message")
	}

	s.metrics.GRPCRequests.WithLabelValues("SendMessage", "ok").Inc()
	return s.chatwootMsgToProto(msg), nil
}

// chatwootConvToProto converts a Chatwoot conversation to proto
func (s *Server) chatwootConvToProto(conv *chatwoot.Conversation) *pb.DeckhandConversation {
	result := &pb.DeckhandConversation{
		Id:          fmt.Sprintf("%d", conv.ID),
		UnreadCount: int32(conv.UnreadCount),
	}

	// Map status
	switch conv.Status {
	case "open":
		result.Status = pb.ConversationStatus_CONVERSATION_STATUS_OPEN
	case "resolved":
		result.Status = pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED
	case "pending":
		result.Status = pb.ConversationStatus_CONVERSATION_STATUS_PENDING
	default:
		result.Status = pb.ConversationStatus_CONVERSATION_STATUS_UNSPECIFIED
	}

	// Timestamps
	if conv.CreatedAt > 0 {
		result.CreatedAt = timestamppb.New(time.Unix(conv.CreatedAt, 0))
	}
	if conv.LastActivityAt > 0 {
		result.UpdatedAt = timestamppb.New(time.Unix(conv.LastActivityAt, 0))
	}

	// Last message
	if len(conv.Messages) > 0 {
		result.LastMessage = s.chatwootMsgToProto(&conv.Messages[len(conv.Messages)-1])
	}

	// Subject stored in custom attributes
	if conv.CustomAttributes != nil {
		if subject, ok := conv.CustomAttributes["subject"]; ok {
			result.Subject = subject
		}
	}

	return result
}

// chatwootMsgToProto converts a Chatwoot message to proto
func (s *Server) chatwootMsgToProto(msg *chatwoot.Message) *pb.DeckhandMessage {
	result := &pb.DeckhandMessage{
		Id:             fmt.Sprintf("%d", msg.ID),
		ConversationId: fmt.Sprintf("%d", msg.ConversationID),
		Content:        msg.Content,
	}

	// Map sender type based on message_type and sender info
	// Chatwoot message types: 0=incoming (customer), 1=outgoing (agent), 2=activity (system)
	if msg.MessageType == chatwoot.MessageTypeActivity {
		result.Sender = pb.MessageSender_MESSAGE_SENDER_SYSTEM
	} else if msg.Sender != nil {
		switch msg.Sender.Type {
		case "user":
			result.Sender = pb.MessageSender_MESSAGE_SENDER_AGENT
		case "contact":
			result.Sender = pb.MessageSender_MESSAGE_SENDER_USER
		}
	} else if msg.MessageType == chatwoot.MessageTypeOutgoing {
		result.Sender = pb.MessageSender_MESSAGE_SENDER_AGENT
	} else {
		result.Sender = pb.MessageSender_MESSAGE_SENDER_USER
	}

	// Timestamp
	if msg.CreatedAt > 0 {
		result.CreatedAt = timestamppb.New(time.Unix(msg.CreatedAt, 0))
	}

	return result
}

func (s *Server) lookupTenantIdentity(ctx context.Context, tenantID string) (string, string) {
	var name string
	var email string

	if s.quartermaster != nil {
		tenant, err := s.quartermaster.GetTenant(ctx, tenantID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to fetch tenant info for contact enrichment")
		} else if tenant != nil && tenant.Tenant != nil {
			name = tenant.Tenant.Name
		}
	}

	if s.purser != nil {
		billing, err := s.purser.GetBillingStatus(ctx, tenantID)
		if err != nil {
			s.logger.WithError(err).Warn("Failed to fetch billing info for contact enrichment")
		} else if billing != nil && billing.Subscription != nil {
			email = billing.Subscription.BillingEmail
		}
	}

	return name, email
}

func (s *Server) verifyConversationTenantByID(ctx context.Context, convID int64) error {
	conv, err := s.chatwoot.GetConversation(ctx, convID)
	if err != nil {
		s.logger.WithError(err).Error("Failed to fetch conversation for tenant verification")
		return status.Error(codes.Internal, "failed to verify conversation")
	}
	if conv == nil {
		return status.Error(codes.NotFound, "conversation not found")
	}
	return s.verifyConversationTenant(ctx, conv)
}

func (s *Server) verifyConversationTenant(ctx context.Context, conv *chatwoot.Conversation) error {
	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return status.Error(codes.PermissionDenied, "tenant_id required")
	}

	if conv.CustomAttributes != nil {
		if attrTenant := conv.CustomAttributes["tenant_id"]; attrTenant != "" {
			if attrTenant != tenantID {
				return status.Error(codes.PermissionDenied, "conversation does not belong to tenant")
			}
			return nil
		}
	}

	if conv.Meta != nil && conv.Meta.Sender != nil {
		if conv.Meta.Sender.Identifier != "" {
			if conv.Meta.Sender.Identifier != tenantID {
				return status.Error(codes.PermissionDenied, "conversation does not belong to tenant")
			}
			return nil
		}
	}

	contact, err := s.chatwoot.GetContactBySourceID(ctx, tenantID)
	if err != nil || contact == nil {
		return status.Error(codes.PermissionDenied, "conversation does not belong to tenant")
	}
	if conv.Meta != nil && conv.Meta.Sender != nil && conv.Meta.Sender.ID == contact.ID {
		return nil
	}

	return status.Error(codes.PermissionDenied, "conversation does not belong to tenant")
}
