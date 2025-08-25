package decklog

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"frameworks/pkg/logging"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "frameworks/pkg/proto"
)

// Client represents a gRPC client for Decklog
type Client struct {
	conn   *grpc.ClientConn
	client pb.DecklogServiceClient
	logger logging.Logger
}

// ClientConfig represents configuration for the Decklog client
type ClientConfig struct {
	Target        string
	AllowInsecure bool
	CACertFile    string
	Timeout       time.Duration
}

// NewClient creates a new Decklog gRPC client
func NewClient(cfg ClientConfig, logger logging.Logger) (*Client, error) {
	var opts []grpc.DialOption

	if cfg.AllowInsecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		// Load CA cert for server verification
		caCert, err := os.ReadFile(cfg.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA cert: %w", err)
		}

		certPool := x509.NewCertPool()
		if !certPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to append CA cert")
		}

		creds := credentials.NewTLS(&tls.Config{
			RootCAs: certPool,
		})
		opts = append(opts, grpc.WithTransportCredentials(creds))
	}

	// Add timeout
	if cfg.Timeout > 0 {
		opts = append(opts, grpc.WithTimeout(cfg.Timeout))
	}

	// Connect to server
	conn, err := grpc.Dial(cfg.Target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial server: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewDecklogServiceClient(conn),
		logger: logger,
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// SendEvent sends a prepared event to Decklog.
// Any missing timestamp will be populated automatically.
func (c *Client) SendEvent(ctx context.Context, event *pb.Event) error {
	if event.Timestamp == nil {
		event.Timestamp = timestamppb.Now()
	}

	_, err := c.client.SendEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to send event: %w", err)
	}

	return nil
}

// StreamEvents creates a bidirectional stream for sending events
func (c *Client) StreamEvents(ctx context.Context) (pb.DecklogService_StreamEventsClient, error) {
	stream, err := c.client.StreamEvents(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create stream: %w", err)
	}

	return stream, nil
}

// CheckHealth checks Decklog's health
func (c *Client) CheckHealth(ctx context.Context) (string, error) {
	resp, err := c.client.CheckHealth(ctx, &pb.HealthRequest{})
	if err != nil {
		return "", fmt.Errorf("health check failed: %w", err)
	}

	return resp.Status, nil
}

// Helper methods for creating common event types

// NewStreamIngestEvent creates a new stream ingest event with typed data
func NewStreamIngestEvent(tenantID, streamKey, userID, internalName, streamID, ingestURL, protocol string) *pb.Event {
	return &pb.Event{
		Source:   "mistserver_webhook",
		TenantId: tenantID,
		Events: []*pb.EventData{
			{
				EventId:       fmt.Sprintf("ingest_%d", time.Now().UnixNano()),
				EventType:     pb.EventType_EVENT_TYPE_STREAM_INGEST,
				Timestamp:     timestamppb.Now(),
				Source:        "mistserver_webhook",
				StreamId:      &streamID,
				UserId:        &userID,
				InternalName:  &internalName,
				Region:        os.Getenv("REGION"),
				SchemaVersion: "1.0",
				EventData: &pb.EventData_StreamIngestData{
					StreamIngestData: &pb.StreamIngestData{
						StreamKey: streamKey,
						Protocol:  protocol,
						IngestUrl: ingestURL,
					},
				},
			},
		},
		Timestamp: timestamppb.Now(),
	}
}

// NewStreamViewEvent creates a new stream view event with typed data
func NewStreamViewEvent(tenantID, playbackID, internalName, streamID string) *pb.Event {
	return &pb.Event{
		Source:   "mistserver_webhook",
		TenantId: tenantID,
		Events: []*pb.EventData{
			{
				EventId:       fmt.Sprintf("view_%d", time.Now().UnixNano()),
				EventType:     pb.EventType_EVENT_TYPE_STREAM_VIEW,
				Timestamp:     timestamppb.Now(),
				Source:        "mistserver_webhook",
				StreamId:      &streamID,
				PlaybackId:    &playbackID,
				InternalName:  &internalName,
				Region:        os.Getenv("REGION"),
				SchemaVersion: "1.0",
				EventData: &pb.EventData_StreamViewData{
					StreamViewData: &pb.StreamViewData{},
				},
			},
		},
		Timestamp: timestamppb.Now(),
	}
}

// NewLoadBalancingEvent creates a new load balancing event with typed data
func NewLoadBalancingEvent(tenantID, streamID, selectedNode, selectedNodeID, clientIP, clientCountry, status, details string, lat, lon float64, score uint64, nodeLat, nodeLon float64, nodeName string, routingDistanceKm float64) *pb.Event {
	return &pb.Event{
		Source:   "foghorn",
		TenantId: tenantID,
		Events: []*pb.EventData{
			{
				EventId:       fmt.Sprintf("lb_%d", time.Now().UnixNano()),
				EventType:     pb.EventType_EVENT_TYPE_LOAD_BALANCING,
				Timestamp:     timestamppb.Now(),
				Source:        "foghorn",
				StreamId:      &streamID,
				Region:        os.Getenv("REGION"),
				SchemaVersion: "1.0",
				EventData: &pb.EventData_LoadBalancingData{
					LoadBalancingData: &pb.LoadBalancingData{
						SelectedNode:  selectedNode,
						Latitude:      lat,
						Longitude:     lon,
						Status:        status,
						Details:       details,
						Score:         score,
						ClientIp:      clientIP,
						ClientCountry: clientCountry,
						NodeLatitude:  nodeLat,
						NodeLongitude: nodeLon,
						NodeName:      nodeName,
						SelectedNodeId: func() *string {
							if selectedNodeID == "" {
								return nil
							}
							return &selectedNodeID
						}(),
						RoutingDistanceKm: func() *float64 {
							if routingDistanceKm == 0 {
								return nil
							}
							v := routingDistanceKm
							return &v
						}(),
					},
				},
			},
		},
		Timestamp: timestamppb.Now(),
	}
}

// NewClipLifecycleEvent creates a new clip lifecycle event
func NewClipLifecycleEvent(tenantID string, internalName string, requestID string, stage pb.ClipLifecycleData_Stage, opts func(*pb.ClipLifecycleData)) *pb.Event {
	data := &pb.ClipLifecycleData{Stage: stage, RequestId: requestID}
	if opts != nil {
		opts(data)
	}
	return &pb.Event{
		Source:   "clip_orchestrator",
		TenantId: tenantID,
		Events: []*pb.EventData{
			{
				EventId:       fmt.Sprintf("clip_%d", time.Now().UnixNano()),
				EventType:     pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE,
				Timestamp:     timestamppb.Now(),
				Source:        "clip_orchestrator",
				InternalName:  &internalName,
				Region:        os.Getenv("REGION"),
				SchemaVersion: "1.0",
				EventData:     &pb.EventData_ClipLifecycleData{ClipLifecycleData: data},
			},
		},
		Timestamp: timestamppb.Now(),
	}
}

// NewDVRLifecycleEvent creates a new DVR lifecycle event
func NewDVRLifecycleEvent(tenantID string, internalName string, requestID string, stage pb.DVRLifecycleData_Stage, opts func(*pb.DVRLifecycleData)) *pb.Event {
	data := &pb.DVRLifecycleData{Stage: stage, RequestId: requestID}
	if opts != nil {
		opts(data)
	}
	return &pb.Event{
		Source:   "dvr_orchestrator",
		TenantId: tenantID,
		Events: []*pb.EventData{
			{
				EventId:       fmt.Sprintf("dvr_%d", time.Now().UnixNano()),
				EventType:     pb.EventType_EVENT_TYPE_DVR_LIFECYCLE,
				Timestamp:     timestamppb.Now(),
				Source:        "dvr_orchestrator",
				InternalName:  &internalName,
				Region:        os.Getenv("REGION"),
				SchemaVersion: "1.0",
				EventData:     &pb.EventData_DvrLifecycleData{DvrLifecycleData: data},
			},
		},
		Timestamp: timestamppb.Now(),
	}
}
