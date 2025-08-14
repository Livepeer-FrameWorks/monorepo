package handlers

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/pkg/logging"
	"frameworks/pkg/models"
	pb "frameworks/pkg/proto"
)

// DecklogClient handles batched analytics events to the regional ingest service via gRPC
type DecklogClient struct {
	address    string
	batchSize  int
	flushTimer *time.Timer
	mu         sync.Mutex
	events     []models.DecklogEvent
	grpcClient pb.DecklogServiceClient
	conn       *grpc.ClientConn

	// Buffered channel for async event processing
	eventChan chan models.DecklogEvent
}

var decklogClient *DecklogClient

const (
	defaultBatchSize = 10
	flushInterval    = 1 * time.Second
)

// InitDecklogClient initializes the gRPC Decklog client for batched analytics
func InitDecklogClient() {
	decklogURL := os.Getenv("DECKLOG_URL")
	if decklogURL == "" {
		decklogURL = "http://localhost:18006"
	}

	// Extract address from URL for gRPC (remove http:// prefix)
	address := decklogURL
	if strings.HasPrefix(address, "http://") {
		address = strings.TrimPrefix(address, "http://")
	}
	if strings.HasPrefix(address, "https://") {
		address = strings.TrimPrefix(address, "https://")
	}

	batchSize := defaultBatchSize
	if envBatchSize := os.Getenv("DECKLOG_BATCH_SIZE"); envBatchSize != "" {
		if size, err := strconv.Atoi(envBatchSize); err == nil && size > 0 {
			batchSize = size
		}
	}

	// Create gRPC connection
	conn, err := grpc.Dial(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.WithFields(logging.Fields{
			"address": address,
			"error":   err,
		}).Fatal("Failed to connect to Decklog gRPC service")
		return
	}

	client := pb.NewDecklogServiceClient(conn)

	decklogClient = &DecklogClient{
		address:    address,
		batchSize:  batchSize,
		events:     make([]models.DecklogEvent, 0, batchSize),
		grpcClient: client,
		conn:       conn,
		eventChan:  make(chan models.DecklogEvent, batchSize*2),
	}

	// Start background goroutine to process events
	go decklogClient.processEvents()
	decklogClient.startFlushTimer()

	logger.WithFields(logging.Fields{
		"decklog_url":     decklogURL,
		"decklog_address": address,
		"batch_size":      batchSize,
		"source":          "helmsman-dev-01",
	}).Info("Decklog gRPC client initialized")
}

// SendAnalyticsEvent queues an analytics event for batched sending
func (dc *DecklogClient) SendAnalyticsEvent(eventType string, data map[string]interface{}) {
	if decklogClient == nil {
		logger.Warn("Decklog client not initialized, dropping analytics event")
		return
	}

	event := models.DecklogEvent{
		EventType: eventType,
		Data:      data,
	}

	select {
	case dc.eventChan <- event:
	default:
		logger.WithFields(logging.Fields{
			"event_type": eventType,
		}).Warn("Decklog event channel full, dropping event")
	}
}

// processEvents handles events from the channel and batches them
func (dc *DecklogClient) processEvents() {
	for event := range dc.eventChan {
		dc.mu.Lock()
		dc.events = append(dc.events, event)

		// Flush if batch is full
		if len(dc.events) >= dc.batchSize {
			dc.flushBatch()
		}
		dc.mu.Unlock()
	}
}

// startFlushTimer starts the periodic flush timer
func (dc *DecklogClient) startFlushTimer() {
	dc.flushTimer = time.AfterFunc(flushInterval, func() {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		dc.flushBatch()
		dc.startFlushTimer() // Restart timer
	})
}

// flushBatch sends the current batch to Decklog via gRPC
func (dc *DecklogClient) flushBatch() {
	if len(dc.events) == 0 {
		return
	}

	// Create a copy of events and clear the slice
	eventsCopy := make([]models.DecklogEvent, len(dc.events))
	copy(eventsCopy, dc.events)
	dc.events = dc.events[:0] // Clear slice but keep capacity

	// Send batch asynchronously
	go func() {
		if err := dc.sendBatchGRPC(eventsCopy); err != nil {
			logger.WithFields(logging.Fields{
				"error":           err,
				"batch_size":      len(eventsCopy),
				"decklog_address": dc.address,
			}).Error("Failed to send batch to Decklog")
		} else {
			logger.WithFields(logging.Fields{
				"batch_size":      len(eventsCopy),
				"decklog_address": dc.address,
			}).Info("Successfully sent batch to Decklog")
		}
	}()
}

// sendBatchGRPC sends a batch to Decklog via gRPC streaming
func (dc *DecklogClient) sendBatchGRPC(events []models.DecklogEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := dc.grpcClient.StreamEvents(ctx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	// Convert events to protobuf format
	var eventData []*pb.EventData
	batchTenantID := ""
	for _, event := range events {
		// Convert event type string to enum
		eventType := mapEventTypeToProto(event.EventType)

		// Extract tenant_id for batch
		if batchTenantID == "" {
			if v, ok := event.Data["tenant_id"]; ok {
				batchTenantID = fmt.Sprintf("%v", v)
			}
		}

		// Create typed event data based on event type
		eventDataItem := &pb.EventData{
			EventId:       uuid.New().String(),
			EventType:     eventType,
			Timestamp:     timestamppb.Now(),
			Source:        "helmsman",
			Region:        "local",
			SchemaVersion: "1.0",
		}

		// Set stream_id and user_id from data if available
		if v, ok := event.Data["stream_id"]; ok {
			streamID := fmt.Sprintf("%v", v)
			eventDataItem.StreamId = &streamID
		}
		if v, ok := event.Data["user_id"]; ok {
			userID := fmt.Sprintf("%v", v)
			eventDataItem.UserId = &userID
		}
		if v, ok := event.Data["internal_name"]; ok {
			internalName := fmt.Sprintf("%v", v)
			eventDataItem.InternalName = &internalName
		}

		// Create typed event data based on event type
		switch eventType {
		case pb.EventType_EVENT_TYPE_STREAM_INGEST:
			eventDataItem.EventData = &pb.EventData_StreamIngestData{
				StreamIngestData: &pb.StreamIngestData{
					StreamKey: getStringFromData(event.Data, "stream_key"),
					Protocol:  getStringFromData(event.Data, "protocol"),
					IngestUrl: getStringFromData(event.Data, "push_url"),
				},
			}
		case pb.EventType_EVENT_TYPE_STREAM_VIEW:
			eventDataItem.EventData = &pb.EventData_StreamViewData{
				StreamViewData: &pb.StreamViewData{
					ViewerIp:  getStringFromData(event.Data, "viewer_ip"),
					UserAgent: getStringFromData(event.Data, "user_agent"),
				},
			}
		case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE:
			eventDataItem.EventData = &pb.EventData_StreamLifecycleData{
				StreamLifecycleData: &pb.StreamLifecycleData{
					State:  pb.StreamLifecycleData_STATE_UNSPECIFIED, // Default, could be improved
					Reason: getOptionalStringFromData(event.Data, "reason"),
				},
			}
		case pb.EventType_EVENT_TYPE_USER_CONNECTION:
			eventDataItem.EventData = &pb.EventData_UserConnectionData{
				UserConnectionData: &pb.UserConnectionData{
					Action:           pb.UserConnectionData_ACTION_UNSPECIFIED, // Default, could be improved
					DisconnectReason: getOptionalStringFromData(event.Data, "disconnect_reason"),
				},
			}
		case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE:
			eventDataItem.EventData = &pb.EventData_NodeMonitoringData{
				NodeMonitoringData: &pb.NodeMonitoringData{
					CpuLoad:       getFloatFromData(event.Data, "cpu_load"),
					MemoryUsed:    getUint64FromData(event.Data, "memory_used"),
					MemoryTotal:   getUint64FromData(event.Data, "memory_total"),
					ActiveStreams: getUint32FromData(event.Data, "active_streams"),
				},
			}
		default:
			// For untyped events, we'll need to add a generic fallback or skip
			continue
		}

		eventData = append(eventData, eventDataItem)
	}

	// Send the batch
	batchEvent := &pb.Event{
		BatchId:   uuid.New().String(),
		Source:    "helmsman",
		TenantId:  batchTenantID,
		Events:    eventData,
		Timestamp: timestamppb.Now(),
	}

	if err := stream.Send(batchEvent); err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}

	// Close the send side
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	// Receive the response
	resp, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("failed to receive response: %w", err)
	}

	if resp.Status != "success" {
		return fmt.Errorf("decklog returned error: %s", resp.Message)
	}

	return nil
}

// mapEventTypeToProto converts string event types to protobuf enum
func mapEventTypeToProto(eventType string) pb.EventType {
	switch eventType {
	case "stream-ingest":
		return pb.EventType_EVENT_TYPE_STREAM_INGEST
	case "stream-view":
		return pb.EventType_EVENT_TYPE_STREAM_VIEW
	case "stream-lifecycle":
		return pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE
	case "user-connection":
		return pb.EventType_EVENT_TYPE_USER_CONNECTION
	case "push-lifecycle":
		return pb.EventType_EVENT_TYPE_PUSH_LIFECYCLE
	case "recording-lifecycle":
		return pb.EventType_EVENT_TYPE_RECORDING_LIFECYCLE
	case "client-lifecycle":
		return pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE
	case "node-lifecycle":
		return pb.EventType_EVENT_TYPE_NODE_LIFECYCLE
	case "load-balancing":
		return pb.EventType_EVENT_TYPE_LOAD_BALANCING
	case "track-list":
		return pb.EventType_EVENT_TYPE_TRACK_LIST
	case "stream-buffer":
		return pb.EventType_EVENT_TYPE_STREAM_BUFFER
	case "stream-end":
		return pb.EventType_EVENT_TYPE_STREAM_END
	default:
		return pb.EventType_EVENT_TYPE_UNSPECIFIED
	}
}

// FlushPendingEvents forces a flush of any pending events (used during shutdown)
func FlushPendingEvents() {
	if decklogClient != nil {
		decklogClient.flushBatch()
	}
}

// ForwardEventToDecklog sends batched analytics events to the regional ingest service
func ForwardEventToDecklog(eventTypeOrData interface{}, eventData ...map[string]interface{}) error {
	if decklogClient == nil {
		logger.Warn("Decklog client not initialized")
		return fmt.Errorf("decklog client not initialized")
	}

	var eventType string
	var data map[string]interface{}

	// Handle flexible arguments: either (eventType, data) or (eventData)
	switch v := eventTypeOrData.(type) {
	case string:
		// Called as ForwardEventToDecklog("event_type", data)
		eventType = v
		if len(eventData) > 0 {
			data = eventData[0]
		} else {
			data = make(map[string]interface{})
		}
	case map[string]interface{}:
		// Called as ForwardEventToDecklog(eventData) - extract event_type from data
		data = v
		if et, ok := data["event_type"].(string); ok {
			eventType = et
		} else {
			return fmt.Errorf("event_type not found in event data")
		}
	default:
		return fmt.Errorf("invalid argument type for ForwardEventToDecklog")
	}

	event := models.DecklogEvent{
		EventType: eventType,
		Data:      data,
	}

	// Send to channel (non-blocking)
	select {
	case decklogClient.eventChan <- event:
		return nil
	default:
		// Channel full, log warning and drop
		err := fmt.Errorf("decklog event channel full, dropping event")
		logger.WithFields(logging.Fields{
			"event_type":  eventType,
			"channel_len": len(decklogClient.eventChan),
			"channel_cap": cap(decklogClient.eventChan),
		}).Warn(err.Error())
		return err
	}
}

// Graceful shutdown - flush any remaining events and close connection
func ShutdownDecklogClient() {
	if decklogClient != nil {
		decklogClient.flushBatch()
		if decklogClient.conn != nil {
			decklogClient.conn.Close()
		}
	}
}

// Helper functions for extracting typed data from map[string]interface{}

func getStringFromData(data map[string]interface{}, key string) string {
	if v, ok := data[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func getOptionalStringFromData(data map[string]interface{}, key string) *string {
	if v, ok := data[key]; ok {
		str := fmt.Sprintf("%v", v)
		return &str
	}
	return nil
}

func getFloatFromData(data map[string]interface{}, key string) float32 {
	if v, ok := data[key]; ok {
		switch val := v.(type) {
		case float64:
			return float32(val)
		case float32:
			return val
		case int:
			return float32(val)
		case string:
			if f, err := strconv.ParseFloat(val, 32); err == nil {
				return float32(f)
			}
		}
	}
	return 0
}

func getUint64FromData(data map[string]interface{}, key string) uint64 {
	if v, ok := data[key]; ok {
		switch val := v.(type) {
		case uint64:
			return val
		case int:
			return uint64(val)
		case int64:
			return uint64(val)
		case string:
			if i, err := strconv.ParseUint(val, 10, 64); err == nil {
				return i
			}
		}
	}
	return 0
}

func getUint32FromData(data map[string]interface{}, key string) uint32 {
	if v, ok := data[key]; ok {
		switch val := v.(type) {
		case uint32:
			return val
		case int:
			return uint32(val)
		case int64:
			return uint32(val)
		case string:
			if i, err := strconv.ParseUint(val, 10, 32); err == nil {
				return uint32(i)
			}
		}
	}
	return 0
}
