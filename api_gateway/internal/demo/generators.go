package demo

import (
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GenerateStreams creates realistic demo stream data
func GenerateStreams() []*pb.Stream {
	now := time.Now()

	return []*pb.Stream{
		{
			InternalName: "demo_live_stream_001",
			Title:        "Live: FrameWorks Demo Stream",
			Description:  "Demonstrating live streaming capabilities",
			StreamKey:    "sk_demo_live_a1b2c3d4e5f6",
			PlaybackId:   "pb_demo_live_x7y8z9",
			IsLive:       true,
			Status:       "live",
			IsRecording:  true,
			CreatedAt:    timestamppb.New(now.Add(-2 * time.Hour)),
			UpdatedAt:    timestamppb.New(now.Add(-5 * time.Minute)),
		},
		{
			InternalName: "demo_offline_stream_002",
			Title:        "Gaming Stream Setup",
			Description:  "Getting ready for tonight's gaming session",
			StreamKey:    "sk_demo_offline_f6g7h8i9j0k1",
			PlaybackId:   "pb_demo_offline_m3n4o5",
			IsLive:       false,
			Status:       "offline",
			IsRecording:  false,
			CreatedAt:    timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:    timestamppb.New(now.Add(-1 * time.Hour)),
		},
		{
			InternalName: "demo_recording_stream_003",
			Title:        "Product Demo Recording",
			Description:  "Recording a product demo for the marketing team",
			StreamKey:    "sk_demo_rec_l2m3n4o5p6q7",
			PlaybackId:   "pb_demo_rec_r8s9t0",
			IsLive:       true,
			Status:       "recording",
			IsRecording:  true,
			CreatedAt:    timestamppb.New(now.Add(-6 * time.Hour)),
			UpdatedAt:    timestamppb.New(now.Add(-10 * time.Minute)),
		},
		{
			InternalName: "demo_ended_stream_004",
			Title:        "Weekly Team Standup",
			Description:  "Our weekly development team standup meeting",
			StreamKey:    "sk_demo_ended_u1v2w3x4y5z6",
			PlaybackId:   "pb_demo_ended_a7b8c9",
			IsLive:       false,
			Status:       "ended",
			IsRecording:  false,
			CreatedAt:    timestamppb.New(now.Add(-48 * time.Hour)),
			UpdatedAt:    timestamppb.New(now.Add(-36 * time.Hour)),
		},
	}
}

// GenerateStreamAnalytics creates realistic analytics data
func GenerateStreamAnalytics(streamID string) *pb.StreamAnalytics {
	now := time.Now()

	startTime := now.Add(-2 * time.Hour)
	endTime := now

	return &pb.StreamAnalytics{
		Id:                   "demo_analytics_" + streamID,
		TenantId:             "demo_tenant_frameworks",
		StreamId:             streamID,
		InternalName:         streamID,
		SessionStartTime:     timestamppb.New(startTime),
		SessionEndTime:       timestamppb.New(endTime),
		TotalSessionDuration: 18650, // seconds
		CurrentViewers:       45,
		PeakViewers:          89,
		TotalConnections:     1247,
		BandwidthIn:          125_000_000,
		BandwidthOut:         450_000_000,
		TotalBandwidthGb:     0.575,
		BitrateKbps:          int32Ptr(2500),
		Resolution:           stringPtr("1920x1080"),
		PacketsSent:          850000,
		PacketsLost:          425,
		PacketsRetrans:       1275,
		Upbytes:              125_000_000,
		Downbytes:            450_000_000,
		FirstMs:              int32Ptr(0),
		LastMs:               int32Ptr(7200000),
		TrackCount:           2,
		Inputs:               1,
		Outputs:              3,
		NodeId:               stringPtr("node_demo_us_west_01"),
		NodeName:             stringPtr("US West Primary"),
		Latitude:             float64Ptr(37.7749),
		Longitude:            float64Ptr(-122.4194),
		Location:             stringPtr("San Francisco, US"),
		Status:               stringPtr("live"),
		LastUpdated:          timestamppb.New(now),
		CreatedAt:            timestamppb.New(startTime),
		CurrentBufferState: stringPtr("FULL"),
		CurrentCodec:         stringPtr("H264"),
		CurrentFps:           float32Ptr(30.0),
		CurrentResolution:    stringPtr("1920x1080"),
		MistStatus:           stringPtr("live"),
		QualityTier:          stringPtr("1080p30"),
		AvgViewers:           34.7,
		UniqueCountries:      12,
		UniqueCities:         28,
		AvgBufferHealth:      0.95,
		AvgBitrate:           2450,
		AvgConnectionQuality: 0.98,
		PacketLossRate:       0.0005,
		TotalViews:           1247,  // Total view count (session starts)
		UniqueViewers:        342,   // Unique viewer count (distinct sessions)
	}
}

// GenerateViewerMetrics creates demo viewer metrics
func GenerateViewerMetrics() []*pb.ViewerSession {
	now := time.Now()
	metrics := make([]*pb.ViewerSession, 20)

	// Simulate viewer count over last 20 intervals (5 min each)
	viewerCounts := []int32{12, 15, 23, 34, 45, 67, 89, 85, 78, 92, 87, 76, 65, 58, 71, 69, 54, 47, 39, 31}
	countries := []string{"US", "GB", "DE", "FR", "JP", "AU", "CA", "BR", "IN", "ES"}
	cities := []string{"San Francisco", "London", "Berlin", "Paris", "Tokyo", "Sydney", "Toronto", "Sao Paulo", "Mumbai", "Madrid"}

	for i, count := range viewerCounts {
		countryIdx := i % len(countries)
		cityIdx := i % len(cities)
		metrics[i] = &pb.ViewerSession{
			SessionId:         "demo_session_" + string(rune('a'+i)),
			Timestamp:         timestamppb.New(now.Add(-time.Duration(19-i) * 5 * time.Minute)),
			InternalName:      "demo_live_stream_001",
			TenantId:          "demo_tenant_frameworks",
			NodeId:            "node_demo_us_west_01",
			ConnectionAddr:    "192.168.1." + string(rune('1'+i)),
			Connector:         "webrtc",
			CountryCode:       countries[countryIdx],
			City:              cities[cityIdx],
			Latitude:          37.7749 + float64(i)*0.1,
			Longitude:         -122.4194 + float64(i)*0.1,
			DurationSeconds:   int64(300 + i*60),
			BytesUp:           int64(1000 + i*100),
			BytesDown:         int64(50000 + i*5000),
			ViewerCount:       count,
			ConnectionType:    "webrtc",
			ConnectionQuality: 0.9 + float32(i%10)*0.01,
			BufferHealth:      0.85 + float32(i%15)*0.01,
		}
	}

	return metrics
}

// GenerateViewerCountTimeSeries creates demo time-bucketed viewer counts for charts
func GenerateViewerCountTimeSeries() []*pb.ViewerCountBucket {
	now := time.Now()
	buckets := make([]*pb.ViewerCountBucket, 24)

	// Simulate viewer count over last 24 buckets (5 min each = 2 hours)
	viewerCounts := []int32{12, 15, 23, 34, 45, 67, 89, 85, 78, 92, 87, 76, 65, 58, 71, 69, 54, 47, 39, 31, 28, 35, 42, 55}

	for i, count := range viewerCounts {
		buckets[i] = &pb.ViewerCountBucket{
			Timestamp:    timestamppb.New(now.Add(-time.Duration(23-i) * 5 * time.Minute)),
			ViewerCount:  count,
			InternalName: "demo_live_stream_001",
		}
	}

	return buckets
}

// GenerateBillingTiers creates demo billing tier data
func GenerateBillingTiers() []*pb.BillingTier {
	return []*pb.BillingTier{
		{
			Id:          "tier_demo_starter",
			TierName:    "starter",
			DisplayName: "Starter",
			Description: "Perfect for trying out FrameWorks",
			BasePrice:   0.00,
			Currency:    "USD",
			Features: &pb.BillingFeatures{
				Recording:    false,
				Analytics:    true,
				SupportLevel: "community",
			},
		},
		{
			Id:          "tier_demo_pro",
			TierName:    "professional",
			DisplayName: "Professional",
			Description: "For content creators and small businesses",
			BasePrice:   29.99,
			Currency:    "USD",
			Features: &pb.BillingFeatures{
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				SupportLevel:   "basic",
			},
		},
		{
			Id:          "tier_demo_enterprise",
			TierName:    "enterprise",
			DisplayName: "Enterprise",
			Description: "For large organizations with custom needs",
			BasePrice:   299.99,
			Currency:    "USD",
			Features: &pb.BillingFeatures{
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				ApiAccess:      true,
				SupportLevel:   "dedicated",
				Sla:            true,
			},
		},
	}
}

// GenerateInvoices creates demo invoice data
func GenerateInvoices() []*pb.Invoice {
	now := time.Now()

	// Build usage details for first invoice
	usageDetails1, _ := structpb.NewStruct(map[string]interface{}{
		"Streaming hours": map[string]interface{}{
			"quantity":   42.5,
			"unit_price": 0.50,
			"unit":       "hours",
		},
		"Storage GB": map[string]interface{}{
			"quantity":   15.2,
			"unit_price": 0.25,
			"unit":       "GB",
		},
	})

	// Build usage details for second invoice
	usageDetails2, _ := structpb.NewStruct(map[string]interface{}{
		"Streaming hours": map[string]interface{}{
			"quantity":   35.0,
			"unit_price": 0.50,
			"unit":       "hours",
		},
		"Storage GB": map[string]interface{}{
			"quantity":   19.0,
			"unit_price": 0.25,
			"unit":       "GB",
		},
	})

	return []*pb.Invoice{
		{
			Id:            "inv_demo_current_001",
			TenantId:      "demo_tenant_frameworks",
			Amount:        29.99,
			BaseAmount:    19.99,
			MeteredAmount: 10.00,
			Currency:      "USD",
			Status:        "paid",
			DueDate:       timestamppb.New(now.Add(24 * time.Hour)),
			UsageDetails:  usageDetails1,
			CreatedAt:     timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:     timestamppb.New(now.Add(-30 * 24 * time.Hour)),
		},
		{
			Id:            "inv_demo_previous_002",
			TenantId:      "demo_tenant_frameworks",
			Amount:        24.50,
			BaseAmount:    19.99,
			MeteredAmount: 4.51,
			Currency:      "USD",
			Status:        "paid",
			DueDate:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			PaidAt:        timestamppb.New(now.Add(-28 * 24 * time.Hour)),
			UsageDetails:  usageDetails2,
			CreatedAt:     timestamppb.New(now.Add(-60 * 24 * time.Hour)),
			UpdatedAt:     timestamppb.New(now.Add(-28 * 24 * time.Hour)),
		},
	}
}

// GenerateBillingStatus creates demo billing status
func GenerateBillingStatus() *pb.BillingStatusResponse {
	now := time.Now()
	nextBilling := now.Add(18 * 24 * time.Hour)

	return &pb.BillingStatusResponse{
		TenantId: "demo_tenant_frameworks",
		Subscription: &pb.TenantSubscription{
			Id:              "sub_demo_123",
			TenantId:        "demo_tenant_frameworks",
			TierId:          "tier_professional",
			Status:          "active",
			BillingEmail:    "demo@frameworks.example",
			StartedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			NextBillingDate: timestamppb.New(nextBilling),
		},
		Tier: &pb.BillingTier{
			Id:          "tier_professional",
			TierName:    "professional",
			DisplayName: "Professional",
			Description: "For growing businesses",
			BasePrice:   99.00,
			Currency:    "USD",
		},
		BillingStatus:     "active",
		NextBillingDate:   timestamppb.New(nextBilling),
		OutstandingAmount: 0.00,
		Currency:          "USD",
		PendingInvoices:   []*pb.Invoice{},
		RecentPayments:    []*pb.Payment{},
		UsageSummary: &pb.UsageSummary{
			BillingMonth:       now.Format("2006-01"),
			Period:             "1d",
			Timestamp:          timestamppb.Now(),
			StreamHours:        42.5,
			EgressGb:           25.4,
			RecordingGb:        8.2,
			PeakBandwidthMbps:  156.8,
			TranscodingMinutes: 0, // Placeholder until Livepeer metering
			StorageGb:          12.5,
			AverageStorageGb:   11.8,
			ClipsAdded:         3,
			ClipsDeleted:       1,
			ClipStorageAddedGb: 0.5,
			ClipStorageDeletedGb: 0.2,
			TotalStreams:       8,
			TotalViewers:       1847,
			ViewerHours:        156.3,
			PeakViewers:        342,
			MaxViewers:         342,
			UniqueUsers:        1203,
			AvgViewers:         145.5,
			UniqueCountries:    12,
			UniqueCities:       58,
			AvgBufferHealth:    0.94,
			AvgBitrate:         3250,
			PacketLossRate:     0.0008,
			GeoBreakdown: []*pb.CountryMetrics{
				{CountryCode: "US", ViewerCount: 245, ViewerHours: 82.5, Percentage: 52.3, EgressGb: 13.2},
				{CountryCode: "GB", ViewerCount: 78, ViewerHours: 28.1, Percentage: 16.6, EgressGb: 4.2},
				{CountryCode: "DE", ViewerCount: 52, ViewerHours: 18.4, Percentage: 11.1, EgressGb: 2.8},
				{CountryCode: "FR", ViewerCount: 38, ViewerHours: 13.2, Percentage: 8.1, EgressGb: 2.1},
				{CountryCode: "JP", ViewerCount: 25, ViewerHours: 8.7, Percentage: 5.3, EgressGb: 1.4},
			},
		},
	}
}

// GenerateUsageRecords creates demo usage records
func GenerateUsageRecords() []*pb.UsageRecord {
	now := time.Now()
	records := make([]*pb.UsageRecord, 10)

	usageTypes := []string{"streaming", "storage", "bandwidth", "transcoding"}

	for i := 0; i < 10; i++ {
		usageType := usageTypes[i%len(usageTypes)]
		var value float64

		switch usageType {
		case "streaming":
			value = float64(2 + i*3) // hours
		case "storage":
			value = float64(5 + i*2) // GB
		case "bandwidth":
			value = float64(50 + i*25) // GB
		case "transcoding":
			value = float64(10 + i*5) // minutes
		}

		// Build usage details as structpb.Struct
		usageDetails, _ := structpb.NewStruct(map[string]interface{}{
			"cost": map[string]interface{}{
				"quantity":   value,
				"unit_price": 0.5,
				"unit":       "units",
			},
		})

		records[i] = &pb.UsageRecord{
			Id:           "usage_demo_" + time.Now().Format("20060102") + "_" + string(rune(i+'a')),
			TenantId:     "demo_tenant_frameworks",
			ClusterId:    "cluster_demo_us_west",
			ClusterName:  stringPtr("US West Demo Cluster"),
			UsageType:    usageType,
			UsageValue:   value,
			UsageDetails: usageDetails,
			BillingMonth: now.Format("2006-01"),
			CreatedAt:    timestamppb.New(now.Add(-time.Duration(i) * 24 * time.Hour)),
		}
	}

	return records
}

// GenerateDeveloperTokens creates demo API tokens
func GenerateDeveloperTokens() []*pb.APITokenInfo {
	now := time.Now()

	return []*pb.APITokenInfo{
		{
			Id:          "token_demo_production",
			TokenName:   "Production API Access",
			Permissions: []string{"streams:read", "streams:write", "analytics:read"},
			Status:      "active",
			LastUsedAt:  timestamppb.New(now.Add(-2 * time.Hour)),
			ExpiresAt:   timestamppb.New(now.Add(365 * 24 * time.Hour)),
			CreatedAt:   timestamppb.New(now.Add(-60 * 24 * time.Hour)),
		},
		{
			Id:          "token_demo_readonly",
			TokenName:   "Analytics Dashboard",
			Permissions: []string{"analytics:read", "streams:read"},
			Status:      "active",
			LastUsedAt:  timestamppb.New(now.Add(-30 * time.Minute)),
			ExpiresAt:   nil, // No expiration
			CreatedAt:   timestamppb.New(now.Add(-30 * 24 * time.Hour)),
		},
		{
			Id:          "token_demo_revoked",
			TokenName:   "Old Integration Token",
			Permissions: []string{"streams:read", "streams:write"},
			Status:      "revoked",
			LastUsedAt:  timestamppb.New(now.Add(-10 * 24 * time.Hour)),
			ExpiresAt:   timestamppb.New(now.Add(30 * 24 * time.Hour)),
			CreatedAt:   timestamppb.New(now.Add(-90 * 24 * time.Hour)),
		},
	}
}

// GenerateTenant creates demo tenant data
func GenerateTenant() *pb.Tenant {
	now := time.Now()
	return &pb.Tenant{
		Id:                     "demo_tenant_frameworks",
		Name:                   "FrameWorks Demo Organization",
		Subdomain:             stringPtr("demo"),
		PrimaryColor:          "#3B82F6", // Blue
		SecondaryColor:        "#1F2937", // Dark gray
		DeploymentTier:        "edge",
		DeploymentModel:       "multi-region",
		PrimaryDeploymentTier: "edge",
		PrimaryClusterId:      stringPtr("cluster_demo_us_west"),
		IsActive:               true,
		CreatedAt:              timestamppb.New(now.Add(-180 * 24 * time.Hour)),
		UpdatedAt:              timestamppb.New(now.Add(-1 * 24 * time.Hour)),
	}
}

// GeneratePlatformOverview creates demo platform metrics
func GeneratePlatformOverview() *pb.GetPlatformOverviewResponse {
	now := time.Now()

	return &pb.GetPlatformOverviewResponse{
		TenantId:              "demo_tenant_frameworks",
		TotalUsers:            156,
		ActiveUsers:           120,
		TotalStreams:          42,
		ActiveStreams:         7,
		TotalViewers:          1247,
		AverageViewers:        54.2,
		PeakBandwidth:         850.5,
		GeneratedAt:           timestamppb.New(now),
		StreamHours:           284.5,   // ~12 days of streaming
		EgressGb:              1247.8,  // ~1.2 TB egress
		PeakViewers:           342,     // Unique viewers (legacy field)
		TotalUploadBytes:      52428800000,  // ~50 GB uploaded
		TotalDownloadBytes:    1340000000000, // ~1.2 TB downloaded
		ViewerHours:           4892.5,  // Total accumulated watch time
		DeliveredMinutes:      293550,  // viewerHours * 60
		UniqueViewers:         342,     // Distinct viewer sessions
		IngestHours:           284.5,   // Same as StreamHours (alias)
		PeakConcurrentViewers: 89,      // Max concurrent viewers at any instant
		TotalViews:            8734,    // Total view sessions started
	}
}

// GenerateStreamEvents creates demo stream events for subscription
func GenerateStreamEvents() []*pb.StreamEvent {
	return []*pb.StreamEvent{
		{
			Timestamp: timestamppb.Now(),
			EventId:   "evt_stream_start",
			EventType: "STREAM_START",
			Status:    "LIVE",
			NodeId:    "node_demo_us_west_01",
			EventData: "{\"internal_name\":\"demo_live_stream_001\"}",
		},
		{
			Timestamp: timestamppb.New(time.Now().Add(30 * time.Second)),
			EventId:   "evt_buffer_update",
			EventType: "BUFFER_UPDATE",
			Status:    "LIVE",
			NodeId:    "node_demo_us_west_01",
			EventData: "{\"buffer_health\":95}",
		},
	}
}

// GenerateViewerMetricsEvents creates demo viewer metrics for subscription
// ViewerMetrics is now bound to proto.ClientLifecycleUpdate
func GenerateViewerMetricsEvents() []*pb.ClientLifecycleUpdate {
	return []*pb.ClientLifecycleUpdate{
		{
			NodeId:          "node_demo_us_west_01",
			InternalName:    "demo_live_stream_001",
			Action:          "connect",
			Protocol:        "hls",
			Host:            "192.168.1.100",
			SessionId:       stringPtr("sess_demo_001"),
			ConnectionTime:  float32Ptr(45.5),
			BandwidthOutBps: uint64Ptr(2500000),
			BytesDownloaded: uint64Ptr(125000000),
			Timestamp:       time.Now().Unix(),
		},
		{
			NodeId:          "node_demo_us_west_01",
			InternalName:    "demo_live_stream_001",
			Action:          "connect",
			Protocol:        "webrtc",
			Host:            "192.168.1.101",
			SessionId:       stringPtr("sess_demo_002"),
			ConnectionTime:  float32Ptr(120.3),
			BandwidthOutBps: uint64Ptr(3200000),
			BytesDownloaded: uint64Ptr(384000000),
			Timestamp:       time.Now().Add(30 * time.Second).Unix(),
		},
	}
}

// GenerateTrackListEvents creates demo track list events for subscription
// TrackListEvent is now bound to proto.StreamTrackListTrigger
func GenerateTrackListEvents() []*pb.StreamTrackListTrigger {
	return []*pb.StreamTrackListTrigger{
		{
			StreamName: "demo_live_stream_001",
			Tracks: []*pb.StreamTrack{
				{
					TrackName:   "video_main",
					TrackType:   "video",
					Codec:       "H264",
					BitrateKbps: int32Ptr(2500),
					Width:       int32Ptr(1920),
					Height:      int32Ptr(1080),
					Fps:         float64Ptr(30.0),
				},
				{
					TrackName:   "audio_main",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: int32Ptr(128),
					Channels:    int32Ptr(2),
					SampleRate:  int32Ptr(48000),
				},
			},
			TotalTracks:     int32Ptr(2),
			VideoTrackCount: int32Ptr(1),
			AudioTrackCount: int32Ptr(1),
			PrimaryWidth:    int32Ptr(1920),
			PrimaryHeight:   int32Ptr(1080),
			PrimaryFps:      float64Ptr(30.0),
		},
		{
			StreamName: "demo_live_stream_001",
			Tracks: []*pb.StreamTrack{
				{
					TrackName:   "video_main",
					TrackType:   "video",
					Codec:       "H264",
					BitrateKbps: int32Ptr(2400),
					Width:       int32Ptr(1920),
					Height:      int32Ptr(1080),
					Fps:         float64Ptr(29.8),
				},
				{
					TrackName:   "audio_main",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: int32Ptr(128),
					Channels:    int32Ptr(2),
					SampleRate:  int32Ptr(48000),
				},
				{
					TrackName:   "audio_spanish",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: int32Ptr(96),
					Channels:    int32Ptr(2),
					SampleRate:  int32Ptr(44100),
				},
			},
			TotalTracks:     int32Ptr(3),
			VideoTrackCount: int32Ptr(1),
			AudioTrackCount: int32Ptr(2),
			PrimaryWidth:    int32Ptr(1920),
			PrimaryHeight:   int32Ptr(1080),
			PrimaryFps:      float64Ptr(29.8),
		},
	}
}

// GenerateSystemHealthEvents creates demo system health events for subscription
// SystemHealthEvent is now bound to proto.NodeLifecycleUpdate
func GenerateSystemHealthEvents() []*pb.NodeLifecycleUpdate {
	ramMax := uint64(16 * 1024 * 1024 * 1024)
	shmTotal := uint64(4 * 1024 * 1024 * 1024)
	diskTotal := uint64(1000 * 1024 * 1024 * 1024)

	return []*pb.NodeLifecycleUpdate{
		{
			NodeId:         "node_demo_us_west_01",
			CpuTenths:      652, // 65.2%
			RamMax:         ramMax,
			RamCurrent:     uint64(float64(ramMax) * 0.785),
			ShmTotalBytes:  shmTotal,
			ShmUsedBytes:   uint64(float64(shmTotal) * 0.20),
			DiskTotalBytes: diskTotal,
			DiskUsedBytes:  uint64(float64(diskTotal) * 0.453),
			UpSpeed:        125000000,
			DownSpeed:      250000000,
			Latitude:       37.7749,
			Longitude:      -122.4194,
			Location:       "San Francisco, CA",
			ActiveStreams:  3,
			IsHealthy:      true,
			EventType:      "node_lifecycle_update",
			Timestamp:      time.Now().Unix(),
		},
		{
			NodeId:         "node_demo_us_west_02",
			CpuTenths:      721, // 72.1%
			RamMax:         ramMax,
			RamCurrent:     uint64(float64(ramMax) * 0.823),
			ShmTotalBytes:  shmTotal,
			ShmUsedBytes:   uint64(float64(shmTotal) * 0.25),
			DiskTotalBytes: diskTotal,
			DiskUsedBytes:  uint64(float64(diskTotal) * 0.387),
			UpSpeed:        100000000,
			DownSpeed:      200000000,
			Latitude:       34.0522,
			Longitude:      -118.2437,
			Location:       "Los Angeles, CA",
			ActiveStreams:  5,
			IsHealthy:      true,
			EventType:      "node_lifecycle_update",
			Timestamp:      time.Now().Add(15 * time.Second).Unix(),
		},
	}
}

// GenerateStreamHealthMetrics creates demo stream health metrics
func GenerateStreamHealthMetrics() []*pb.StreamHealthMetric {
	now := time.Now()

	videoBitrate := int32(2500)
	videoWidth := int32(1920)
	videoHeight := int32(1080)
	videoFPS := 30.0
	videoResolution := "1920x1080"
	audioBitrate := int32(128)
	audioChannels := int32(2)
	audioSampleRate := int32(48000)
	tracks := []*pb.StreamTrack{
		{
			TrackName:   "video_main",
			TrackType:   "video",
			Codec:       "H264",
			BitrateKbps: &videoBitrate,
			Width:       &videoWidth,
			Height:      &videoHeight,
			Fps:         float64Ptr(videoFPS),
			Resolution:  stringPtr(videoResolution),
		},
		{
			TrackName:   "audio_main",
			TrackType:   "audio",
			Codec:       "AAC",
			BitrateKbps: &audioBitrate,
			Channels:    &audioChannels,
			SampleRate:  &audioSampleRate,
		},
	}

	degradedBitrate := int32(2200)
	tracksDegraded := []*pb.StreamTrack{
		{
			TrackName:   "video_main",
			TrackType:   "video",
			Codec:       "H264",
			BitrateKbps: &degradedBitrate,
			Width:       &videoWidth,
			Height:      &videoHeight,
			Fps:         float64Ptr(29.8),
			Resolution:  stringPtr(videoResolution),
		},
		{
			TrackName:   "audio_main",
			TrackType:   "audio",
			Codec:       "AAC",
			BitrateKbps: &audioBitrate,
			Channels:    &audioChannels,
			SampleRate:  &audioSampleRate,
		},
	}

	packetLoss1 := 0.08
	packetLoss2 := 0.32
	qualityTierFull := "1080p30"

	return []*pb.StreamHealthMetric{
		{
			Timestamp:              timestamppb.New(now.Add(-5 * time.Minute)),
			InternalName:           "demo_live_stream_001",
			TenantId:               "demo_tenant_frameworks",
			NodeId:                 "node_demo_us_west_01",
			Bitrate:                2500000,
			Fps:                    30,
			GopSize:                60,
			Width:                  1920,
			Height:                 1080,
			BufferHealth:           0.98,
			BufferState:            "FULL",
			PacketsSent:            15420,
			PacketsLost:            12,
			Codec:                  "H264",
			Tracks:                 tracks,
			PrimaryAudioChannels:   &audioChannels,
			PrimaryAudioSampleRate: &audioSampleRate,
			PrimaryAudioCodec:      stringPtr("AAC"),
			PrimaryAudioBitrate:    &audioBitrate,
			PacketLossPercentage:   float64Ptr(packetLoss1),
			QualityTier:            &qualityTierFull,
		},
		{
			Timestamp:              timestamppb.New(now.Add(-2 * time.Minute)),
			InternalName:           "demo_live_stream_001",
			TenantId:               "demo_tenant_frameworks",
			NodeId:                 "node_demo_us_west_01",
			Bitrate:                2400000,
			Fps:                    29,
			GopSize:                64,
			Width:                  1920,
			Height:                 1080,
			BufferHealth:           0.72,
			BufferState:            "DRY",
			PacketsSent:            15890,
			PacketsLost:            45,
			Codec:                  "H264",
			Tracks:                 tracksDegraded,
			PrimaryAudioChannels:   &audioChannels,
			PrimaryAudioSampleRate: &audioSampleRate,
			PrimaryAudioCodec:      stringPtr("AAC"),
			PrimaryAudioBitrate:    &audioBitrate,
			PacketLossPercentage:   float64Ptr(packetLoss2),
			QualityTier:            &qualityTierFull,
			IssuesDescription:      stringPtr("elevated packet loss on edge node"),
		},
	}
}

// GenerateRebufferingEvents creates demo rebuffering events
func GenerateRebufferingEvents() []*model.RebufferingEvent {
	now := time.Now()
	return []*model.RebufferingEvent{
		{
			Timestamp:     now.Add(-8 * time.Minute),
			Stream:        "demo_live_stream_001",
			NodeID:        "node_demo_us_west_01",
			BufferState:   model.BufferStateDry,
			PreviousState: model.BufferStateFull,
			RebufferStart: true,
			RebufferEnd:   false,
		},
		{
			Timestamp:     now.Add(-6 * time.Minute),
			Stream:        "demo_live_stream_001",
			NodeID:        "node_demo_us_west_01",
			BufferState:   model.BufferStateRecover,
			PreviousState: model.BufferStateDry,
			RebufferStart: false,
			RebufferEnd:   true,
		},
	}
}

// GenerateViewerGeographics creates realistic demo viewer geographic data
func GenerateViewerGeographics() []*pb.ConnectionEvent {
	now := time.Now()

	return []*pb.ConnectionEvent{
		// Connect event - no duration/bytes yet
		{
			EventId:        "evt_demo_1",
			Timestamp:      timestamppb.New(now.Add(-30 * time.Minute)),
			TenantId:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionId:      "sess_demo_1",
			ConnectionAddr: "192.168.1.100",
			Connector:      "HLS",
			NodeId:         "node_demo_us_west_01",
			CountryCode:    "US",
			City:           "San Francisco",
			Latitude:       37.7749,
			Longitude:      -122.4194,
			EventType:      "connect",
		},
		// Disconnect event - has session duration and bytes
		{
			EventId:                "evt_demo_1_disconnect",
			Timestamp:              timestamppb.New(now.Add(-5 * time.Minute)),
			TenantId:               "demo_tenant_frameworks",
			InternalName:           "demo_live_stream_001",
			SessionId:              "sess_demo_1",
			ConnectionAddr:         "192.168.1.100",
			Connector:              "HLS",
			NodeId:                 "node_demo_us_west_01",
			CountryCode:            "US",
			City:                   "San Francisco",
			Latitude:               37.7749,
			Longitude:              -122.4194,
			EventType:              "disconnect",
			SessionDurationSeconds: 1500, // 25 minutes
			BytesTransferred:       256000000, // ~256 MB
		},
		// Connect event
		{
			EventId:        "evt_demo_2",
			Timestamp:      timestamppb.New(now.Add(-25 * time.Minute)),
			TenantId:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionId:      "sess_demo_2",
			ConnectionAddr: "203.0.113.45",
			Connector:      "DASH",
			NodeId:         "node_demo_eu_west_01",
			CountryCode:    "GB",
			City:           "London",
			Latitude:       51.5074,
			Longitude:      -0.1278,
			EventType:      "connect",
		},
		// Disconnect event
		{
			EventId:                "evt_demo_2_disconnect",
			Timestamp:              timestamppb.New(now.Add(-2 * time.Minute)),
			TenantId:               "demo_tenant_frameworks",
			InternalName:           "demo_live_stream_001",
			SessionId:              "sess_demo_2",
			ConnectionAddr:         "203.0.113.45",
			Connector:              "DASH",
			NodeId:                 "node_demo_eu_west_01",
			CountryCode:            "GB",
			City:                   "London",
			Latitude:               51.5074,
			Longitude:              -0.1278,
			EventType:              "disconnect",
			SessionDurationSeconds: 1380, // 23 minutes
			BytesTransferred:       189000000, // ~189 MB
		},
		// Connect event - still connected
		{
			EventId:        "evt_demo_3",
			Timestamp:      timestamppb.New(now.Add(-20 * time.Minute)),
			TenantId:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionId:      "sess_demo_3",
			ConnectionAddr: "198.51.100.78",
			Connector:      "WebRTC",
			NodeId:         "node_demo_ap_east_01",
			CountryCode:    "JP",
			City:           "Tokyo",
			Latitude:       35.6762,
			Longitude:      139.6503,
			EventType:      "connect",
		},
		// Short session disconnect
		{
			EventId:                "evt_demo_4_disconnect",
			Timestamp:              timestamppb.New(now.Add(-10 * time.Minute)),
			TenantId:               "demo_tenant_frameworks",
			InternalName:           "demo_live_stream_001",
			SessionId:              "sess_demo_4",
			ConnectionAddr:         "45.33.32.156",
			Connector:              "HLS",
			NodeId:                 "node_demo_us_west_01",
			CountryCode:            "DE",
			City:                   "Berlin",
			Latitude:               52.5200,
			Longitude:              13.4050,
			EventType:              "disconnect",
			SessionDurationSeconds: 45, // 45 seconds (short session)
			BytesTransferred:       5200000, // ~5 MB
		},
	}
}

// GenerateGeographicDistribution creates realistic demo geographic distribution data
func GenerateGeographicDistribution() *model.GeographicDistribution {
	now := time.Now()

	return &model.GeographicDistribution{
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(now.Add(-24 * time.Hour)),
			End:   timestamppb.New(now),
		},
		Stream:          func() *string { s := "demo_live_stream_001"; return &s }(),
		UniqueCountries: 5,
		UniqueCities:    8,
		TotalViewers:    1247,
		TopCountries: []*pb.CountryMetric{
			{
				CountryCode: "US",
				ViewerCount: 623,
				Percentage:  49.9,
			},
			{
				CountryCode: "GB",
				ViewerCount: 298,
				Percentage:  23.9,
			},
			{
				CountryCode: "JP",
				ViewerCount: 187,
				Percentage:  15.0,
			},
		},
		TopCities: []*pb.CityMetric{
			{
				City:        "San Francisco",
				CountryCode: "US",
				ViewerCount: 234,
				Percentage:  18.8,
				Latitude:    37.7749,
				Longitude:   -122.4194,
			},
			{
				City:        "London",
				CountryCode: "GB",
				ViewerCount: 201,
				Percentage:  16.1,
				Latitude:    51.5074,
				Longitude:   -0.1278,
			},
			{
				City:        "New York",
				CountryCode: "US",
				ViewerCount: 189,
				Percentage:  15.2,
				Latitude:    40.7128,
				Longitude:   -74.0060,
			},
		},
		ViewersByCountry: []*model.CountryTimeSeries{
			{
				Timestamp:   now.Add(-23 * time.Hour),
				CountryCode: "US",
				ViewerCount: 45,
			},
			{
				Timestamp:   now.Add(-22 * time.Hour),
				CountryCode: "US",
				ViewerCount: 67,
			},
			{
				Timestamp:   now.Add(-21 * time.Hour),
				CountryCode: "GB",
				ViewerCount: 23,
			},
			{
				Timestamp:   now.Add(-20 * time.Hour),
				CountryCode: "JP",
				ViewerCount: 15,
			},
		},
	}
}

// GenerateLoadBalancingMetrics creates realistic demo load balancing metrics data
func GenerateLoadBalancingMetrics() []*pb.RoutingEvent {
	now := time.Now()

	str := func(s string) *string { return &s }
	f64 := func(f float64) *float64 { return &f }
	i32 := func(i int32) *int32 { return &i }

	// H3 index examples at resolution 5 (~25km hexagons)
	// These are valid H3 indexes for the given lat/lng coordinates
	sfClientBucket := &pb.GeoBucket{H3Index: 0x85283473fffffff, Resolution: 5}   // San Francisco area
	sfNodeBucket := &pb.GeoBucket{H3Index: 0x85283477fffffff, Resolution: 5}     // Palo Alto area
	londonClientBucket := &pb.GeoBucket{H3Index: 0x85194ad7fffffff, Resolution: 5} // London area
	londonNodeBucket := &pb.GeoBucket{H3Index: 0x85194ad3fffffff, Resolution: 5}   // London node
	tokyoClientBucket := &pb.GeoBucket{H3Index: 0x8529a927fffffff, Resolution: 5}  // Tokyo area
	tokyoNodeBucket := &pb.GeoBucket{H3Index: 0x8529a923fffffff, Resolution: 5}    // Tokyo node
	nyClientBucket := &pb.GeoBucket{H3Index: 0x85282607fffffff, Resolution: 5}     // New York area

	return []*pb.RoutingEvent{
		// US West routing events - multiple to same node for realistic counts
		{
			Id:              "demo_routing_001",
			Timestamp:       timestamppb.New(now.Add(-30 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-1",
			NodeId:          str("edge-node-1"),
			ClientCountry:   str("US"),
			ClientLatitude:  f64(37.7749),
			ClientLongitude: f64(-122.4194),
			NodeLatitude:    f64(37.4419),
			NodeLongitude:   f64(-122.1430),
			Score:           i32(2850),
			Status:          "success",
			Details:         str("optimal_routing"),
			RoutingDistance: f64(42.3),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 3,
			LatencyMs:       12.5,
			ClientBucket:    sfClientBucket,
			NodeBucket:      sfNodeBucket,
		},
		{
			Id:              "demo_routing_002",
			Timestamp:       timestamppb.New(now.Add(-28 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-1",
			NodeId:          str("edge-node-1"),
			ClientCountry:   str("US"),
			ClientLatitude:  f64(37.8044),
			ClientLongitude: f64(-122.2712),
			NodeLatitude:    f64(37.4419),
			NodeLongitude:   f64(-122.1430),
			Score:           i32(2780),
			Status:          "success",
			Details:         str("optimal_routing"),
			RoutingDistance: f64(48.1),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 3,
			LatencyMs:       11.2,
			ClientBucket:    sfClientBucket,
			NodeBucket:      sfNodeBucket,
		},
		{
			Id:              "demo_routing_003",
			Timestamp:       timestamppb.New(now.Add(-25 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-1",
			NodeId:          str("edge-node-1"),
			ClientCountry:   str("US"),
			ClientLatitude:  f64(40.7128),
			ClientLongitude: f64(-74.0060),
			NodeLatitude:    f64(37.4419),
			NodeLongitude:   f64(-122.1430),
			Score:           i32(2100),
			Status:          "success",
			Details:         str("cross_region_fallback"),
			RoutingDistance: f64(4130.5),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 2,
			LatencyMs:       45.3,
			ClientBucket:    nyClientBucket,
			NodeBucket:      sfNodeBucket,
		},
		// EU routing events
		{
			Id:              "demo_routing_004",
			Timestamp:       timestamppb.New(now.Add(-22 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-2",
			NodeId:          str("edge-node-2"),
			ClientCountry:   str("GB"),
			ClientLatitude:  f64(51.5074),
			ClientLongitude: f64(-0.1278),
			NodeLatitude:    f64(51.4994),
			NodeLongitude:   f64(-0.1270),
			Score:           i32(3100),
			Status:          "success",
			Details:         str("regional_optimal"),
			RoutingDistance: f64(1.2),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 4,
			LatencyMs:       8.7,
			ClientBucket:    londonClientBucket,
			NodeBucket:      londonNodeBucket,
		},
		{
			Id:              "demo_routing_005",
			Timestamp:       timestamppb.New(now.Add(-20 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-2",
			NodeId:          str("edge-node-2"),
			ClientCountry:   str("GB"),
			ClientLatitude:  f64(51.4545),
			ClientLongitude: f64(-2.5879),
			NodeLatitude:    f64(51.4994),
			NodeLongitude:   f64(-0.1270),
			Score:           i32(2900),
			Status:          "success",
			Details:         str("regional_optimal"),
			RoutingDistance: f64(172.8),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 4,
			LatencyMs:       15.2,
			ClientBucket:    londonClientBucket,
			NodeBucket:      londonNodeBucket,
		},
		// AP routing events
		{
			Id:              "demo_routing_006",
			Timestamp:       timestamppb.New(now.Add(-18 * time.Minute)),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "edge-node-3",
			NodeId:          str("edge-node-3"),
			ClientCountry:   str("JP"),
			ClientLatitude:  f64(35.6762),
			ClientLongitude: f64(139.6503),
			NodeLatitude:    f64(35.6804),
			NodeLongitude:   f64(139.7690),
			Score:           i32(2950),
			Status:          "success",
			Details:         str("ap_regional"),
			RoutingDistance: f64(13.8),
			EventType:       str("load_balancing"),
			Source:          str("foghorn"),
			CandidatesCount: 2,
			LatencyMs:       22.1,
			ClientBucket:    tokyoClientBucket,
			NodeBucket:      tokyoNodeBucket,
		},
	}
}

// GenerateViewerEndpointResponse returns a demo viewer endpoint resolution payload
func GenerateViewerEndpointResponse(contentType, contentID string) *pb.ViewerEndpointResponse {
	if contentType == "" {
		contentType = "live"
	}
	if contentID == "" {
		contentID = "demo_live_stream_001"
	}

	primaryOutputs := map[string]*pb.OutputEndpoint{
		"WHEP": {
			Protocol: "WHEP",
			Url:      "https://edge.demo.frameworks.video/whep/demo_live_stream_001",
			Capabilities: &pb.OutputCapability{
				SupportsSeek:          false,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
		"HLS": {
			Protocol: "HLS",
			Url:      "https://edge.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
			Capabilities: &pb.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
	}

	fallbackOutputs := map[string]*pb.OutputEndpoint{
		"HLS": {
			Protocol: "HLS",
			Url:      "https://edge.eu.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
			Capabilities: &pb.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
		"HTTP": {
			Protocol: "HTTP",
			Url:      "https://edge.eu.demo.frameworks.video/http/demo_live_stream_001",
			Capabilities: &pb.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: false,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"MP4"},
			},
		},
	}

	primary := &pb.ViewerEndpoint{
		NodeId:      "node_demo_us_west_01",
		BaseUrl:     "https://edge.demo.frameworks.video",
		Protocol:    "webrtc",
		Url:         "https://edge.demo.frameworks.video/webrtc/demo_live_stream_001",
		GeoDistance: 18.4,
		LoadScore:   0.72,
		Outputs:     primaryOutputs,
	}

	fallback := &pb.ViewerEndpoint{
		NodeId:      "node_demo_eu_west_01",
		BaseUrl:     "https://edge.eu.demo.frameworks.video",
		Protocol:    "hls",
		Url:         "https://edge.eu.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
		GeoDistance: 4567.0,
		LoadScore:   0.81,
		Outputs:     fallbackOutputs,
	}

	now := time.Now()
	durationSeconds := int32(0)
	metadata := &pb.PlaybackMetadata{
		Status:      "live",
		IsLive:      true,
		Viewers:     132,
		BufferState: "FULL",
		TenantId:      "demo_tenant_frameworks",
		ContentId:     contentID,
		ContentType:   contentType,
		ProtocolHints: []string{"WHEP", "HLS", "HTTP"},
		Tracks: []*pb.PlaybackTrack{
			{Type: "video", Codec: "H264", BitrateKbps: 2500, Width: 1920, Height: 1080},
			{Type: "audio", Codec: "AAC", BitrateKbps: 128, Channels: 2, SampleRate: 48000},
		},
		Instances: []*pb.PlaybackInstance{
			{
				NodeId:           "node_demo_us_west_01",
				Viewers:          78,
				BufferState:      "FULL",
				BytesUp:          3_456_789,
				BytesDown:        5_678_901,
				TotalConnections: 120,
				Inputs:           1,
				LastUpdate:       timestamppb.New(now.Add(-25 * time.Second)),
			},
			{
				NodeId:           "node_demo_eu_west_01",
				Viewers:          54,
				BufferState:      "RECOVER",
				BytesUp:          2_345_678,
				BytesDown:        4_321_987,
				TotalConnections: 96,
				Inputs:           1,
				LastUpdate:       timestamppb.New(now.Add(-40 * time.Second)),
			},
		},
		CreatedAt:       timestamppb.New(now),
		DurationSeconds: &durationSeconds,
	}

	return &pb.ViewerEndpointResponse{
		Primary:   primary,
		Fallbacks: []*pb.ViewerEndpoint{fallback},
		Metadata:  metadata,
	}
}

// ============================================================================
// Connection-style Demo Generators (for Relay pagination)
// ============================================================================

// GenerateRoutingEventsConnection creates demo routing events with pagination
func GenerateRoutingEventsConnection() *model.RoutingEventsConnection {
	events := GenerateLoadBalancingMetrics()
	edges := make([]*model.RoutingEventEdge, len(events))
	for i, event := range events {
		cursor := event.Id
		if cursor == "" {
			cursor = fmt.Sprintf("cursor_%d", i)
		}
		edges[i] = &model.RoutingEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	return &model.RoutingEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateConnectionEventsConnection creates demo connection events with pagination
func GenerateConnectionEventsConnection() *model.ConnectionEventsConnection {
	events := GenerateViewerGeographics()
	edges := make([]*model.ConnectionEventEdge, len(events))
	for i, event := range events {
		cursor := event.EventId
		if cursor == "" {
			cursor = fmt.Sprintf("cursor_%d", i)
		}
		edges[i] = &model.ConnectionEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	return &model.ConnectionEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateClipEventsConnection creates demo clip events with pagination
func GenerateClipEventsConnection() *model.ClipEventsConnection {
	now := time.Now()

	events := []*pb.ClipEvent{
		{
			RequestId:    "clip_req_demo_001",
			Timestamp:    timestamppb.New(now.Add(-30 * time.Minute)),
			InternalName: "demo_live_stream_001",
			Stage:        "COMPLETED",
			ContentType:  stringPtr("clip"),
			StartUnix:    int64Ptr(now.Add(-90 * time.Minute).Unix()),
			StopUnix:     int64Ptr(now.Add(-30 * time.Minute).Unix()),
			IngestNodeId: stringPtr("node_demo_us_west_01"),
			Percent:      uint32Ptr(100),
			FilePath:     stringPtr("/clips/demo_clip_001.mp4"),
			S3Url:        stringPtr("s3://demo-bucket/clips/demo_clip_001.mp4"),
			SizeBytes:    uint64Ptr(15000000),
		},
		{
			RequestId:    "clip_req_demo_002",
			Timestamp:    timestamppb.New(now.Add(-15 * time.Minute)),
			InternalName: "demo_live_stream_001",
			Stage:        "PROCESSING",
			ContentType:  stringPtr("clip"),
			StartUnix:    int64Ptr(now.Add(-45 * time.Minute).Unix()),
			StopUnix:     int64Ptr(now.Add(-15 * time.Minute).Unix()),
			IngestNodeId: stringPtr("node_demo_us_west_01"),
			Percent:      uint32Ptr(65),
			Message:      stringPtr("Encoding video..."),
		},
	}

	edges := make([]*model.ClipEventEdge, len(events))
	for i, event := range events {
		edges[i] = &model.ClipEventEdge{
			Cursor: event.RequestId,
			Node:   event,
		}
	}

	return &model.ClipEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateNodeMetricsConnection creates demo node metrics with pagination
func GenerateNodeMetricsConnection() *model.NodeMetricsConnection {
	now := time.Now()

	metrics := []*pb.NodeMetric{
		{
			Id:           "nm_demo_001",
			Timestamp:    timestamppb.New(now.Add(-5 * time.Minute)),
			NodeId:       "node_demo_us_west_01",
			CpuUsage:     65.2,
			RamMax:       16000000000,
			RamCurrent:   12500000000,
			BandwidthIn:  125000000,
			BandwidthOut: 250000000,
			IsHealthy:    true,
			Latitude:     37.7749,
			Longitude:    -122.4194,
		},
		{
			Id:           "nm_demo_002",
			Timestamp:    timestamppb.New(now.Add(-10 * time.Minute)),
			NodeId:       "node_demo_us_west_01",
			CpuUsage:     58.7,
			RamMax:       16000000000,
			RamCurrent:   11800000000,
			BandwidthIn:  118000000,
			BandwidthOut: 235000000,
			IsHealthy:    true,
			Latitude:     37.7749,
			Longitude:    -122.4194,
		},
		{
			Id:           "nm_demo_003",
			Timestamp:    timestamppb.New(now.Add(-5 * time.Minute)),
			NodeId:       "node_demo_eu_west_01",
			CpuUsage:     72.1,
			RamMax:       16000000000,
			RamCurrent:   13200000000,
			BandwidthIn:  100000000,
			BandwidthOut: 200000000,
			IsHealthy:    true,
			Latitude:     51.5074,
			Longitude:    -0.1278,
		},
	}

	edges := make([]*model.NodeMetricEdge, len(metrics))
	for i, metric := range metrics {
		edges[i] = &model.NodeMetricEdge{
			Cursor: metric.Id,
			Node:   metric,
		}
	}

	return &model.NodeMetricsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(metrics),
	}
}

// GenerateNodeMetrics1hConnection creates demo hourly node metrics with pagination
func GenerateNodeMetrics1hConnection() *model.NodeMetrics1hConnection {
	now := time.Now()

	metrics := []*pb.NodeMetricHourly{
		{
			Id:                "nmh_demo_001",
			Timestamp:         timestamppb.New(now.Truncate(time.Hour)),
			NodeId:            "node_demo_us_west_01",
			AvgCpu:            62.5,
			PeakCpu:           78.3,
			AvgMemory:         78.1,
			PeakMemory:        82.4,
			TotalBandwidthIn:  120000000,
			TotalBandwidthOut: 240000000,
			WasHealthy:        true,
			AvgDisk:           45.2,
			PeakDisk:          52.1,
		},
		{
			Id:                "nmh_demo_002",
			Timestamp:         timestamppb.New(now.Add(-1 * time.Hour).Truncate(time.Hour)),
			NodeId:            "node_demo_us_west_01",
			AvgCpu:            55.8,
			PeakCpu:           71.2,
			AvgMemory:         72.3,
			PeakMemory:        78.1,
			TotalBandwidthIn:  110000000,
			TotalBandwidthOut: 220000000,
			WasHealthy:        true,
			AvgDisk:           44.1,
			PeakDisk:          50.3,
		},
	}

	edges := make([]*model.NodeMetricHourlyEdge, len(metrics))
	for i, metric := range metrics {
		edges[i] = &model.NodeMetricHourlyEdge{
			Cursor: metric.Id,
			Node:   metric,
		}
	}

	return &model.NodeMetrics1hConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(metrics),
	}
}

// GenerateStreamHealthMetricsConnection creates demo stream health metrics with pagination
func GenerateStreamHealthMetricsConnection() *model.StreamHealthMetricsConnection {
	metrics := GenerateStreamHealthMetrics()
	edges := make([]*model.StreamHealthMetricEdge, len(metrics))
	for i, metric := range metrics {
		cursor := fmt.Sprintf("shm_cursor_%d", i)
		edges[i] = &model.StreamHealthMetricEdge{
			Cursor: cursor,
			Node:   metric,
		}
	}

	return &model.StreamHealthMetricsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(metrics),
	}
}

// GenerateTrackListEventsConnection creates demo track list events with pagination
func GenerateTrackListEventsConnection() *model.TrackListEventsConnection {
	now := time.Now()

	events := []*pb.TrackListEvent{
		{
			Id:        "tle_demo_001",
			Timestamp: timestamppb.New(now.Add(-10 * time.Minute)),
			NodeId:    "node_demo_us_west_01",
			Stream:    "demo_live_stream_001",
			TrackList: `[{"trackName":"video_main","trackType":"video","codec":"H264","width":1920,"height":1080,"fps":30},{"trackName":"audio_main","trackType":"audio","codec":"AAC"}]`,
			Tracks: []*pb.StreamTrack{
				{
					TrackName: "video_main",
					TrackType: "video",
					Codec:     "H264",
					Width:     int32Ptr(1920),
					Height:    int32Ptr(1080),
					Fps:       float64Ptr(30.0),
				},
				{
					TrackName: "audio_main",
					TrackType: "audio",
					Codec:     "AAC",
				},
			},
			TrackCount: 2,
		},
		{
			Id:        "tle_demo_002",
			Timestamp: timestamppb.New(now.Add(-5 * time.Minute)),
			NodeId:    "node_demo_us_west_01",
			Stream:    "demo_live_stream_001",
			TrackList: `[{"trackName":"video_main","trackType":"video","codec":"H264","width":1920,"height":1080,"fps":30},{"trackName":"audio_main","trackType":"audio","codec":"AAC"},{"trackName":"audio_spanish","trackType":"audio","codec":"AAC"}]`,
			Tracks: []*pb.StreamTrack{
				{
					TrackName: "video_main",
					TrackType: "video",
					Codec:     "H264",
					Width:     int32Ptr(1920),
					Height:    int32Ptr(1080),
					Fps:       float64Ptr(30.0),
				},
				{
					TrackName: "audio_main",
					TrackType: "audio",
					Codec:     "AAC",
				},
				{
					TrackName: "audio_spanish",
					TrackType: "audio",
					Codec:     "AAC",
				},
			},
			TrackCount: 3,
		},
	}

	edges := make([]*model.TrackListEventEdge, len(events))
	for i, event := range events {
		edges[i] = &model.TrackListEventEdge{
			Cursor: event.Id,
			Node:   event,
		}
	}

	return &model.TrackListEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateStreamEventsConnection creates demo stream events with pagination
func GenerateStreamEventsConnection() *model.StreamEventsConnection {
	events := GenerateStreamEvents()
	edges := make([]*model.StreamEventEdge, len(events))
	for i, event := range events {
		cursor := event.EventId
		if cursor == "" {
			cursor = fmt.Sprintf("se_cursor_%d", i)
		}
		edges[i] = &model.StreamEventEdge{
			Cursor: cursor,
			Node:   event,
		}
	}

	return &model.StreamEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateArtifactState creates a demo artifact state for a single request ID
func GenerateArtifactState(requestID string) *pb.ArtifactState {
	now := time.Now()

	// Return a realistic artifact based on the request ID
	return &pb.ArtifactState{
		RequestId:       requestID,
		TenantId:        "demo_tenant_frameworks",
		InternalName:    "demo_live_stream_001",
		ContentType:     "clip",
		Stage:           "completed",
		ProgressPercent: 100,
		RequestedAt:     timestamppb.New(now.Add(-2 * time.Hour)),
		StartedAt:       timestamppb.New(now.Add(-2*time.Hour + 10*time.Second)),
		CompletedAt:     timestamppb.New(now.Add(-1 * time.Hour)),
		FilePath:        stringPtr("/clips/" + requestID + ".mp4"),
		S3Url:           stringPtr("s3://demo-bucket/clips/" + requestID + ".mp4"),
		SizeBytes:       uint64Ptr(15000000),
		UpdatedAt:       timestamppb.New(now.Add(-1 * time.Hour)),
	}
}

// GenerateArtifactStatesConnection creates demo artifact states with pagination
func GenerateArtifactStatesConnection() *model.ArtifactStatesConnection {
	now := time.Now()

	artifacts := []*pb.ArtifactState{
		{
			RequestId:       "artifact_demo_001",
			TenantId:        "demo_tenant_frameworks",
			InternalName:    "demo_live_stream_001",
			ContentType:     "clip",
			Stage:           "completed",
			ProgressPercent: 100,
			RequestedAt:     timestamppb.New(now.Add(-2 * time.Hour)),
			StartedAt:       timestamppb.New(now.Add(-2*time.Hour + 10*time.Second)),
			CompletedAt:     timestamppb.New(now.Add(-1 * time.Hour)),
			FilePath:        stringPtr("/clips/demo_clip_001.mp4"),
			S3Url:           stringPtr("s3://demo-bucket/clips/demo_clip_001.mp4"),
			SizeBytes:       uint64Ptr(15000000),
			UpdatedAt:       timestamppb.New(now.Add(-1 * time.Hour)),
		},
		{
			RequestId:       "artifact_demo_002",
			TenantId:        "demo_tenant_frameworks",
			InternalName:    "demo_live_stream_001",
			ContentType:     "dvr",
			Stage:           "processing",
			ProgressPercent: 45,
			RequestedAt:     timestamppb.New(now.Add(-30 * time.Minute)),
			StartedAt:       timestamppb.New(now.Add(-25 * time.Minute)),
			UpdatedAt:       timestamppb.New(now.Add(-1 * time.Minute)),
		},
		{
			RequestId:       "artifact_demo_003",
			TenantId:        "demo_tenant_frameworks",
			InternalName:    "demo_live_stream_001",
			ContentType:     "clip",
			Stage:           "processing",
			ProgressPercent: 65,
			RequestedAt:     timestamppb.New(now.Add(-10 * time.Minute)),
			StartedAt:       timestamppb.New(now.Add(-9 * time.Minute)),
			UpdatedAt:       timestamppb.New(now.Add(-5 * time.Minute)),
		},
	}

	edges := make([]*model.ArtifactStateEdge, len(artifacts))
	for i, artifact := range artifacts {
		edges[i] = &model.ArtifactStateEdge{
			Cursor: artifact.RequestId,
			Node:   artifact,
		}
	}

	return &model.ArtifactStatesConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(artifacts),
	}
}

// GenerateStreamConnectionHourlyConnection creates demo hourly connection aggregates
func GenerateStreamConnectionHourlyConnection() *model.StreamConnectionHourlyConnection {
	now := time.Now()

	records := []*pb.StreamConnectionHourly{
		{
			Id:            "sch_demo_001",
			Hour:          timestamppb.New(now.Truncate(time.Hour)),
			TenantId:      "demo_tenant_frameworks",
			InternalName:  "demo_live_stream_001",
			TotalBytes:    45000000000,
			UniqueViewers: 189,
			TotalSessions: 245,
		},
		{
			Id:            "sch_demo_002",
			Hour:          timestamppb.New(now.Add(-1 * time.Hour).Truncate(time.Hour)),
			TenantId:      "demo_tenant_frameworks",
			InternalName:  "demo_live_stream_001",
			TotalBytes:    58000000000,
			UniqueViewers: 234,
			TotalSessions: 312,
		},
		{
			Id:            "sch_demo_003",
			Hour:          timestamppb.New(now.Add(-2 * time.Hour).Truncate(time.Hour)),
			TenantId:      "demo_tenant_frameworks",
			InternalName:  "demo_live_stream_001",
			TotalBytes:    32000000000,
			UniqueViewers: 156,
			TotalSessions: 198,
		},
	}

	edges := make([]*model.StreamConnectionHourlyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.StreamConnectionHourlyEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.StreamConnectionHourlyConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateClientMetrics5mConnection creates demo 5-minute client metrics
func GenerateClientMetrics5mConnection() *model.ClientMetrics5mConnection {
	now := time.Now()

	records := []*pb.ClientMetrics5M{
		{
			Id:                   "cm5_demo_001",
			Timestamp:            timestamppb.New(now.Truncate(5 * time.Minute)),
			TenantId:             "demo_tenant_frameworks",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       45,
			AvgBandwidthIn:       2450000,
			AvgBandwidthOut:      5400000000,
			AvgConnectionTime:    1847.5,
			PacketLossRate:       float32Ptr(0.02),
			AvgConnectionQuality: float32Ptr(0.95),
		},
		{
			Id:                   "cm5_demo_002",
			Timestamp:            timestamppb.New(now.Add(-5 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:             "demo_tenant_frameworks",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       52,
			AvgBandwidthIn:       2380000,
			AvgBandwidthOut:      6200000000,
			AvgConnectionTime:    2156.3,
			PacketLossRate:       float32Ptr(0.03),
			AvgConnectionQuality: float32Ptr(0.91),
		},
		{
			Id:                   "cm5_demo_003",
			Timestamp:            timestamppb.New(now.Add(-10 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:             "demo_tenant_frameworks",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       41,
			AvgBandwidthIn:       2520000,
			AvgBandwidthOut:      4900000000,
			AvgConnectionTime:    1523.8,
			PacketLossRate:       float32Ptr(0.01),
			AvgConnectionQuality: float32Ptr(0.97),
		},
	}

	edges := make([]*model.ClientMetrics5mEdge, len(records))
	for i, record := range records {
		edges[i] = &model.ClientMetrics5mEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.ClientMetrics5mConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateQualityTierDailyConnection creates demo daily quality tier data
func GenerateQualityTierDailyConnection() *model.QualityTierDailyConnection {
	now := time.Now()

	records := []*pb.QualityTierDaily{
		{
			Id:                "qtd_demo_001",
			Day:               timestamppb.New(now.Truncate(24 * time.Hour)),
			TenantId:          "demo_tenant_frameworks",
			InternalName:      "demo_live_stream_001",
			Tier_1080PMinutes: 245,
			Tier_720PMinutes:  120,
			Tier_480PMinutes:  45,
			TierSdMinutes:     12,
			PrimaryTier:       "1080p",
			CodecH264Minutes:  400,
			CodecH265Minutes:  22,
			AvgBitrate:        2450000,
			AvgFps:            29.8,
		},
		{
			Id:                "qtd_demo_002",
			Day:               timestamppb.New(now.Add(-24 * time.Hour).Truncate(24 * time.Hour)),
			TenantId:          "demo_tenant_frameworks",
			InternalName:      "demo_live_stream_001",
			Tier_1080PMinutes: 312,
			Tier_720PMinutes:  98,
			Tier_480PMinutes:  32,
			TierSdMinutes:     8,
			PrimaryTier:       "1080p",
			CodecH264Minutes:  430,
			CodecH265Minutes:  20,
			AvgBitrate:        2580000,
			AvgFps:            30.0,
		},
		{
			Id:                "qtd_demo_003",
			Day:               timestamppb.New(now.Add(-48 * time.Hour).Truncate(24 * time.Hour)),
			TenantId:          "demo_tenant_frameworks",
			InternalName:      "demo_live_stream_001",
			Tier_1080PMinutes: 189,
			Tier_720PMinutes:  156,
			Tier_480PMinutes:  67,
			TierSdMinutes:     21,
			PrimaryTier:       "1080p",
			CodecH264Minutes:  410,
			CodecH265Minutes:  23,
			AvgBitrate:        2320000,
			AvgFps:            29.5,
		},
	}

	edges := make([]*model.QualityTierDailyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.QualityTierDailyEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.QualityTierDailyConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateQualityChangesHourlyConnection creates demo hourly quality changes
func GenerateQualityChangesHourlyConnection() *model.QualityChangesHourlyConnection {
	now := time.Now()

	records := []*pb.QualityChangesHourly{
		{
			Id:                "qch_demo_001",
			Hour:              timestamppb.New(now.Truncate(time.Hour)),
			TenantId:          "demo_tenant_frameworks",
			InternalName:      "demo_live_stream_001",
			TotalChanges:      15,
			ResolutionChanges: 12,
			CodecChanges:      3,
			QualityTiers:      []string{"1080p", "720p", "1080p"},
			LatestQuality:     "1080p",
			LatestCodec:       "H264",
			LatestResolution:  "1920x1080",
		},
		{
			Id:                "qch_demo_002",
			Hour:              timestamppb.New(now.Add(-1 * time.Hour).Truncate(time.Hour)),
			TenantId:          "demo_tenant_frameworks",
			InternalName:      "demo_live_stream_001",
			TotalChanges:      13,
			ResolutionChanges: 8,
			CodecChanges:      5,
			QualityTiers:      []string{"720p", "1080p"},
			LatestQuality:     "1080p",
			LatestCodec:       "H264",
			LatestResolution:  "1920x1080",
		},
	}

	edges := make([]*model.QualityChangesHourlyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.QualityChangesHourlyEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.QualityChangesHourlyConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateStorageUsageConnection creates demo storage usage records
func GenerateStorageUsageConnection() *model.StorageUsageConnection {
	now := time.Now()

	records := []*pb.StorageUsageRecord{
		{
			Id:             "su_demo_001",
			Timestamp:      timestamppb.New(now.Add(-1 * time.Hour)),
			TenantId:       "demo_tenant_frameworks",
			NodeId:         "node_demo_us_west_01",
			TotalBytes:     45500000000, // 45.5 GB
			FileCount:      156,
			DvrBytes:       25000000000, // 25 GB
			ClipBytes:      8500000000,  // 8.5 GB
			RecordingBytes: 12000000000, // 12 GB
		},
		{
			Id:             "su_demo_002",
			Timestamp:      timestamppb.New(now.Add(-2 * time.Hour)),
			TenantId:       "demo_tenant_frameworks",
			NodeId:         "node_demo_us_west_01",
			TotalBytes:     43700000000,
			FileCount:      152,
			DvrBytes:       24000000000,
			ClipBytes:      8200000000,
			RecordingBytes: 11500000000,
		},
		{
			Id:             "su_demo_003",
			Timestamp:      timestamppb.New(now.Add(-3 * time.Hour)),
			TenantId:       "demo_tenant_frameworks",
			NodeId:         "node_demo_us_west_01",
			TotalBytes:     41800000000,
			FileCount:      148,
			DvrBytes:       23000000000,
			ClipBytes:      7800000000,
			RecordingBytes: 11000000000,
		},
	}

	edges := make([]*model.StorageUsageEdge, len(records))
	for i, record := range records {
		edges[i] = &model.StorageUsageEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.StorageUsageConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateServiceInstancesConnection creates demo service instances for infrastructure
func GenerateServiceInstancesConnection() *model.ServiceInstancesConnection {
	now := time.Now()

	instances := []*pb.ServiceInstance{
		{
			Id:              "svc_demo_001",
			InstanceId:      "mist-instance-001",
			ServiceId:       "service_mist",
			ClusterId:       "cluster_demo_us_west",
			NodeId:          stringPtr("node_demo_us_west_01"),
			Status:          "running",
			HealthStatus:    "healthy",
			Version:         stringPtr("3.4.0"),
			StartedAt:       timestamppb.New(now.Add(-24 * time.Hour)),
			LastHealthCheck: timestamppb.New(now.Add(-30 * time.Second)),
			CreatedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:       timestamppb.New(now.Add(-30 * time.Second)),
		},
		{
			Id:              "svc_demo_002",
			InstanceId:      "caddy-instance-001",
			ServiceId:       "service_caddy",
			ClusterId:       "cluster_demo_us_west",
			NodeId:          stringPtr("node_demo_us_west_01"),
			Status:          "running",
			HealthStatus:    "healthy",
			Version:         stringPtr("2.7.6"),
			StartedAt:       timestamppb.New(now.Add(-24 * time.Hour)),
			LastHealthCheck: timestamppb.New(now.Add(-25 * time.Second)),
			CreatedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:       timestamppb.New(now.Add(-25 * time.Second)),
		},
		{
			Id:              "svc_demo_003",
			InstanceId:      "helmsman-instance-001",
			ServiceId:       "service_helmsman",
			ClusterId:       "cluster_demo_us_west",
			NodeId:          stringPtr("node_demo_us_west_01"),
			Status:          "running",
			HealthStatus:    "healthy",
			Version:         stringPtr("1.0.0"),
			StartedAt:       timestamppb.New(now.Add(-24 * time.Hour)),
			LastHealthCheck: timestamppb.New(now.Add(-20 * time.Second)),
			CreatedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:       timestamppb.New(now.Add(-20 * time.Second)),
		},
	}

	edges := make([]*model.ServiceInstanceEdge, len(instances))
	for i, instance := range instances {
		edges[i] = &model.ServiceInstanceEdge{
			Cursor: instance.Id,
			Node:   instance,
		}
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(instances),
	}
}

// GenerateServiceInstances returns a slice of demo service instances (non-paginated)
func GenerateServiceInstances() []*pb.ServiceInstance {
	conn := GenerateServiceInstancesConnection()
	result := make([]*pb.ServiceInstance, len(conn.Edges))
	for i, edge := range conn.Edges {
		result[i] = edge.Node
	}
	return result
}

// GenerateNodesConnection creates demo nodes for infrastructure
func GenerateNodesConnection() *model.NodesConnection {
	now := time.Now()

	nodes := []*pb.InfrastructureNode{
		{
			Id:         "node_demo_us_west_01",
			NodeId:     "node_demo_us_west_01",
			NodeName:   "US West Primary",
			NodeType:   "edge",
			Status:     "online",
			ClusterId:  "cluster_demo_us_west",
			InternalIp: stringPtr("10.0.1.10"),
			ExternalIp: stringPtr("203.0.113.10"),
			Region:     stringPtr("us-west-2"),
			Latitude:   float64Ptr(37.7749),
			Longitude:  float64Ptr(-122.4194),
			CpuCores:   int32Ptr(8),
			MemoryGb:   int32Ptr(16),
			DiskGb:     int32Ptr(500),
			CreatedAt:  timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:  timestamppb.New(now.Add(-1 * time.Hour)),
		},
		{
			Id:         "node_demo_eu_west_01",
			NodeId:     "node_demo_eu_west_01",
			NodeName:   "EU West Primary",
			NodeType:   "edge",
			Status:     "online",
			ClusterId:  "cluster_demo_eu_west",
			InternalIp: stringPtr("10.0.2.10"),
			ExternalIp: stringPtr("203.0.113.20"),
			Region:     stringPtr("eu-west-1"),
			Latitude:   float64Ptr(51.5074),
			Longitude:  float64Ptr(-0.1278),
			CpuCores:   int32Ptr(8),
			MemoryGb:   int32Ptr(16),
			DiskGb:     int32Ptr(500),
			CreatedAt:  timestamppb.New(now.Add(-25 * 24 * time.Hour)),
			UpdatedAt:  timestamppb.New(now.Add(-2 * time.Hour)),
		},
		{
			Id:         "node_demo_ap_east_01",
			NodeId:     "node_demo_ap_east_01",
			NodeName:   "AP East Primary",
			NodeType:   "edge",
			Status:     "online",
			ClusterId:  "cluster_demo_ap_east",
			InternalIp: stringPtr("10.0.3.10"),
			ExternalIp: stringPtr("203.0.113.30"),
			Region:     stringPtr("ap-northeast-1"),
			Latitude:   float64Ptr(35.6762),
			Longitude:  float64Ptr(139.6503),
			CpuCores:   int32Ptr(4),
			MemoryGb:   int32Ptr(8),
			DiskGb:     int32Ptr(250),
			CreatedAt:  timestamppb.New(now.Add(-20 * 24 * time.Hour)),
			UpdatedAt:  timestamppb.New(now.Add(-30 * time.Minute)),
		},
	}

	edges := make([]*model.NodeEdge, len(nodes))
	for i, node := range nodes {
		edges[i] = &model.NodeEdge{
			Cursor: node.Id,
			Node:   node,
		}
	}

	return &model.NodesConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(nodes),
	}
}

// GenerateClustersConnection creates demo clusters for infrastructure
func GenerateClustersConnection() *model.ClustersConnection {
	now := time.Now()

	clusters := []*pb.InfrastructureCluster{
		{
			Id:                   "cluster_demo_us_west",
			ClusterId:            "cluster_demo_us_west",
			ClusterName:          "US West Demo Cluster",
			ClusterType:          "edge",
			DeploymentModel:      "hybrid",
			BaseUrl:              "https://us-west.demo.frameworks.video",
			MaxConcurrentStreams: 100,
			MaxConcurrentViewers: 10000,
			MaxBandwidthMbps:     10000,
			CurrentStreamCount:   2,
			CurrentViewerCount:   150,
			CurrentBandwidthMbps: 450,
			HealthStatus:         "healthy",
			IsActive:             true,
			CreatedAt:            timestamppb.New(now.Add(-60 * 24 * time.Hour)),
			UpdatedAt:            timestamppb.New(now.Add(-1 * time.Hour)),
		},
		{
			Id:                   "cluster_demo_eu_west",
			ClusterId:            "cluster_demo_eu_west",
			ClusterName:          "EU West Demo Cluster",
			ClusterType:          "edge",
			DeploymentModel:      "hybrid",
			BaseUrl:              "https://eu-west.demo.frameworks.video",
			MaxConcurrentStreams: 50,
			MaxConcurrentViewers: 5000,
			MaxBandwidthMbps:     5000,
			CurrentStreamCount:   1,
			CurrentViewerCount:   75,
			CurrentBandwidthMbps: 180,
			HealthStatus:         "healthy",
			IsActive:             true,
			CreatedAt:            timestamppb.New(now.Add(-45 * 24 * time.Hour)),
			UpdatedAt:            timestamppb.New(now.Add(-2 * time.Hour)),
		},
	}

	edges := make([]*model.ClusterEdge, len(clusters))
	for i, cluster := range clusters {
		edges[i] = &model.ClusterEdge{
			Cursor: cluster.Id,
			Node:   cluster,
		}
	}

	return &model.ClustersConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(clusters),
	}
}

func intPtr(v int) *int             { return &v }
func int32Ptr(v int32) *int32       { return &v }
func int64Ptr(v int64) *int64       { return &v }
func uint32Ptr(v uint32) *uint32    { return &v }
func uint64Ptr(v uint64) *uint64    { return &v }
func float32Ptr(v float32) *float32 { return &v }
func float64Ptr(v float64) *float64 { return &v }
func stringPtr(s string) *string    { return &s }
func boolPtr(v bool) *bool          { return &v }
