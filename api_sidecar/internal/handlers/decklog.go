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
	"frameworks/pkg/validation"
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

	// Buffered channel for async event processing (legacy map-based)
	eventChan chan models.DecklogEvent

	// Typed protobuf event pipeline
	protoEvents    []queuedProtoEvent
	protoEventChan chan queuedProtoEvent
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
		address:        address,
		batchSize:      batchSize,
		events:         make([]models.DecklogEvent, 0, batchSize),
		grpcClient:     client,
		conn:           conn,
		eventChan:      make(chan models.DecklogEvent, batchSize*2),
		protoEvents:    make([]queuedProtoEvent, 0, batchSize),
		protoEventChan: make(chan queuedProtoEvent, batchSize*2),
	}

	// Start background goroutine to process events
	go decklogClient.processEvents()
	go decklogClient.processProtoEvents()
	decklogClient.startFlushTimer()

	logger.WithFields(logging.Fields{
		"decklog_url":     decklogURL,
		"decklog_address": address,
		"batch_size":      batchSize,
		"source":          "helmsman-dev-01",
	}).Info("Decklog gRPC client initialized")
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

// processProtoEvents handles typed protobuf events and batches them
func (dc *DecklogClient) processProtoEvents() {
	for protoEv := range dc.protoEventChan {
		dc.mu.Lock()
		dc.protoEvents = append(dc.protoEvents, protoEv)
		// Flush if batch is full across either queue
		if len(dc.protoEvents)+len(dc.events) >= dc.batchSize {
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
	if len(dc.events) == 0 && len(dc.protoEvents) == 0 {
		return
	}

	// Create a copy of events and clear the slice
	eventsCopy := make([]models.DecklogEvent, len(dc.events))
	copy(eventsCopy, dc.events)
	dc.events = dc.events[:0] // Clear slice but keep capacity

	protoCopy := make([]queuedProtoEvent, len(dc.protoEvents))
	copy(protoCopy, dc.protoEvents)
	dc.protoEvents = dc.protoEvents[:0]

	// Send batch asynchronously
	go func() {
		if err := dc.sendBatchGRPC(eventsCopy, protoCopy); err != nil {
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

// queuedProtoEvent holds a typed protobuf event plus derived context
// Placed above sendBatchGRPC
type queuedProtoEvent struct {
	data     *pb.EventData
	tenantID string
	metadata map[string]string
}

// sendBatchGRPC sends a batch to Decklog via gRPC streaming
func (dc *DecklogClient) sendBatchGRPC(events []models.DecklogEvent, protoEvents []queuedProtoEvent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := dc.grpcClient.StreamEvents(ctx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	// Convert events to protobuf format
	var eventData []*pb.EventData
	batchTenantID := ""

	// First, append typed protobuf events directly
	for _, q := range protoEvents {
		if batchTenantID == "" && q.tenantID != "" {
			batchTenantID = q.tenantID
		}
		eventData = append(eventData, q.data)
	}

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
		switch eventType {
		case pb.EventType_EVENT_TYPE_STREAM_INGEST:
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_StreamIngestData{
				StreamIngestData: &pb.StreamIngestData{
					StreamKey: getStringFromData(event.Data, "stream_key"),
					Protocol:  getStringFromData(event.Data, "protocol"),
					IngestUrl: getStringFromData(event.Data, "push_url"),
					Hostname:  getOptionalStringFromData(event.Data, "hostname"),
					NodeId:    getOptionalStringFromData(event.Data, "node_id"),
					Latitude: func() *float64 {
						if v, ok := event.Data["latitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Longitude: func() *float64 {
						if v, ok := event.Data["longitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Location: getOptionalStringFromData(event.Data, "location"),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_STREAM_VIEW:
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_StreamViewData{
				StreamViewData: &pb.StreamViewData{
					NodeId:     getOptionalStringFromData(event.Data, "node_id"),
					OutputType: getOptionalStringFromData(event.Data, "output_type"),
					RequestUrl: getOptionalStringFromData(event.Data, "request_url"),
					Latitude: func() *float64 {
						if v, ok := event.Data["latitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Longitude: func() *float64 {
						if v, ok := event.Data["longitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					ViewerHost: getOptionalStringFromData(event.Data, "viewer_host"),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE:
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_StreamLifecycleData{
				StreamLifecycleData: &pb.StreamLifecycleData{
					State:       pb.StreamLifecycleData_STATE_UNSPECIFIED,
					Reason:      getOptionalStringFromData(event.Data, "reason"),
					Status:      getOptionalStringFromData(event.Data, "status"),
					BufferState: getOptionalStringFromData(event.Data, "buffer_state"),
					DownloadedBytes: func() *uint64 {
						if v, ok := event.Data["downloaded_bytes"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					UploadedBytes: func() *uint64 {
						if v, ok := event.Data["uploaded_bytes"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					TotalViewers: func() *uint32 {
						if v, ok := event.Data["total_viewers"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					TotalInputs: func() *uint32 {
						if v, ok := event.Data["total_inputs"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					TotalOutputs: func() *uint32 {
						if v, ok := event.Data["total_outputs"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					ViewerSeconds: func() *uint64 {
						if v, ok := event.Data["viewer_seconds"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					StreamDetails: getOptionalStringFromData(event.Data, "stream_details"),
					HealthScore: func() *float32 {
						if v, ok := event.Data["health_score"]; ok {
							switch t := v.(type) {
							case float32:
								return &t
							case float64:
								f := float32(t)
								return &f
							}
						}
						return nil
					}(),
					HasIssues:         getOptionalBoolFromData(event.Data, "has_issues"),
					IssuesDescription: getOptionalStringFromData(event.Data, "issues_description"),
					TrackCount: func() *uint32 {
						if v, ok := event.Data["track_count"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					QualityTier: getOptionalStringFromData(event.Data, "quality_tier"),
					PrimaryWidth: func() *uint32 {
						if v, ok := event.Data["primary_width"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					PrimaryHeight: func() *uint32 {
						if v, ok := event.Data["primary_height"]; ok {
							if i, ok2 := v.(uint32); ok2 {
								return &i
							}
						}
						return nil
					}(),
					PrimaryFps: func() *float32 {
						if v, ok := event.Data["primary_fps"]; ok {
							switch t := v.(type) {
							case float32:
								return &t
							case float64:
								f := float32(t)
								return &f
							}
						}
						return nil
					}(),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_USER_CONNECTION:
			// Determine action from event data
			action := pb.UserConnectionData_ACTION_UNSPECIFIED
			if actionStr := getStringFromData(event.Data, "action"); actionStr != "" {
				switch actionStr {
				case "connect":
					action = pb.UserConnectionData_ACTION_CONNECT
				case "disconnect":
					action = pb.UserConnectionData_ACTION_DISCONNECT
				}
			}

			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_UserConnectionData{
				UserConnectionData: &pb.UserConnectionData{
					Action:            action,
					DisconnectReason:  getOptionalStringFromData(event.Data, "disconnect_reason"),
					NodeId:            getOptionalStringFromData(event.Data, "node_id"),
					ConnectionAddr:    getOptionalStringFromData(event.Data, "connection_addr"),
					SessionId:         getOptionalStringFromData(event.Data, "session_id"),
					SessionIdentifier: getOptionalStringFromData(event.Data, "session_identifier"),
					SecondsConnected: func() *uint64 {
						if v, ok := event.Data["seconds_connected"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					UploadedBytes: func() *uint64 {
						if v, ok := event.Data["uploaded_bytes"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					DownloadedBytes: func() *uint64 {
						if v, ok := event.Data["downloaded_bytes"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					Tags:        getOptionalStringFromData(event.Data, "tags"),
					CountryCode: getOptionalStringFromData(event.Data, "country_code"),
					City:        getOptionalStringFromData(event.Data, "city"),
					Latitude: func() *float64 {
						if v, ok := event.Data["latitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Longitude: func() *float64 {
						if v, ok := event.Data["longitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE:
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_NodeMonitoringData{
				NodeMonitoringData: &pb.NodeMonitoringData{
					CpuLoad:       getFloatFromData(event.Data, "cpu_load"),
					MemoryUsed:    getUint64FromData(event.Data, "memory_used"),
					MemoryTotal:   getUint64FromData(event.Data, "memory_total"),
					ActiveStreams: getUint32FromData(event.Data, "active_streams"),
					NodeId:        getOptionalStringFromData(event.Data, "node_id"),
					IsHealthy:     getOptionalBoolFromData(event.Data, "is_healthy"),
					CountryCode:   getOptionalStringFromData(event.Data, "country_code"),
					City:          getOptionalStringFromData(event.Data, "city"),
					Latitude: func() *float64 {
						if v, ok := event.Data["latitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Longitude: func() *float64 {
						if v, ok := event.Data["longitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Location: getOptionalStringFromData(event.Data, "location"),
					BandwidthLimitBps: func() *uint64 {
						if v, ok := event.Data["bandwidth_limit"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE:
			// Map to ClientLifecycleData
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_ClientLifecycleData{
				ClientLifecycleData: &pb.ClientLifecycleData{
					Action:        getStringFromData(event.Data, "action"),
					ClientIp:      getStringFromData(event.Data, "client_ip"),
					ClientCountry: getOptionalStringFromData(event.Data, "client_country"),
					ClientCity:    getOptionalStringFromData(event.Data, "client_city"),
					ClientLatitude: func() *float64 {
						if v, ok := event.Data["client_latitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					ClientLongitude: func() *float64 {
						if v, ok := event.Data["client_longitude"]; ok {
							if f, ok2 := v.(float64); ok2 {
								return &f
							}
						}
						return nil
					}(),
					Protocol:  getOptionalStringFromData(event.Data, "protocol"),
					Host:      getOptionalStringFromData(event.Data, "host"),
					SessionId: getOptionalStringFromData(event.Data, "session_id"),
					ConnectionTime: func() *float32 {
						if v, ok := event.Data["connection_time"]; ok {
							switch t := v.(type) {
							case float32:
								return &t
							case float64:
								f := float32(t)
								return &f
							}
						}
						return nil
					}(),
					Position: func() *float32 {
						if v, ok := event.Data["position"]; ok {
							switch t := v.(type) {
							case float32:
								return &t
							case float64:
								f := float32(t)
								return &f
							}
						}
						return nil
					}(),
					BandwidthInBps: func() *uint64 {
						if v, ok := event.Data["bandwidth_in_bps"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					BandwidthOutBps: func() *uint64 {
						if v, ok := event.Data["bandwidth_out_bps"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					BytesDownloaded: func() *uint64 {
						if v, ok := event.Data["bytes_downloaded"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					BytesUploaded: func() *uint64 {
						if v, ok := event.Data["bytes_uploaded"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					PacketsSent: func() *uint64 {
						if v, ok := event.Data["packets_sent"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					PacketsLost: func() *uint64 {
						if v, ok := event.Data["packets_lost"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					PacketsRetransmitted: func() *uint64 {
						if v, ok := event.Data["packets_retransmitted"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_BANDWIDTH_THRESHOLD:
			// For bandwidth-threshold events, use BandwidthThresholdData
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_BandwidthThresholdData{
				BandwidthThresholdData: &pb.BandwidthThresholdData{
					CurrentBytesPerSec: getUint64FromData(event.Data, "current_bytes_per_sec"),
					ThresholdExceeded:  true, // Always true since this event only fires on threshold exceeded
					ThresholdValue: func() *uint64 {
						if v, ok := event.Data["threshold_value"]; ok {
							if i, ok2 := v.(uint64); ok2 {
								return &i
							}
						}
						return nil
					}(),
					NodeId: getOptionalStringFromData(event.Data, "node_id"),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_TRACK_LIST:
			// Map to TrackListData
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_TrackListData{
				TrackListData: &pb.TrackListData{
					TrackList: getStringFromData(event.Data, "track_list"),
					TrackCount: func() uint32 {
						if v, ok := event.Data["track_count"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					VideoTrackCount: func() uint32 {
						if v, ok := event.Data["video_track_count"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					AudioTrackCount: func() uint32 {
						if v, ok := event.Data["audio_track_count"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryWidth: func() uint32 {
						if v, ok := event.Data["primary_width"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryHeight: func() uint32 {
						if v, ok := event.Data["primary_height"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryFps: func() float32 {
						if v, ok := event.Data["primary_fps"].(float64); ok {
							return float32(v)
						}
						return 0
					}(),
					PrimaryVideoBitrate: func() uint32 {
						if v, ok := event.Data["primary_video_bitrate"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryVideoCodec: getStringFromData(event.Data, "primary_video_codec"),
					PrimaryAudioBitrate: func() uint32 {
						if v, ok := event.Data["primary_audio_bitrate"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryAudioCodec: getStringFromData(event.Data, "primary_audio_codec"),
					PrimaryAudioChannels: func() uint32 {
						if v, ok := event.Data["primary_audio_channels"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					PrimaryAudioSampleRate: func() uint32 {
						if v, ok := event.Data["primary_audio_sample_rate"].(int); ok {
							return uint32(v)
						}
						return 0
					}(),
					QualityTier: getStringFromData(event.Data, "quality_tier"),
					NodeId:      getOptionalStringFromData(event.Data, "node_id"),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_STREAM_BUFFER:
			// Use StreamMetricsData for rich health information from STREAM_BUFFER parsing
			streamMetrics := &pb.StreamMetricsData{
				ViewerCount: getUint32FromData(event.Data, "viewer_count"),
			}
			// Extract packet loss if available
			if packetsSent := getUint64FromData(event.Data, "packets_sent"); packetsSent > 0 {
				if packetsLost := getUint64FromData(event.Data, "packets_lost"); packetsLost > 0 {
					packetLossPercent := float32(packetsLost) / float32(packetsSent) * 100.0
					streamMetrics.PacketLoss = &packetLossPercent
				}
			}
			// Extract primary track quality metrics if available
			if tracks, ok := event.Data["tracks"].([]map[string]interface{}); ok && len(tracks) > 0 {
				primaryTrack := tracks[0]
				if bitrate, ok := primaryTrack["bitrate"].(int); ok {
					bitrateKbps := uint32(bitrate)
					streamMetrics.BitrateKbps = &bitrateKbps
				}
				if width, ok := primaryTrack["width"].(int); ok {
					if height, ok := primaryTrack["height"].(int); ok {
						resolution := fmt.Sprintf("%dx%d", width, height)
						streamMetrics.Resolution = &resolution
					}
				}
				if fps, ok := primaryTrack["fps"].(float64); ok {
					fpsUint := uint32(fps)
					streamMetrics.Fps = &fpsUint
				}
			}
			// Extract bandwidth
			if bandwidth := getUint64FromData(event.Data, "current_bytes_per_sec"); bandwidth > 0 {
				streamMetrics.BandwidthBps = bandwidth
			}
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_StreamMetricsData{
				StreamMetricsData: streamMetrics,
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_STREAM_END:
			// Handle STREAM_END events with aggregate metrics for billing
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_StreamLifecycleData{
				StreamLifecycleData: &pb.StreamLifecycleData{
					State:  pb.StreamLifecycleData_STATE_ENDED,
					Reason: getOptionalStringFromData(event.Data, "source"),
				},
			}
			eventData = append(eventData, eventDataItem)
		case pb.EventType_EVENT_TYPE_RECORDING_LIFECYCLE:
			eventDataItem := &pb.EventData{
				EventId:       uuid.New().String(),
				EventType:     eventType,
				Timestamp:     timestamppb.Now(),
				Source:        "helmsman",
				SchemaVersion: "1.0",
			}
			eventDataItem.EventData = &pb.EventData_RecordingLifecycleData{
				RecordingLifecycleData: &pb.RecordingLifecycleData{
					FilePath:       getStringFromData(event.Data, "file_path"),
					OutputProtocol: getStringFromData(event.Data, "output_protocol"),
					BytesWritten:   getUint64FromData(event.Data, "bytes_written"),
					SecondsWriting: getUint64FromData(event.Data, "seconds_writing"),
					TimeStarted: func() int64 {
						if v, ok := event.Data["time_started"].(int64); ok {
							return v
						}
						return 0
					}(),
					TimeEnded: func() int64 {
						if v, ok := event.Data["time_ended"].(int64); ok {
							return v
						}
						return 0
					}(),
					MediaDurationMs: func() int64 {
						if v, ok := event.Data["media_duration_ms"].(int64); ok {
							return v
						}
						return 0
					}(),
					NodeId: getOptionalStringFromData(event.Data, "node_id"),
				},
			}
			eventData = append(eventData, eventDataItem)
		default:
			// Log unrecognized event types clearly so we can add support
			logger.WithFields(logging.Fields{
				"event_type": eventType,
				"event_id":   uuid.New().String(), // Use a new UUID for unrecognized events
				"data":       event.Data,
			}).Error("Unrecognized event type - skipping event")
			continue
		}
	}

	// Don't send empty batches - this prevents validation errors
	if len(eventData) == 0 {
		logger.WithFields(logging.Fields{
			"original_batch_size": len(events),
			"batch_id":            uuid.New().String(),
		}).Warn("All events in batch were unrecognized - skipping batch send")
		return nil
	}

	// Collect all original data fields for metadata preservation
	metadata := make(map[string]string)
	for _, event := range events {
		for key, value := range event.Data {
			// Convert all data values to strings for metadata
			if str := fmt.Sprintf("%v", value); str != "" {
				metadata[key] = str
			}
		}
	}
	for _, q := range protoEvents {
		for k, v := range q.metadata {
			if v != "" {
				metadata[k] = v
			}
		}
		if batchTenantID == "" && q.data != nil && q.data.InternalName != nil {
			t := getTenantForInternalName(*q.data.InternalName)
			if t != "" {
				batchTenantID = t
			}
		}
	}

	// Send the batch
	batchEvent := &pb.Event{
		BatchId:   uuid.New().String(),
		Source:    "helmsman",
		TenantId:  batchTenantID,
		Events:    eventData,
		Metadata:  metadata,
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
	case "bandwidth-threshold":
		return pb.EventType_EVENT_TYPE_BANDWIDTH_THRESHOLD
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

// ForwardTypedEventToDecklog forwards a typed BaseEvent to Decklog via gRPC
func ForwardTypedEventToDecklog(event *validation.BaseEvent) error {
	if decklogClient == nil {
		logger.Warn("Decklog client not initialized")
		return fmt.Errorf("decklog client not initialized")
	}

	if event == nil {
		return fmt.Errorf("event cannot be nil")
	}

	logger.WithFields(logging.Fields{
		"event_id":   event.EventID,
		"event_type": event.EventType,
	}).Debug("Forwarding typed event to Decklog")

	// Prefer typed path: convert BaseEvent -> protobuf EventData + tenant/meta hints
	ed, tenantID, meta := convertBaseEventToProto(event)
	q := queuedProtoEvent{data: ed, tenantID: tenantID, metadata: meta}

	select {
	case decklogClient.protoEventChan <- q:
		logger.WithFields(logging.Fields{
			"event_id":   event.EventID,
			"event_type": event.EventType,
		}).Debug("Queued typed protobuf event to Decklog")
		return nil
	default:
		err := fmt.Errorf("decklog typed event channel full, dropping event")
		logger.WithFields(logging.Fields{
			"event_id":   event.EventID,
			"event_type": event.EventType,
			"typed_len":  len(decklogClient.protoEventChan),
			"typed_cap":  cap(decklogClient.protoEventChan),
		}).Warn(err.Error())
		return err
	}
}

// convertBaseEventToProto maps validation.BaseEvent into protobuf EventData and returns tenant/meta hints
func convertBaseEventToProto(event *validation.BaseEvent) (*pb.EventData, string, map[string]string) {
	ed := &pb.EventData{
		EventId:       event.EventID,
		EventType:     mapEventTypeToProto(string(event.EventType)),
		Timestamp:     timestamppb.New(event.Timestamp),
		Source:        event.Source,
		Region:        "",
		SchemaVersion: event.SchemaVersion,
	}
	if event.StreamID != nil {
		ed.StreamId = event.StreamID
	}
	if event.UserID != nil {
		ed.UserId = event.UserID
	}
	if event.PlaybackID != nil {
		ed.PlaybackId = event.PlaybackID
	}
	if event.InternalName != nil {
		ed.InternalName = event.InternalName
	}
	if event.NodeURL != nil {
		ed.NodeUrl = event.NodeURL
	}

	meta := map[string]string{}
	tenant := ""
	// Try to propagate tenant from typed payloads
	switch event.EventType {
	case validation.EventStreamIngest:
		if p := event.StreamIngest; p != nil {
			ed.EventData = &pb.EventData_StreamIngestData{StreamIngestData: &pb.StreamIngestData{
				StreamKey: p.StreamKey,
				Protocol:  p.Protocol,
				IngestUrl: p.PushURL,
				Hostname:  &p.Hostname,
				NodeId:    &p.NodeID,
				Latitude: func() *float64 {
					if p.Latitude != 0 {
						v := p.Latitude
						return &v
					}
					return nil
				}(),
				Longitude: func() *float64 {
					if p.Longitude != 0 {
						v := p.Longitude
						return &v
					}
					return nil
				}(),
				Location: &p.Location,
			}}
			tenant = p.TenantID
		}
	case validation.EventStreamView:
		if p := event.StreamView; p != nil {
			ed.EventData = &pb.EventData_StreamViewData{StreamViewData: &pb.StreamViewData{
				RequestUrl:  &p.RequestURL,
				NodeId:      &p.NodeID,
				OutputType:  &p.OutputType,
				CountryCode: &p.CountryCode,
				City:        &p.City,
				Latitude: func() *float64 {
					if p.Latitude != 0 {
						v := p.Latitude
						return &v
					}
					return nil
				}(),
				Longitude: func() *float64 {
					if p.Longitude != 0 {
						v := p.Longitude
						return &v
					}
					return nil
				}(),
				ViewerHost: &p.ViewerHost,
			}}
			tenant = p.TenantID
		}
	case validation.EventStreamLifecycle:
		if p := event.StreamLifecycle; p != nil {
			ed.EventData = &pb.EventData_StreamLifecycleData{StreamLifecycleData: &pb.StreamLifecycleData{
				Status:      &p.Status,
				BufferState: &p.BufferState,
				DownloadedBytes: func() *uint64 {
					if p.DownloadedBytes > 0 {
						v := uint64(p.DownloadedBytes)
						return &v
					}
					return nil
				}(),
				UploadedBytes: func() *uint64 {
					if p.UploadedBytes > 0 {
						v := uint64(p.UploadedBytes)
						return &v
					}
					return nil
				}(),
				TotalViewers: func() *uint32 {
					if p.TotalViewers > 0 {
						v := uint32(p.TotalViewers)
						return &v
					}
					return nil
				}(),
				TotalInputs: func() *uint32 {
					if p.TotalInputs > 0 {
						v := uint32(p.TotalInputs)
						return &v
					}
					return nil
				}(),
				TotalOutputs: func() *uint32 {
					if p.TotalOutputs > 0 {
						v := uint32(p.TotalOutputs)
						return &v
					}
					return nil
				}(),
				ViewerSeconds: func() *uint64 {
					if p.ViewerSeconds > 0 {
						v := uint64(p.ViewerSeconds)
						return &v
					}
					return nil
				}(),
				StreamDetails: &p.StreamDetails,
				HealthScore: func() *float32 {
					if p.HealthScore > 0 {
						v := float32(p.HealthScore)
						return &v
					}
					return nil
				}(),
				HasIssues: func() *bool {
					if p.HasIssues {
						v := true
						return &v
					}
					return nil
				}(),
				IssuesDescription: &p.IssuesDesc,
				TrackCount: func() *uint32 {
					if p.TrackCount > 0 {
						v := uint32(p.TrackCount)
						return &v
					}
					return nil
				}(),
				QualityTier: &p.QualityTier,
				PrimaryWidth: func() *uint32 {
					if p.PrimaryWidth > 0 {
						v := uint32(p.PrimaryWidth)
						return &v
					}
					return nil
				}(),
				PrimaryHeight: func() *uint32 {
					if p.PrimaryHeight > 0 {
						v := uint32(p.PrimaryHeight)
						return &v
					}
					return nil
				}(),
				PrimaryFps: func() *float32 {
					if p.PrimaryFPS > 0 {
						v := float32(p.PrimaryFPS)
						return &v
					}
					return nil
				}(),
			}}
			tenant = p.TenantID
		}
	case validation.EventTrackList:
		if p := event.TrackList; p != nil {
			ed.EventData = &pb.EventData_TrackListData{TrackListData: &pb.TrackListData{
				TrackList:              p.TrackListJSON,
				TrackCount:             uint32(p.TrackCount),
				VideoTrackCount:        uint32(p.VideoTrackCount),
				AudioTrackCount:        uint32(p.AudioTrackCount),
				PrimaryWidth:           uint32(p.PrimaryWidth),
				PrimaryHeight:          uint32(p.PrimaryHeight),
				PrimaryFps:             float32(p.PrimaryFPS),
				PrimaryVideoBitrate:    uint32(p.PrimaryVideoBitrate),
				PrimaryVideoCodec:      p.PrimaryVideoCodec,
				PrimaryAudioBitrate:    uint32(p.PrimaryAudioBitrate),
				PrimaryAudioCodec:      p.PrimaryAudioCodec,
				PrimaryAudioChannels:   uint32(p.PrimaryAudioChannels),
				PrimaryAudioSampleRate: uint32(p.PrimaryAudioSampleRate),
				QualityTier:            p.QualityTier,
				NodeId:                 &p.NodeID,
			}}
			tenant = p.TenantID
		}
	case validation.EventBandwidthThreshold:
		if p := event.BandwidthThreshold; p != nil {
			ed.EventData = &pb.EventData_BandwidthThresholdData{BandwidthThresholdData: &pb.BandwidthThresholdData{
				CurrentBytesPerSec: uint64(p.CurrentBytesPerSec),
				ThresholdExceeded:  p.ThresholdExceeded,
				ThresholdValue: func() *uint64 {
					if p.ThresholdValue > 0 {
						v := uint64(p.ThresholdValue)
						return &v
					}
					return nil
				}(),
				NodeId: &p.NodeID,
			}}
			tenant = p.TenantID
		}
	case validation.EventRecordingLifecycle:
		if p := event.Recording; p != nil {
			ed.EventData = &pb.EventData_RecordingLifecycleData{RecordingLifecycleData: &pb.RecordingLifecycleData{
				FilePath:        p.FilePath,
				OutputProtocol:  p.OutputProtocol,
				BytesWritten:    uint64(p.BytesWritten),
				SecondsWriting:  uint64(p.SecondsWriting),
				TimeStarted:     p.TimeStarted,
				TimeEnded:       p.TimeEnded,
				MediaDurationMs: p.MediaDurationMs,
				NodeId:          &p.NodeID,
			}}
			tenant = p.TenantID
		}
	case validation.EventPushLifecycle:
		if p := event.PushLifecycle; p != nil {
			ed.EventData = &pb.EventData_PushLifecycleData{PushLifecycleData: &pb.PushLifecycleData{
				PushTarget:      p.PushTarget,
				Action:          p.Action,
				PushId:          &p.PushID,
				TargetUriBefore: &p.TargetURIBefore,
				TargetUriAfter:  &p.TargetURIAfter,
				Status:          &p.Status,
				LogMessages:     &p.LogMessages,
				NodeId:          &p.NodeID,
			}}
			tenant = p.TenantID
		}
	case validation.EventNodeLifecycle:
		if p := event.NodeLifecycle; p != nil {
			ed.EventData = &pb.EventData_NodeMonitoringData{
				NodeMonitoringData: &pb.NodeMonitoringData{
					NodeId:        &p.NodeID,
					IsHealthy:     &p.IsHealthy,
					CpuLoad:       float32(p.CPUUsage),
					MemoryUsed:    p.RAMCurrent,
					MemoryTotal:   p.RAMMax,
					NetworkInBps:  p.BandwidthDown,
					NetworkOutBps: p.BandwidthUp,
					ActiveStreams: uint32(p.ActiveStreams),
					CountryCode:   &p.GeoData.CountryCode,
					City:          &p.GeoData.City,
					Latitude: func() *float64 {
						if p.GeoData.Latitude != 0 {
							v := p.GeoData.Latitude
							return &v
						}
						return nil
					}(),
					Longitude: func() *float64 {
						if p.GeoData.Longitude != 0 {
							v := p.GeoData.Longitude
							return &v
						}
						return nil
					}(),
					Location: &p.Location,
					BandwidthLimitBps: func() *uint64 {
						if p.BandwidthLimit > 0 {
							v := p.BandwidthLimit
							return &v
						}
						return nil
					}(),
				},
			}
			// NODE_LIFECYCLE events don't have tenant_id
			tenant = ""
		}
	}

	// derive metadata hints for validator compatibility when needed
	if event.EventType == validation.EventNodeLifecycle && event.NodeLifecycle != nil {
		meta["node_id"] = event.NodeLifecycle.NodeID
		meta["is_healthy"] = fmt.Sprintf("%t", event.NodeLifecycle.IsHealthy)
	}

	return ed, tenant, meta
}

// convertTypedEventToMap converts a typed BaseEvent to map[string]interface{} for backward compatibility
func convertTypedEventToMap(event *validation.BaseEvent) map[string]interface{} {
	data := map[string]interface{}{
		"event_id":       event.EventID,
		"event_type":     string(event.EventType),
		"timestamp":      event.Timestamp.Unix(),
		"source":         event.Source,
		"schema_version": event.SchemaVersion,
	}

	// Add optional base fields
	if event.StreamID != nil {
		data["stream_id"] = *event.StreamID
	}
	if event.UserID != nil {
		data["user_id"] = *event.UserID
	}
	if event.PlaybackID != nil {
		data["playback_id"] = *event.PlaybackID
	}
	if event.InternalName != nil {
		data["internal_name"] = *event.InternalName
	}
	if event.NodeURL != nil {
		data["node_url"] = *event.NodeURL
	}

	// Add typed payload data based on event type
	switch event.EventType {
	case validation.EventUserConnection:
		if event.UserConnection != nil {
			data["tenant_id"] = event.UserConnection.TenantID
			data["internal_name"] = event.UserConnection.InternalName
			data["connection_addr"] = event.UserConnection.ConnectionAddr
			data["connector"] = event.UserConnection.Connector
			data["node_id"] = event.UserConnection.NodeID
			data["action"] = event.UserConnection.Action

			// Add optional fields only if they have values
			if event.UserConnection.SessionID != "" {
				data["session_id"] = event.UserConnection.SessionID
			}
			if event.UserConnection.SessionIdentifier != "" {
				data["session_identifier"] = event.UserConnection.SessionIdentifier
			}
			if event.UserConnection.SecondsConnected > 0 {
				data["seconds_connected"] = event.UserConnection.SecondsConnected
			}
			if event.UserConnection.UploadedBytes > 0 {
				data["uploaded_bytes"] = event.UserConnection.UploadedBytes
			}
			if event.UserConnection.DownloadedBytes > 0 {
				data["downloaded_bytes"] = event.UserConnection.DownloadedBytes
			}
			if event.UserConnection.Tags != "" {
				data["tags"] = event.UserConnection.Tags
			}
			if event.UserConnection.CountryCode != "" {
				data["country_code"] = event.UserConnection.CountryCode
			}
			if event.UserConnection.City != "" {
				data["city"] = event.UserConnection.City
			}
			if event.UserConnection.Latitude != 0 {
				data["latitude"] = event.UserConnection.Latitude
			}
			if event.UserConnection.Longitude != 0 {
				data["longitude"] = event.UserConnection.Longitude
			}
		}
	case validation.EventStreamLifecycle:
		if event.StreamLifecycle != nil {
			data["stream_name"] = event.StreamLifecycle.StreamName
			data["internal_name"] = event.StreamLifecycle.InternalName
			data["node_id"] = event.StreamLifecycle.NodeID
			data["tenant_id"] = event.StreamLifecycle.TenantID
			data["status"] = event.StreamLifecycle.Status
			data["buffer_state"] = event.StreamLifecycle.BufferState

			// Add optional stream end metrics
			if event.StreamLifecycle.DownloadedBytes > 0 {
				data["downloaded_bytes"] = event.StreamLifecycle.DownloadedBytes
			}
			if event.StreamLifecycle.UploadedBytes > 0 {
				data["uploaded_bytes"] = event.StreamLifecycle.UploadedBytes
			}
			if event.StreamLifecycle.TotalViewers > 0 {
				data["total_viewers"] = event.StreamLifecycle.TotalViewers
			}
			if event.StreamLifecycle.TotalInputs > 0 {
				data["total_inputs"] = event.StreamLifecycle.TotalInputs
			}
			if event.StreamLifecycle.TotalOutputs > 0 {
				data["total_outputs"] = event.StreamLifecycle.TotalOutputs
			}
			if event.StreamLifecycle.ViewerSeconds > 0 {
				data["viewer_seconds"] = event.StreamLifecycle.ViewerSeconds
			}
			if event.StreamLifecycle.StreamDetails != "" {
				data["stream_details"] = event.StreamLifecycle.StreamDetails
			}

			// Add parsed health metrics
			if event.StreamLifecycle.HealthScore > 0 {
				data["health_score"] = event.StreamLifecycle.HealthScore
			}
			if event.StreamLifecycle.HasIssues {
				data["has_issues"] = event.StreamLifecycle.HasIssues
			}
			if event.StreamLifecycle.IssuesDesc != "" {
				data["issues_description"] = event.StreamLifecycle.IssuesDesc
			}
			if event.StreamLifecycle.TrackCount > 0 {
				data["track_count"] = event.StreamLifecycle.TrackCount
			}
			if event.StreamLifecycle.QualityTier != "" {
				data["quality_tier"] = event.StreamLifecycle.QualityTier
			}
			if event.StreamLifecycle.PrimaryWidth > 0 {
				data["primary_width"] = event.StreamLifecycle.PrimaryWidth
			}
			if event.StreamLifecycle.PrimaryHeight > 0 {
				data["primary_height"] = event.StreamLifecycle.PrimaryHeight
			}
			if event.StreamLifecycle.PrimaryFPS > 0 {
				data["primary_fps"] = event.StreamLifecycle.PrimaryFPS
			}
		}
	case validation.EventTrackList:
		if event.TrackList != nil {
			data["stream_name"] = event.TrackList.StreamName
			data["internal_name"] = event.TrackList.InternalName
			data["node_id"] = event.TrackList.NodeID
			data["tenant_id"] = event.TrackList.TenantID
			data["track_list"] = event.TrackList.TrackListJSON

			// Add parsed quality metrics
			if event.TrackList.TrackCount > 0 {
				data["track_count"] = event.TrackList.TrackCount
			}
			if event.TrackList.VideoTrackCount > 0 {
				data["video_track_count"] = event.TrackList.VideoTrackCount
			}
			if event.TrackList.AudioTrackCount > 0 {
				data["audio_track_count"] = event.TrackList.AudioTrackCount
			}
			if event.TrackList.QualityTier != "" {
				data["quality_tier"] = event.TrackList.QualityTier
			}
			if event.TrackList.PrimaryWidth > 0 {
				data["primary_width"] = event.TrackList.PrimaryWidth
			}
			if event.TrackList.PrimaryHeight > 0 {
				data["primary_height"] = event.TrackList.PrimaryHeight
			}
			if event.TrackList.PrimaryFPS > 0 {
				data["primary_fps"] = event.TrackList.PrimaryFPS
			}
			if event.TrackList.PrimaryVideoBitrate > 0 {
				data["primary_video_bitrate"] = event.TrackList.PrimaryVideoBitrate
			}
			if event.TrackList.PrimaryVideoCodec != "" {
				data["primary_video_codec"] = event.TrackList.PrimaryVideoCodec
			}
			if event.TrackList.PrimaryAudioBitrate > 0 {
				data["primary_audio_bitrate"] = event.TrackList.PrimaryAudioBitrate
			}
			if event.TrackList.PrimaryAudioCodec != "" {
				data["primary_audio_codec"] = event.TrackList.PrimaryAudioCodec
			}
			if event.TrackList.PrimaryAudioChannels > 0 {
				data["primary_audio_channels"] = event.TrackList.PrimaryAudioChannels
			}
			if event.TrackList.PrimaryAudioSampleRate > 0 {
				data["primary_audio_sample_rate"] = event.TrackList.PrimaryAudioSampleRate
			}
		}
	case validation.EventBandwidthThreshold:
		if event.BandwidthThreshold != nil {
			data["stream_name"] = event.BandwidthThreshold.StreamName
			data["internal_name"] = event.BandwidthThreshold.InternalName
			data["node_id"] = event.BandwidthThreshold.NodeID
			data["tenant_id"] = event.BandwidthThreshold.TenantID
			data["current_bytes_per_sec"] = event.BandwidthThreshold.CurrentBytesPerSec
			data["threshold_exceeded"] = event.BandwidthThreshold.ThresholdExceeded

			if event.BandwidthThreshold.ThresholdValue > 0 {
				data["threshold_value"] = event.BandwidthThreshold.ThresholdValue
			}
		}
	case validation.EventRecordingLifecycle:
		if event.Recording != nil {
			data["stream_name"] = event.Recording.StreamName
			data["internal_name"] = event.Recording.InternalName
			data["node_id"] = event.Recording.NodeID
			data["tenant_id"] = event.Recording.TenantID
			data["file_path"] = event.Recording.FilePath
			data["output_protocol"] = event.Recording.OutputProtocol
			data["bytes_written"] = event.Recording.BytesWritten
			data["seconds_writing"] = event.Recording.SecondsWriting
			data["time_started"] = event.Recording.TimeStarted
			data["time_ended"] = event.Recording.TimeEnded
			data["media_duration_ms"] = event.Recording.MediaDurationMs
			data["is_recording"] = event.Recording.IsRecording
		}
	case validation.EventNodeLifecycle:
		if event.NodeLifecycle != nil {
			data["node_id"] = event.NodeLifecycle.NodeID
			data["base_url"] = event.NodeLifecycle.BaseURL
			data["is_healthy"] = event.NodeLifecycle.IsHealthy
			// Optional geo and metrics
			if event.NodeLifecycle.GeoData.Latitude != 0 {
				data["latitude"] = event.NodeLifecycle.GeoData.Latitude
			}
			if event.NodeLifecycle.GeoData.Longitude != 0 {
				data["longitude"] = event.NodeLifecycle.GeoData.Longitude
			}
			if event.NodeLifecycle.Location != "" {
				data["location"] = event.NodeLifecycle.Location
			}
			if event.NodeLifecycle.CPUUsage != 0 {
				data["cpu"] = event.NodeLifecycle.CPUUsage
			}
			if event.NodeLifecycle.RAMMax != 0 {
				data["ram_max"] = event.NodeLifecycle.RAMMax
			}
			if event.NodeLifecycle.RAMCurrent != 0 {
				data["ram_current"] = event.NodeLifecycle.RAMCurrent
			}
			if event.NodeLifecycle.BandwidthUp != 0 {
				data["bandwidth_up"] = event.NodeLifecycle.BandwidthUp
			}
			if event.NodeLifecycle.BandwidthDown != 0 {
				data["bandwidth_down"] = event.NodeLifecycle.BandwidthDown
			}
			if event.NodeLifecycle.BandwidthLimit != 0 {
				data["bandwidth_limit"] = event.NodeLifecycle.BandwidthLimit
			}
			if event.NodeLifecycle.ActiveStreams != 0 {
				data["active_streams"] = event.NodeLifecycle.ActiveStreams
			}
		}
	case validation.EventPushLifecycle:
		if event.PushLifecycle != nil {
			data["stream_name"] = event.PushLifecycle.StreamName
			data["internal_name"] = event.PushLifecycle.InternalName
			data["node_id"] = event.PushLifecycle.NodeID
			data["tenant_id"] = event.PushLifecycle.TenantID
			data["push_target"] = event.PushLifecycle.PushTarget
			data["action"] = event.PushLifecycle.Action

			// Add optional fields
			if event.PushLifecycle.PushID != "" {
				data["push_id"] = event.PushLifecycle.PushID
			}
			if event.PushLifecycle.TargetURIBefore != "" {
				data["target_uri_before"] = event.PushLifecycle.TargetURIBefore
			}
			if event.PushLifecycle.TargetURIAfter != "" {
				data["target_uri_after"] = event.PushLifecycle.TargetURIAfter
			}
			if event.PushLifecycle.Status != "" {
				data["status"] = event.PushLifecycle.Status
			}
			if event.PushLifecycle.LogMessages != "" {
				data["log_messages"] = event.PushLifecycle.LogMessages
			}
		}
	case validation.EventStreamIngest:
		if event.StreamIngest != nil {
			data["stream_key"] = event.StreamIngest.StreamKey
			data["internal_name"] = event.StreamIngest.InternalName
			data["node_id"] = event.StreamIngest.NodeID
			data["tenant_id"] = event.StreamIngest.TenantID
			data["hostname"] = event.StreamIngest.Hostname
			data["push_url"] = event.StreamIngest.PushURL
			data["protocol"] = event.StreamIngest.Protocol

			// Add optional fields
			if event.StreamIngest.UserID != "" {
				data["user_id"] = event.StreamIngest.UserID
			}
			if event.StreamIngest.Latitude != 0 {
				data["latitude"] = event.StreamIngest.Latitude
			}
			if event.StreamIngest.Longitude != 0 {
				data["longitude"] = event.StreamIngest.Longitude
			}
			if event.StreamIngest.Location != "" {
				data["location"] = event.StreamIngest.Location
			}
		}
	case validation.EventStreamView:
		if event.StreamView != nil {
			data["tenant_id"] = event.StreamView.TenantID
			data["playback_id"] = event.StreamView.PlaybackID
			data["internal_name"] = event.StreamView.InternalName
			data["node_id"] = event.StreamView.NodeID
			data["viewer_host"] = event.StreamView.ViewerHost
			data["output_type"] = event.StreamView.OutputType

			// Add optional fields
			if event.StreamView.RequestURL != "" {
				data["request_url"] = event.StreamView.RequestURL
			}
			if event.StreamView.CountryCode != "" {
				data["country_code"] = event.StreamView.CountryCode
			}
			if event.StreamView.City != "" {
				data["city"] = event.StreamView.City
			}
			if event.StreamView.Latitude != 0 {
				data["latitude"] = event.StreamView.Latitude
			}
			if event.StreamView.Longitude != 0 {
				data["longitude"] = event.StreamView.Longitude
			}
		}
		// TODO: Add other event types as they are migrated
	}

	return data
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

func getOptionalBoolFromData(data map[string]interface{}, key string) *bool {
	if v, ok := data[key]; ok {
		switch b := v.(type) {
		case bool:
			return &b
		case string:
			if b == "true" || b == "1" {
				bv := true
				return &bv
			}
			if b == "false" || b == "0" {
				bv := false
				return &bv
			}
		}
	}
	return nil
}
