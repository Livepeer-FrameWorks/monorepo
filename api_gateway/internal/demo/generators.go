package demo

import (
	"fmt"
	"math"
	"math/rand"
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
			InternalName:   "demo_live_stream_001",
			Title:          "Live: FrameWorks Demo Stream",
			Description:    "Demonstrating live streaming capabilities",
			StreamKey:      "sk_demo_live_a1b2c3d4e5f6",
			PlaybackId:     "pb_demo_live_x7y8z9",
			IsLive:         true,
			Status:         "live",
			IsRecording:    true,
			CurrentViewers: 47,
			PeakViewers:    89,
			TotalViews:     1247,
			Duration:       7200, // 2 hours in seconds
			StartedAt:      timestamppb.New(now.Add(-2 * time.Hour)),
			CreatedAt:      timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:      timestamppb.New(now.Add(-5 * time.Minute)),
		},
		{
			InternalName:   "demo_offline_stream_002",
			Title:          "Gaming Stream Setup",
			Description:    "Getting ready for tonight's gaming session",
			StreamKey:      "sk_demo_offline_f6g7h8i9j0k1",
			PlaybackId:     "pb_demo_offline_m3n4o5",
			IsLive:         false,
			Status:         "offline",
			IsRecording:    false,
			CurrentViewers: 0,
			PeakViewers:    0,
			TotalViews:     0,
			Duration:       0,
			CreatedAt:      timestamppb.New(now.Add(-24 * time.Hour)),
			UpdatedAt:      timestamppb.New(now.Add(-1 * time.Hour)),
		},
		{
			InternalName:   "demo_recording_stream_003",
			Title:          "Product Demo Recording",
			Description:    "Recording a product demo for the marketing team",
			StreamKey:      "sk_demo_rec_l2m3n4o5p6q7",
			PlaybackId:     "pb_demo_rec_r8s9t0",
			IsLive:         true,
			Status:         "recording",
			IsRecording:    true,
			CurrentViewers: 3,
			PeakViewers:    5,
			TotalViews:     12,
			Duration:       3600, // 1 hour in seconds
			StartedAt:      timestamppb.New(now.Add(-1 * time.Hour)),
			CreatedAt:      timestamppb.New(now.Add(-6 * time.Hour)),
			UpdatedAt:      timestamppb.New(now.Add(-10 * time.Minute)),
		},
		{
			InternalName:   "demo_ended_stream_004",
			Title:          "Weekly Team Standup",
			Description:    "Our weekly development team standup meeting",
			StreamKey:      "sk_demo_ended_u1v2w3x4y5z6",
			PlaybackId:     "pb_demo_ended_a7b8c9",
			IsLive:         false,
			Status:         "ended",
			IsRecording:    false,
			CurrentViewers: 0,
			PeakViewers:    28,
			TotalViews:     45,
			Duration:       1800, // 30 minutes in seconds
			StartedAt:      timestamppb.New(now.Add(-48 * time.Hour)),
			EndedAt:        timestamppb.New(now.Add(-47*time.Hour - 30*time.Minute)),
			CreatedAt:      timestamppb.New(now.Add(-48 * time.Hour)),
			UpdatedAt:      timestamppb.New(now.Add(-36 * time.Hour)),
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
		TenantId:             "00000000-0000-0000-0000-000000000001",
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
		CurrentBufferState:   stringPtr("FULL"),
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
		PacketLossRate:       0.0005,
		TotalViews:           1247, // Total view count (session starts)
		UniqueViewers:        342,  // Unique viewer count (distinct sessions)
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
			TenantId:          "00000000-0000-0000-0000-000000000001",
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

	// Build usage details for first invoice (within allocation, metered overage)
	usageDetails1, _ := structpb.NewStruct(map[string]interface{}{
		"usage_data": map[string]interface{}{
			"viewer_hours":       708.33, // 42,500 delivered minutes / 60
			"average_storage_gb": 15.2,
			"gpu_hours":          5.0,
			"stream_hours":       42.5,
			"egress_gb":          125.0,
			// Per-codec processing (matches ClickHouse columns)
			"livepeer_h264_seconds":  2400.0,
			"livepeer_vp9_seconds":   500.0,
			"livepeer_av1_seconds":   245.7,
			"livepeer_hevc_seconds":  100.0,
			"native_av_h264_seconds": 1200.0,
			"native_av_vp9_seconds":  300.0,
			"native_av_av1_seconds":  120.5,
			"native_av_hevc_seconds": 0.0,
			"native_av_aac_seconds":  150.0, // Audio is FREE
			"native_av_opus_seconds": 50.0,  // Audio is FREE
			"audio_seconds":          200.0,
			"video_seconds":          1620.5,
		},
		"tier_info": map[string]interface{}{
			"tier_name":        "professional",
			"display_name":     "Professional",
			"base_price":       19.99,
			"metering_enabled": true,
		},
	})

	// Build usage details for second invoice
	usageDetails2, _ := structpb.NewStruct(map[string]interface{}{
		"usage_data": map[string]interface{}{
			"viewer_hours":       583.33, // 35,000 delivered minutes / 60
			"average_storage_gb": 19.0,
			"gpu_hours":          3.5,
			"stream_hours":       35.0,
			"egress_gb":          98.0,
			// Per-codec processing
			"livepeer_h264_seconds":  1800.0,
			"livepeer_vp9_seconds":   350.0,
			"livepeer_av1_seconds":   180.0,
			"livepeer_hevc_seconds":  75.0,
			"native_av_h264_seconds": 900.0,
			"native_av_vp9_seconds":  200.0,
			"native_av_av1_seconds":  85.0,
			"native_av_hevc_seconds": 0.0,
			"native_av_aac_seconds":  120.0,
			"native_av_opus_seconds": 40.0,
			"audio_seconds":          160.0,
			"video_seconds":          1185.0,
		},
		"tier_info": map[string]interface{}{
			"tier_name":        "professional",
			"display_name":     "Professional",
			"base_price":       19.99,
			"metering_enabled": true,
		},
	})

	return []*pb.Invoice{
		{
			Id:            "inv_demo_current_001",
			TenantId:      "00000000-0000-0000-0000-000000000001",
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
			TenantId:      "00000000-0000-0000-0000-000000000001",
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

	// Demo custom allocation - 100,000 viewer-minutes included
	customLimit := float64(100000)

	return &pb.BillingStatusResponse{
		TenantId: "00000000-0000-0000-0000-000000000001",
		Subscription: &pb.TenantSubscription{
			Id:              "sub_demo_123",
			TenantId:        "00000000-0000-0000-0000-000000000001",
			TierId:          "tier_professional",
			Status:          "active",
			BillingEmail:    "demo@frameworks.example",
			StartedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			NextBillingDate: timestamppb.New(nextBilling),
			CreatedAt:       timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:       timestamppb.Now(),
			// Demo custom terms for enterprise-style subscription
			CustomPricing: &pb.CustomPricing{
				BasePrice:    79.00, // Custom negotiated base price
				DiscountRate: 0.20,  // 20% discount
			},
			CustomAllocations: &pb.AllocationDetails{
				Limit:     &customLimit, // 100k viewer-minutes included
				UnitPrice: 0.0005,       // $0.0005 per minute overage
				Unit:      "viewer-minutes",
			},
			CustomFeatures: &pb.BillingFeatures{
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				ApiAccess:      true,
				SupportLevel:   "priority",
				Sla:            true,
			},
		},
		Tier: &pb.BillingTier{
			Id:            "tier_professional",
			TierName:      "professional",
			DisplayName:   "Professional",
			Description:   "For growing businesses with advanced streaming needs",
			BasePrice:     99.00,
			Currency:      "USD",
			BillingPeriod: "month",
			BandwidthAllocation: &pb.AllocationDetails{
				Limit:     float64Ptr(1000), // 1TB included
				UnitPrice: 0.08,
				Unit:      "GB",
			},
			StorageAllocation: &pb.AllocationDetails{
				Limit:     float64Ptr(100), // 100GB included
				UnitPrice: 0.10,
				Unit:      "GB",
			},
			ComputeAllocation: &pb.AllocationDetails{
				Limit:     float64Ptr(500), // 500 compute hours included
				UnitPrice: 0.05,
				Unit:      "hours",
			},
			Features: &pb.BillingFeatures{
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				ApiAccess:      true,
				SupportLevel:   "priority",
				Sla:            true,
			},
			SupportLevel:    "priority",
			SlaLevel:        "99.9%",
			MeteringEnabled: true,
			OverageRates: &pb.OverageRates{
				Bandwidth: &pb.AllocationDetails{
					UnitPrice: 0.10,
					Unit:      "GB",
				},
				Storage: &pb.AllocationDetails{
					UnitPrice: 0.12,
					Unit:      "GB",
				},
				Compute: &pb.AllocationDetails{
					UnitPrice: 0.06,
					Unit:      "hours",
				},
			},
			IsActive:     true,
			SortOrder:    2, // Professional tier in the middle
			IsEnterprise: false,
			CreatedAt:    timestamppb.New(now.Add(-90 * 24 * time.Hour)),
			UpdatedAt:    timestamppb.New(now.Add(-7 * 24 * time.Hour)),
		},
		BillingStatus:     "active",
		NextBillingDate:   timestamppb.New(nextBilling),
		OutstandingAmount: 0.00,
		Currency:          "USD",
		PendingInvoices:   []*pb.Invoice{}, // Empty slice
		RecentPayments:    []*pb.Payment{}, // Empty slice
		UsageSummary: &pb.UsageSummary{
			BillingMonth:          now.Format("2006-01"),
			Period:                "1d",
			Timestamp:             timestamppb.Now(),
			StreamHours:           42.5,
			EgressGb:              25.4,
			RecordingGb:           8.2,
			PeakBandwidthMbps:     156.8,
			// Processing/transcoding usage (totals)
			LivepeerSeconds:       3245.7, // ~54 minutes of Livepeer transcode
			LivepeerSegmentCount:  542,
			LivepeerUniqueStreams: 3,
			NativeAvSeconds:       1820.5, // ~30 minutes of local AV processing
			NativeAvSegmentCount:  364,
			NativeAvUniqueStreams: 5,
			// Per-codec breakdown: Livepeer (external gateway)
			LivepeerH264Seconds: 2400.0, // H264 is most common
			LivepeerVp9Seconds:  500.0,
			LivepeerAv1Seconds:  245.7,
			LivepeerHevcSeconds: 100.0,
			// Per-codec breakdown: Native AV (local processing)
			NativeAvH264Seconds: 1200.0,
			NativeAvVp9Seconds:  300.0,
			NativeAvAv1Seconds:  120.5,
			NativeAvHevcSeconds: 0.0,
			NativeAvAacSeconds:  150.0, // Audio is FREE but tracked
			NativeAvOpusSeconds: 50.0,  // Audio is FREE but tracked
			// Track type aggregates
			AudioSeconds: 200.0,  // Total audio (aac + opus)
			VideoSeconds: 1620.5, // Total video (h264+vp9+av1+hevc)
			StorageGb:             12.5,
			AverageStorageGb:      11.8,
			// Clip storage lifecycle
			ClipsAdded:           3,
			ClipsDeleted:         1,
			ClipStorageAddedGb:   0.5,
			ClipStorageDeletedGb: 0.2,
			// DVR storage lifecycle
			DvrAdded:             2,
			DvrDeleted:           0,
			DvrStorageAddedGb:    1.2,
			DvrStorageDeletedGb:  0.0,
			// VOD storage lifecycle
			VodAdded:             1,
			VodDeleted:           0,
			VodStorageAddedGb:    2.5,
			VodStorageDeletedGb:  0.0,
			// Viewer metrics
			TotalStreams:         8,
			TotalViewers:         1847,
			ViewerHours:          156.3,
			PeakViewers:          342,
			MaxViewers:           342,
			UniqueUsers:          1203,
			AvgViewers:           145.5,
			UniqueCountries:      12,
			UniqueCities:         58,
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
// Usage types must match what frontend expects and what Purser stores
func GenerateUsageRecords() []*pb.UsageRecord {
	now := time.Now()

	// Define usage type data: type name, value, unit for details
	usageData := []struct {
		usageType string
		value     float64
		unit      string
	}{
		// Core streaming metrics
		{"stream_hours", 25, "hours"},
		{"egress_gb", 1628, "GB"},
		{"recording_gb", 37.25, "GB"},
		{"peak_bandwidth_mbps", 850.5, "Mbps"},
		{"total_streams", 1, "streams"},
		{"peak_viewers", 342, "viewers"},
		// Per-codec processing: Livepeer (external gateway)
		{"livepeer_h264_seconds", 2400.0, "seconds"},
		{"livepeer_vp9_seconds", 500.0, "seconds"},
		{"livepeer_av1_seconds", 245.7, "seconds"},
		{"livepeer_hevc_seconds", 100.0, "seconds"},
		// Per-codec processing: Native AV (local)
		{"native_av_h264_seconds", 1200.0, "seconds"},
		{"native_av_vp9_seconds", 300.0, "seconds"},
		{"native_av_av1_seconds", 120.5, "seconds"},
		{"native_av_hevc_seconds", 0.0, "seconds"},
		{"native_av_aac_seconds", 150.0, "seconds"},  // Audio is FREE
		{"native_av_opus_seconds", 50.0, "seconds"},  // Audio is FREE
		// Track type aggregates
		{"audio_seconds", 200.0, "seconds"},
		{"video_seconds", 1620.5, "seconds"},
	}

	records := make([]*pb.UsageRecord, len(usageData))

	for i, data := range usageData {
		// Build usage details as structpb.Struct
		usageDetails, _ := structpb.NewStruct(map[string]interface{}{
			"cost": map[string]interface{}{
				"quantity":   data.value,
				"unit_price": 0.5,
				"unit":       data.unit,
			},
		})

		records[i] = &pb.UsageRecord{
			Id:           "usage_demo_" + now.Format("20060102") + "_" + data.usageType,
			TenantId:     "00000000-0000-0000-0000-000000000001",
			ClusterId:    "cluster_demo_us_west",
			ClusterName:  stringPtr("US West Demo Cluster"),
			UsageType:    data.usageType,
			UsageValue:   data.value,
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
		Id:                    "00000000-0000-0000-0000-000000000001",
		Name:                  "FrameWorks Demo Organization",
		Subdomain:             stringPtr("demo"),
		PrimaryColor:          "#3B82F6", // Blue
		SecondaryColor:        "#1F2937", // Dark gray
		DeploymentTier:        "edge",
		DeploymentModel:       "multi-region",
		PrimaryDeploymentTier: "edge",
		PrimaryClusterId:      stringPtr("cluster_demo_us_west"),
		IsActive:              true,
		CreatedAt:             timestamppb.New(now.Add(-180 * 24 * time.Hour)),
		UpdatedAt:             timestamppb.New(now.Add(-1 * 24 * time.Hour)),
	}
}

// GeneratePlatformOverview creates demo platform metrics
func GeneratePlatformOverview() *pb.GetPlatformOverviewResponse {
	now := time.Now()

	return &pb.GetPlatformOverviewResponse{
		TenantId:      "00000000-0000-0000-0000-000000000001",
		TotalStreams:  42,
		ActiveStreams: 7,
		TotalViewers:          1247,
		AverageViewers:        54.2,
		PeakBandwidth:         850.5,
		GeneratedAt:           timestamppb.New(now),
		StreamHours:           284.5,         // ~12 days of streaming
		EgressGb:              1247.8,        // ~1.2 TB egress
		PeakViewers:           342,           // Unique viewers (legacy field)
		TotalUploadBytes:      52428800000,   // ~50 GB uploaded
		TotalDownloadBytes:    1340000000000, // ~1.2 TB downloaded
		ViewerHours:           4892.5,        // Total accumulated watch time
		DeliveredMinutes:      293550,        // viewerHours * 60
		UniqueViewers:         342,           // Distinct viewer sessions
		IngestHours:           284.5,         // Same as StreamHours (alias)
		PeakConcurrentViewers: 89,            // Max concurrent viewers at any instant
		TotalViews:            8734,          // Total view sessions started
	}
}

// GenerateStreamSubscriptionEvents creates demo stream subscription events
// Returns model.StreamSubscriptionEvent with direct proto payloads
func GenerateStreamSubscriptionEvents() []*model.StreamSubscriptionEvent {
	now := time.Now()
	return []*model.StreamSubscriptionEvent{
		{
			EventType: "EVENT_TYPE_STREAM_LIFECYCLE_UPDATE",
			Timestamp: now,
			LifecycleUpdate: &pb.StreamLifecycleUpdate{
				NodeId:          "node_demo_us_west_01",
				InternalName:    "demo_live_stream_001",
				Status:          "live",
				BufferState:     stringPtr("FULL"),
				UploadedBytes:   uint64Ptr(125000000),
				DownloadedBytes: uint64Ptr(450000000),
				TotalViewers:    uint32Ptr(47),
				TotalInputs:     uint32Ptr(1),
				ViewerSeconds:   uint64Ptr(2820),
			},
		},
		{
			EventType: "EVENT_TYPE_STREAM_LIFECYCLE_UPDATE",
			Timestamp: now.Add(30 * time.Second),
			LifecycleUpdate: &pb.StreamLifecycleUpdate{
				NodeId:          "node_demo_us_west_01",
				InternalName:    "demo_live_stream_001",
				Status:          "live",
				BufferState:     stringPtr("FULL"),
				UploadedBytes:   uint64Ptr(187500000),
				DownloadedBytes: uint64Ptr(675000000),
				TotalViewers:    uint32Ptr(52),
				TotalInputs:     uint32Ptr(1),
				ViewerSeconds:   uint64Ptr(4230),
			},
		},
	}
}

// GenerateStreamEvents creates demo stream events for the old StreamEvent type (used by connections/pagination)
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
	metrics := make([]*pb.StreamHealthMetric, 0, 50)

	// Base values
	videoWidth := int32(1920)
	videoHeight := int32(1080)
	videoResolution := "1920x1080"
	audioChannels := int32(2)
	audioSampleRate := int32(48000)
	audioBitrate := int32(128)
	qualityTierFull := "1080p30"

	// Generate 50 points over last 4 hours (approx every 5 mins)
	for i := 49; i >= 0; i-- {
		ts := now.Add(-time.Duration(i*5) * time.Minute)
		
		// Simulate some variation
		// Sine wave for bitrate: 2.5Mbps +/- 0.5Mbps
		variation := math.Sin(float64(i) * 0.2)
		bitrateVal := 2500 + int32(variation*500)
		
		// Occasional drops (simulated network issues)
		bufferHealth := 0.98
		bufferState := "FULL"
		packetLoss := 0.001
		fps := 30.0
		
		if i%15 == 0 { // Every 15th point is a "bad" moment
			bitrateVal = 1200
			bufferHealth = 0.45
			bufferState = "RECOVER"
			packetLoss = 0.05
			fps = 24.5
		} else if i%7 == 0 { // Every 7th point is "warning"
			bufferHealth = 0.75
			bufferState = "DRY"
			fps = 28.0
		}

		videoBitrate := bitrateVal
		
		tracks := []*pb.StreamTrack{
			{
				TrackName:   "video_main",
				TrackType:   "video",
				Codec:       "H264",
				BitrateKbps: &videoBitrate,
				Width:       &videoWidth,
				Height:      &videoHeight,
				Fps:         float64Ptr(fps),
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

		metric := &pb.StreamHealthMetric{
			Timestamp:              timestamppb.New(ts),
			InternalName:           "demo_live_stream_001",
			TenantId:               "00000000-0000-0000-0000-000000000001",
			NodeId:                 "node_demo_us_west_01",
			Bitrate:                bitrateVal, // kbps (consistent with ClickHouse schema)
			Fps:                    float32(fps),
			GopSize:                60,
			Width:                  1920,
			Height:                 1080,
			BufferHealth:           float32(bufferHealth),
			BufferState:            bufferState,
			PacketsSent:            15000,
			PacketsLost:            int64(15000 * packetLoss),
			Codec:                  "H264",
			Tracks:                 tracks,
			PrimaryAudioChannels:   &audioChannels,
			PrimaryAudioSampleRate: &audioSampleRate,
			PrimaryAudioCodec:      stringPtr("AAC"),
			PrimaryAudioBitrate:    &audioBitrate,
			PacketLossPercentage:   float64Ptr(packetLoss),
			QualityTier:            &qualityTierFull,
		}
		
		if bufferState != "FULL" {
			metric.IssuesDescription = stringPtr("temporary network congestion")
		}

		metrics = append(metrics, metric)
	}

	return metrics
}

// GenerateRebufferingEvents creates demo rebuffering events
func GenerateRebufferingEvents() []*pb.RebufferingEvent {
	now := time.Now()
	return []*pb.RebufferingEvent{
		{
			Id:            "rebuf_demo_001",
			Timestamp:     timestamppb.New(now.Add(-8 * time.Minute)),
			InternalName:  "demo_live_stream_001",
			NodeId:        "node_demo_us_west_01",
			BufferState:   "DRY",
			PrevState:     "FULL",
			RebufferStart: timestamppb.New(now.Add(-8 * time.Minute)),
			RebufferEnd:   timestamppb.New(now.Add(-6 * time.Minute)),
		},
		{
			Id:            "rebuf_demo_002",
			Timestamp:     timestamppb.New(now.Add(-6 * time.Minute)),
			InternalName:  "demo_live_stream_001",
			NodeId:        "node_demo_us_west_01",
			BufferState:   "RECOVERING",
			PrevState:     "DRY",
			RebufferStart: timestamppb.New(now.Add(-8 * time.Minute)),
			RebufferEnd:   timestamppb.New(now.Add(-6 * time.Minute)),
		},
	}
}

// GenerateViewerTimeSeries creates demo viewer count time series data for charts
func GenerateViewerTimeSeries() []*pb.ViewerCountBucket {
	now := time.Now()
	// Generate 288 data points (24 hours * 12 per hour) at 5-minute intervals
	totalPoints := 288
	buckets := make([]*pb.ViewerCountBucket, 0, totalPoints)

	baseViewers := 150.0
	
	for i := totalPoints - 1; i >= 0; i-- {
		ts := now.Add(-time.Duration(i*5) * time.Minute)
		
		// Create a daily curve using sine wave (peaking around evening)
		// i goes from 287 down to 0 (representing -24h to now)
		// Map i to 0-2PI for sine wave
		
		hourOffset := float64(i) / 12.0 // Hours ago
		
		// Daily cycle: peak at -4h (evening), low at -16h (early morning)
		dailyCycle := math.Sin((hourOffset - 4.0) * math.Pi / 12.0) 
		
		// Add some noise
		noise := (float64(i%7) / 10.0) - 0.3
		
		viewers := baseViewers + (dailyCycle * 80.0) + (noise * 20.0)
		
		// Ensure non-negative
		if viewers < 5 {
			viewers = 5
		}
		
		buckets = append(buckets, &pb.ViewerCountBucket{
			Timestamp:    timestamppb.New(ts),
			ViewerCount:  int32(viewers),
			InternalName: "demo_live_stream_001",
		})
	}

	return buckets
}

// GenerateViewerGeographics creates realistic demo viewer geographic data
func GenerateViewerGeographics() []*pb.ConnectionEvent {
	now := time.Now()

	return []*pb.ConnectionEvent{
		// Connect event - no duration/bytes yet
		{
			EventId:        "evt_demo_1",
			Timestamp:      timestamppb.New(now.Add(-30 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
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
			TenantId:               "00000000-0000-0000-0000-000000000001",
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
			SessionDurationSeconds: 1500,      // 25 minutes
			BytesTransferred:       256000000, // ~256 MB
		},
		// Connect event
		{
			EventId:        "evt_demo_2",
			Timestamp:      timestamppb.New(now.Add(-25 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
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
			TenantId:               "00000000-0000-0000-0000-000000000001",
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
			SessionDurationSeconds: 1380,      // 23 minutes
			BytesTransferred:       189000000, // ~189 MB
		},
		// Connect event - still connected
		{
			EventId:        "evt_demo_3",
			Timestamp:      timestamppb.New(now.Add(-20 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
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
			TenantId:               "00000000-0000-0000-0000-000000000001",
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
			SessionDurationSeconds: 45,      // 45 seconds (short session)
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

// GenerateRoutingEvents creates realistic demo routing event data
func GenerateRoutingEvents() []*pb.RoutingEvent {
	now := time.Now()

	str := func(s string) *string { return &s }
	f64 := func(f float64) *float64 { return &f }
	i32 := func(i int32) *int32 { return &i }

	// H3 index examples at resolution 5 (~25km hexagons)
	// These are valid H3 indexes for the given lat/lng coordinates
	sfClientBucket := &pb.GeoBucket{H3Index: 0x85283473fffffff, Resolution: 5}     // San Francisco area
	sfNodeBucket := &pb.GeoBucket{H3Index: 0x85283477fffffff, Resolution: 5}       // Palo Alto area
	londonClientBucket := &pb.GeoBucket{H3Index: 0x85194ad7fffffff, Resolution: 5} // London area
	londonNodeBucket := &pb.GeoBucket{H3Index: 0x85194ad3fffffff, Resolution: 5}   // London node
	tokyoClientBucket := &pb.GeoBucket{H3Index: 0x8529a927fffffff, Resolution: 5}  // Tokyo area
	tokyoNodeBucket := &pb.GeoBucket{H3Index: 0x8529a923fffffff, Resolution: 5}    // Tokyo node
	nyClientBucket := &pb.GeoBucket{H3Index: 0x85282607fffffff, Resolution: 5}     // New York area

	// Demo cluster and tenant IDs for dual-tenant attribution
	demoClusterID := str("central-primary")
	demoStreamTenantID := str("00000000-0000-0000-0000-000000000001")

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
			StreamTenantId:  demoStreamTenantID,
			ClusterId:       demoClusterID,
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
			StreamTenantId:  demoStreamTenantID,
			ClusterId:       demoClusterID,
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
		Status:        "live",
		IsLive:        true,
		Viewers:       132,
		BufferState:   "FULL",
		TenantId:      "00000000-0000-0000-0000-000000000001",
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

// GenerateIngestEndpointResponse creates demo ingest endpoint data for StreamCrafter
func GenerateIngestEndpointResponse(streamKey string) *pb.IngestEndpointResponse {
	if streamKey == "" {
		streamKey = "demo_stream_key_001"
	}

	// Demo internal stream name (derived from stream key in real impl)
	internalName := "demo_live_stream_001"

	// Primary WHIP ingest URL
	whipURL := "https://ingest.demo.frameworks.video/webrtc/" + internalName
	rtmpURL := "rtmp://ingest.demo.frameworks.video:1935/live/" + internalName
	srtURL := "srt://ingest.demo.frameworks.video:9000?streamid=" + internalName
	region := "US West"
	loadScore := 0.25

	primary := &pb.IngestEndpoint{
		NodeId:    "node_demo_us_west_01",
		BaseUrl:   "https://ingest.demo.frameworks.video",
		WhipUrl:   &whipURL,
		RtmpUrl:   &rtmpURL,
		SrtUrl:    &srtURL,
		Region:    &region,
		LoadScore: &loadScore,
	}

	// Fallback endpoint
	fallbackWhipURL := "https://ingest.eu.demo.frameworks.video/webrtc/" + internalName
	fallbackRtmpURL := "rtmp://ingest.eu.demo.frameworks.video:1935/live/" + internalName
	fallbackSrtURL := "srt://ingest.eu.demo.frameworks.video:9000?streamid=" + internalName
	fallbackRegion := "EU West"
	fallbackLoadScore := 0.42

	fallback := &pb.IngestEndpoint{
		NodeId:    "node_demo_eu_west_01",
		BaseUrl:   "https://ingest.eu.demo.frameworks.video",
		WhipUrl:   &fallbackWhipURL,
		RtmpUrl:   &fallbackRtmpURL,
		SrtUrl:    &fallbackSrtURL,
		Region:    &fallbackRegion,
		LoadScore: &fallbackLoadScore,
	}

	metadata := &pb.IngestMetadata{
		StreamId:         internalName,
		StreamKey:        streamKey,
		TenantId:         "00000000-0000-0000-0000-000000000001",
		RecordingEnabled: true,
	}

	return &pb.IngestEndpointResponse{
		Primary:   primary,
		Fallbacks: []*pb.IngestEndpoint{fallback},
		Metadata:  metadata,
	}
}

// ============================================================================
// Connection-style Demo Generators (for Relay pagination)
// ============================================================================

// GenerateRoutingEventsConnection creates demo routing events with pagination
func GenerateRoutingEventsConnection() *model.RoutingEventsConnection {
	events := GenerateRoutingEvents()
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
			Id:                 "nm_demo_001",
			Timestamp:          timestamppb.New(now.Add(-5 * time.Minute)),
			NodeId:             "node_demo_us_west_01",
			CpuUsage:           65.2,
			RamMax:             16000000000,
			RamCurrent:         12500000000,
			BandwidthIn:        125000000, // cumulative bytes
			BandwidthOut:       250000000, // cumulative bytes
			UpSpeed:            15000000,  // 15 MB/s (bytes/sec)
			DownSpeed:          30000000,  // 30 MB/s (bytes/sec)
			ConnectionsCurrent: 42,        // current viewer connections
			IsHealthy:          true,
			Latitude:           37.7749,
			Longitude:          -122.4194,
		},
		{
			Id:                 "nm_demo_002",
			Timestamp:          timestamppb.New(now.Add(-10 * time.Minute)),
			NodeId:             "node_demo_us_west_01",
			CpuUsage:           58.7,
			RamMax:             16000000000,
			RamCurrent:         11800000000,
			BandwidthIn:        118000000, // cumulative bytes
			BandwidthOut:       235000000, // cumulative bytes
			UpSpeed:            12000000,  // 12 MB/s (bytes/sec)
			DownSpeed:          25000000,  // 25 MB/s (bytes/sec)
			ConnectionsCurrent: 38,        // current viewer connections
			IsHealthy:          true,
			Latitude:           37.7749,
			Longitude:          -122.4194,
		},
		{
			Id:                 "nm_demo_003",
			Timestamp:          timestamppb.New(now.Add(-5 * time.Minute)),
			NodeId:             "node_demo_eu_west_01",
			CpuUsage:           72.1,
			RamMax:             16000000000,
			RamCurrent:         13200000000,
			BandwidthIn:        100000000, // cumulative bytes
			BandwidthOut:       200000000, // cumulative bytes
			UpSpeed:            10000000,  // 10 MB/s (bytes/sec)
			DownSpeed:          20000000,  // 20 MB/s (bytes/sec)
			ConnectionsCurrent: 55,        // current viewer connections
			IsHealthy:          true,
			Latitude:           51.5074,
			Longitude:          -0.1278,
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
		TenantId:        "00000000-0000-0000-0000-000000000001",
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
			TenantId:        "00000000-0000-0000-0000-000000000001",
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
			TenantId:        "00000000-0000-0000-0000-000000000001",
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
			TenantId:        "00000000-0000-0000-0000-000000000001",
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
			TenantId:      "00000000-0000-0000-0000-000000000001",
			InternalName:  "demo_live_stream_001",
			TotalBytes:    45000000000,
			UniqueViewers: 189,
			TotalSessions: 245,
		},
		{
			Id:            "sch_demo_002",
			Hour:          timestamppb.New(now.Add(-1 * time.Hour).Truncate(time.Hour)),
			TenantId:      "00000000-0000-0000-0000-000000000001",
			InternalName:  "demo_live_stream_001",
			TotalBytes:    58000000000,
			UniqueViewers: 234,
			TotalSessions: 312,
		},
		{
			Id:            "sch_demo_003",
			Hour:          timestamppb.New(now.Add(-2 * time.Hour).Truncate(time.Hour)),
			TenantId:      "00000000-0000-0000-0000-000000000001",
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
			TenantId:             "00000000-0000-0000-0000-000000000001",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       45,
			AvgBandwidthIn:       2450000,
			AvgBandwidthOut:      5400000000,
			AvgConnectionTime:    1847.5,
			PacketLossRate:       float32Ptr(0.02),
		},
		{
			Id:                   "cm5_demo_002",
			Timestamp:            timestamppb.New(now.Add(-5 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:             "00000000-0000-0000-0000-000000000001",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       52,
			AvgBandwidthIn:       2380000,
			AvgBandwidthOut:      6200000000,
			AvgConnectionTime:    2156.3,
			PacketLossRate:       float32Ptr(0.03),
		},
		{
			Id:                   "cm5_demo_003",
			Timestamp:            timestamppb.New(now.Add(-10 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:             "00000000-0000-0000-0000-000000000001",
			InternalName:         "demo_live_stream_001",
			NodeId:               "node_demo_us_west_01",
			ActiveSessions:       41,
			AvgBandwidthIn:       2520000,
			AvgBandwidthOut:      4900000000,
			AvgConnectionTime:    1523.8,
			PacketLossRate:       float32Ptr(0.01),
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
			TenantId:          "00000000-0000-0000-0000-000000000001",
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
			TenantId:          "00000000-0000-0000-0000-000000000001",
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
			TenantId:          "00000000-0000-0000-0000-000000000001",
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

// GenerateStorageUsageConnection creates demo storage usage records
func GenerateStorageUsageConnection() *model.StorageUsageConnection {
	now := time.Now()

	records := []*pb.StorageUsageRecord{
		{
			Id:              "su_demo_001",
			Timestamp:       timestamppb.New(now.Add(-1 * time.Hour)),
			TenantId:        "00000000-0000-0000-0000-000000000001",
			NodeId:          "node_demo_us_west_01",
			TotalBytes:      45500000000, // 45.5 GB
			FileCount:       156,
			DvrBytes:        25000000000, // 25 GB
			ClipBytes:       8500000000,  // 8.5 GB
			VodBytes:        12000000000, // 12 GB
			FrozenDvrBytes:  5000000000,  // 5 GB in S3
			FrozenClipBytes: 2000000000,  // 2 GB in S3
			FrozenVodBytes:  3000000000,  // 3 GB in S3
		},
		{
			Id:              "su_demo_002",
			Timestamp:       timestamppb.New(now.Add(-2 * time.Hour)),
			TenantId:        "00000000-0000-0000-0000-000000000001",
			NodeId:          "node_demo_us_west_01",
			TotalBytes:      43700000000,
			FileCount:       152,
			DvrBytes:        24000000000,
			ClipBytes:       8200000000,
			VodBytes:        11500000000,
			FrozenDvrBytes:  4800000000,
			FrozenClipBytes: 1900000000,
			FrozenVodBytes:  2800000000,
		},
		{
			Id:              "su_demo_003",
			Timestamp:       timestamppb.New(now.Add(-3 * time.Hour)),
			TenantId:        "00000000-0000-0000-0000-000000000001",
			NodeId:          "node_demo_us_west_01",
			TotalBytes:      41800000000,
			FileCount:       148,
			DvrBytes:        23000000000,
			ClipBytes:       7800000000,
			VodBytes:        11000000000,
			FrozenDvrBytes:  4600000000,
			FrozenClipBytes: 1800000000,
			FrozenVodBytes:  2600000000,
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

// GenerateStorageEventsConnection creates demo storage lifecycle events
func GenerateStorageEventsConnection(internalName *string) *model.StorageEventsConnection {
	now := time.Now()

	// Filter by stream name if provided
	streamFilter := "demo_live_stream_001"
	if internalName != nil && *internalName != "" {
		streamFilter = *internalName
	}

	events := []*pb.StorageEvent{
		{
			Id:           "se_demo_001",
			Timestamp:    timestamppb.New(now.Add(-30 * time.Minute)),
			TenantId:     "00000000-0000-0000-0000-000000000001",
			InternalName: streamFilter,
			AssetHash:    "clip_hash_demo_001",
			Action:       "frozen",
			AssetType:    "clip",
			SizeBytes:    15000000, // 15 MB
			S3Url:        stringPtr("s3://demo-bucket/clips/clip_hash_demo_001.mp4"),
			LocalPath:    stringPtr("/mnt/storage/clips/clip_hash_demo_001.mp4"),
			NodeId:       "node_demo_us_west_01",
			DurationMs:   int64Ptr(2450), // 2.45 seconds
		},
		{
			Id:           "se_demo_002",
			Timestamp:    timestamppb.New(now.Add(-32 * time.Minute)),
			TenantId:     "00000000-0000-0000-0000-000000000001",
			InternalName: streamFilter,
			AssetHash:    "clip_hash_demo_001",
			Action:       "freeze_started",
			AssetType:    "clip",
			SizeBytes:    15000000,
			LocalPath:    stringPtr("/mnt/storage/clips/clip_hash_demo_001.mp4"),
			NodeId:       "node_demo_us_west_01",
		},
		{
			Id:           "se_demo_003",
			Timestamp:    timestamppb.New(now.Add(-1 * time.Hour)),
			TenantId:     "00000000-0000-0000-0000-000000000001",
			InternalName: streamFilter,
			AssetHash:    "dvr_hash_demo_001",
			Action:       "defrosted",
			AssetType:    "dvr",
			SizeBytes:    250000000, // 250 MB
			S3Url:        stringPtr("s3://demo-bucket/dvr/dvr_hash_demo_001.ts"),
			LocalPath:    stringPtr("/mnt/storage/dvr/dvr_hash_demo_001.ts"),
			NodeId:       "node_demo_us_west_01",
			DurationMs:   int64Ptr(8750),  // 8.75 seconds
			WarmDurationMs: int64Ptr(350), // 0.35 seconds
		},
		{
			Id:           "se_demo_004",
			Timestamp:    timestamppb.New(now.Add(-1*time.Hour - 5*time.Minute)),
			TenantId:     "00000000-0000-0000-0000-000000000001",
			InternalName: streamFilter,
			AssetHash:    "dvr_hash_demo_001",
			Action:       "defrost_started",
			AssetType:    "dvr",
			SizeBytes:    250000000,
			S3Url:        stringPtr("s3://demo-bucket/dvr/dvr_hash_demo_001.ts"),
			NodeId:       "node_demo_us_west_01",
		},
		{
			Id:           "se_demo_005",
			Timestamp:    timestamppb.New(now.Add(-24 * time.Hour)),
			TenantId:     "00000000-0000-0000-0000-000000000001",
			InternalName: streamFilter,
			AssetHash:    "dvr_hash_demo_001",
			Action:       "frozen",
			AssetType:    "dvr",
			SizeBytes:    250000000,
			S3Url:        stringPtr("s3://demo-bucket/dvr/dvr_hash_demo_001.ts"),
			LocalPath:    stringPtr("/mnt/storage/dvr/dvr_hash_demo_001.ts"),
			NodeId:       "node_demo_us_west_01",
			DurationMs:   int64Ptr(12500), // 12.5 seconds
		},
	}

	edges := make([]*model.StorageEventEdge, len(events))
	for i, event := range events {
		edges[i] = &model.StorageEventEdge{
			Cursor: event.Id,
			Node:   event,
		}
	}

	return &model.StorageEventsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(events),
	}
}

// GenerateViewerSessionsConnection creates demo viewer sessions with pagination.
// These represent individual viewer session data from ClickHouse viewer_sessions table.
func GenerateViewerSessionsConnection(streamFilter *string) *model.ViewerSessionsConnection {
	now := time.Now()

	stream := "demo_live_stream_001"
	if streamFilter != nil && *streamFilter != "" {
		stream = *streamFilter
	}

	sessions := []*pb.ViewerSession{
		{
			SessionId:         "sess_demo_viewer_001",
			Timestamp:         timestamppb.New(now.Add(-30 * time.Minute)),
			InternalName:      stream,
			TenantId:          "00000000-0000-0000-0000-000000000001",
			NodeId:            "node_demo_us_west_01",
			ConnectionAddr:    "", // Redacted for privacy
			Connector:         "HLS",
			CountryCode:       "US",
			City:              "San Francisco",
			Latitude:          37.7749,
			Longitude:         -122.4194,
			DurationSeconds:   1800, // 30 minutes
			BytesUp:           512000,
			BytesDown:         256000000, // 256 MB downloaded
			ViewerCount:       1,
			ConnectionType:    "HLS",
			ConnectionQuality: 0.95,
			BufferHealth:      0.98,
		},
		{
			SessionId:         "sess_demo_viewer_002",
			Timestamp:         timestamppb.New(now.Add(-45 * time.Minute)),
			InternalName:      stream,
			TenantId:          "00000000-0000-0000-0000-000000000001",
			NodeId:            "node_demo_eu_west_01",
			ConnectionAddr:    "", // Redacted for privacy
			Connector:         "DASH",
			CountryCode:       "GB",
			City:              "London",
			Latitude:          51.5074,
			Longitude:         -0.1278,
			DurationSeconds:   2700, // 45 minutes
			BytesUp:           768000,
			BytesDown:         384000000, // 384 MB downloaded
			ViewerCount:       1,
			ConnectionType:    "DASH",
			ConnectionQuality: 0.92,
			BufferHealth:      0.96,
		},
		{
			SessionId:         "sess_demo_viewer_003",
			Timestamp:         timestamppb.New(now.Add(-15 * time.Minute)),
			InternalName:      stream,
			TenantId:          "00000000-0000-0000-0000-000000000001",
			NodeId:            "node_demo_us_west_01",
			ConnectionAddr:    "", // Redacted for privacy
			Connector:         "WebRTC",
			CountryCode:       "CA",
			City:              "Toronto",
			Latitude:          43.6532,
			Longitude:         -79.3832,
			DurationSeconds:   900, // 15 minutes (still watching)
			BytesUp:           256000,
			BytesDown:         128000000, // 128 MB downloaded
			ViewerCount:       1,
			ConnectionType:    "WebRTC",
			ConnectionQuality: 0.97,
			BufferHealth:      0.99,
		},
		{
			SessionId:         "sess_demo_viewer_004",
			Timestamp:         timestamppb.New(now.Add(-60 * time.Minute)),
			InternalName:      stream,
			TenantId:          "00000000-0000-0000-0000-000000000001",
			NodeId:            "node_demo_apac_01",
			ConnectionAddr:    "", // Redacted for privacy
			Connector:         "HLS",
			CountryCode:       "JP",
			City:              "Tokyo",
			Latitude:          35.6762,
			Longitude:         139.6503,
			DurationSeconds:   3600, // 60 minutes
			BytesUp:           1024000,
			BytesDown:         512000000, // 512 MB downloaded
			ViewerCount:       1,
			ConnectionType:    "HLS",
			ConnectionQuality: 0.88,
			BufferHealth:      0.94,
		},
		{
			SessionId:         "sess_demo_viewer_005",
			Timestamp:         timestamppb.New(now.Add(-10 * time.Minute)),
			InternalName:      stream,
			TenantId:          "00000000-0000-0000-0000-000000000001",
			NodeId:            "node_demo_us_west_01",
			ConnectionAddr:    "", // Redacted for privacy
			Connector:         "RTMP",
			CountryCode:       "US",
			City:              "Los Angeles",
			Latitude:          34.0522,
			Longitude:         -118.2437,
			DurationSeconds:   600, // 10 minutes
			BytesUp:           128000,
			BytesDown:         64000000, // 64 MB downloaded
			ViewerCount:       1,
			ConnectionType:    "RTMP",
			ConnectionQuality: 0.91,
			BufferHealth:      0.93,
		},
	}

	edges := make([]*model.ViewerSessionEdge, len(sessions))
	for i, session := range sessions {
		edges[i] = &model.ViewerSessionEdge{
			Cursor: session.SessionId,
			Node:   session,
		}
	}

	return &model.ViewerSessionsConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(sessions),
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

// GenerateUsers creates demo users
func GenerateUsers() []*pb.User {
	now := time.Now()
	return []*pb.User{
		{
			Id:          "user_demo_admin",
			TenantId:    "00000000-0000-0000-0000-000000000001",
			Email:       "admin@demo.frameworks.video",
			FirstName:   "Admin",
			LastName:    "User",
			Role:        "admin",
			Permissions: []string{"admin", "streams:write", "streams:read", "billing:read", "billing:write"},
			IsActive:    true,
			IsVerified:  true,
			LastLoginAt: timestamppb.New(now.Add(-1 * time.Hour)),
			CreatedAt:   timestamppb.New(now.Add(-90 * 24 * time.Hour)),
			UpdatedAt:   timestamppb.New(now.Add(-5 * 24 * time.Hour)),
		},
		{
			Id:          "user_demo_viewer",
			TenantId:    "00000000-0000-0000-0000-000000000001",
			Email:       "viewer@demo.frameworks.video",
			FirstName:   "Viewer",
			LastName:    "User",
			Role:        "member",
			Permissions: []string{"streams:read"},
			IsActive:    true,
			IsVerified:  true,
			LastLoginAt: timestamppb.New(now.Add(-5 * time.Hour)),
			CreatedAt:   timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:   timestamppb.New(now.Add(-30 * 24 * time.Hour)),
		},
	}
}

// GenerateServices creates demo services
func GenerateServices() []*pb.Service {
	now := time.Now()
	return []*pb.Service{
		{
			Id:              "svc_def_mist",
			ServiceId:       "service_mist",
			Name:            "MistServer",
			Plane:           "data",
			Description:     stringPtr("MistServer media server instance"),
			DefaultPort:     int32Ptr(4242),
			HealthCheckPath: stringPtr("/health"),
			DockerImage:     stringPtr("mistserver/mistserver:latest"),
			Version:         stringPtr("3.3"),
			IsActive:        true,
			CreatedAt:       timestamppb.New(now.Add(-100 * 24 * time.Hour)),
			Type:            "media_server",
			Protocol:        "http",
		},
		{
			Id:              "svc_def_foghorn",
			ServiceId:       "service_foghorn",
			Name:            "Foghorn",
			Plane:           "control",
			Description:     stringPtr("Load balancing and orchestration service"),
			DefaultPort:     int32Ptr(8080),
			HealthCheckPath: stringPtr("/healthz"),
			DockerImage:     stringPtr("frameworks/foghorn:latest"),
			Version:         stringPtr("1.2.0"),
			IsActive:        true,
			CreatedAt:       timestamppb.New(now.Add(-100 * 24 * time.Hour)),
			Type:            "orchestrator",
			Protocol:        "grpc",
		},
	}
}

// GenerateBootstrapTokens creates demo bootstrap tokens
func GenerateBootstrapTokens() []*pb.BootstrapToken {
	now := time.Now()
	return []*pb.BootstrapToken{
		{
			Id:         "token_boot_001",
			Name:       "Edge Node Token",
			Token:      "boot_edge_xyz123",
			Kind:       "edge_node",
			TenantId:   stringPtr("00000000-0000-0000-0000-000000000001"),
			UsageLimit: int32Ptr(10),
			UsageCount: 2,
			ExpiresAt:  timestamppb.New(now.Add(24 * time.Hour)),
			CreatedAt:  timestamppb.New(now.Add(-1 * time.Hour)),
		},
	}
}

// GenerateInfrastructureNodes returns a slice of demo nodes (wrapper around connection gen)
func GenerateInfrastructureNodes() []*pb.InfrastructureNode {
	conn := GenerateNodesConnection()
	result := make([]*pb.InfrastructureNode, len(conn.Edges))
	for i, edge := range conn.Edges {
		result[i] = edge.Node
	}
	return result
}

// GenerateInfrastructureClusters returns a slice of demo clusters (wrapper around connection gen)
func GenerateInfrastructureClusters() []*pb.InfrastructureCluster {
	conn := GenerateClustersConnection()
	result := make([]*pb.InfrastructureCluster, len(conn.Edges))
	for i, edge := range conn.Edges {
		result[i] = edge.Node
	}
	return result
}

// GenerateClips returns a slice of demo clips matching shared.ClipInfo
func GenerateClips() []*pb.ClipInfo {
	now := time.Now()
	return []*pb.ClipInfo{
		{
			Id:          "clip_info_demo_001",
			ClipHash:    "hash_demo_001",
			StreamName:  "demo_live_stream_001",
			Title:       "Best Moments",
			Description: "Highlights from the stream",
			StartTime:   now.Add(-90 * time.Minute).Unix(),
			Duration:    300,
			NodeId:      "node_demo_us_west_01",
			StoragePath: "/clips/demo_clip_001.mp4",
			SizeBytes:   int64Ptr(15000000),
			Status:      "ready",
			AccessCount: 42,
			CreatedAt:   timestamppb.New(now.Add(-30 * time.Minute)),
			UpdatedAt:   timestamppb.New(now.Add(-30 * time.Minute)),
			ClipMode:    stringPtr("absolute"),
		},
		{
			Id:          "clip_info_demo_002",
			ClipHash:    "hash_demo_002",
			StreamName:  "demo_live_stream_001",
			Title:       "Intro Sequence",
			Description: "Stream introduction",
			StartTime:   now.Add(-120 * time.Minute).Unix(),
			Duration:    60,
			NodeId:      "node_demo_us_west_01",
			StoragePath: "/clips/demo_clip_002.mp4",
			SizeBytes:   int64Ptr(5000000),
			Status:      "ready",
			AccessCount: 12,
			CreatedAt:   timestamppb.New(now.Add(-45 * time.Minute)),
			UpdatedAt:   timestamppb.New(now.Add(-45 * time.Minute)),
			ClipMode:    stringPtr("absolute"),
		},
	}
}

// ============================================================================
// CLUSTER MARKETPLACE DEMO DATA
// ============================================================================

// GenerateMarketplaceClusters returns demo marketplace cluster entries
func GenerateMarketplaceClusters() []*pb.MarketplaceClusterEntry {
	return []*pb.MarketplaceClusterEntry{
		{
			ClusterId:         "cluster_demo_platform",
			ClusterName:       "FrameWorks Platform (Free)",
			ShortDescription:  stringPtr("Free tier platform cluster for all users"),
			Visibility:        pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC,
			PricingModel:      pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED,
			OwnerName:         stringPtr("FrameWorks"),
			IsSubscribed:      true,
			IsPlatformCluster: true,
		},
		{
			ClusterId:         "cluster_demo_us_west",
			ClusterName:       "US West CDN (Oregon)",
			ShortDescription:  stringPtr("Low-latency US West Coast edge delivery"),
			Visibility:        pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC,
			PricingModel:      pb.ClusterPricingModel_CLUSTER_PRICING_METERED,
			MonthlyPriceCents: 0,
			OwnerName:         stringPtr("FrameWorks"),
			IsSubscribed:      false,
			IsPlatformCluster: true,
		},
		{
			ClusterId:         "cluster_demo_eu_west",
			ClusterName:       "EU West CDN (Ireland)",
			ShortDescription:  stringPtr("GDPR-compliant European edge delivery"),
			Visibility:        pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC,
			PricingModel:      pb.ClusterPricingModel_CLUSTER_PRICING_METERED,
			MonthlyPriceCents: 0,
			OwnerName:         stringPtr("FrameWorks"),
			IsSubscribed:      false,
			IsPlatformCluster: true,
		},
		{
			ClusterId:         "cluster_demo_enterprise",
			ClusterName:       "Enterprise Private Cloud",
			ShortDescription:  stringPtr("Dedicated infrastructure for enterprise clients"),
			Visibility:        pb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED,
			PricingModel:      pb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY,
			MonthlyPriceCents: 99900, // $999/month
			OwnerName:         stringPtr("ACME Corp"),
			IsSubscribed:      false,
			IsPlatformCluster: false,
		},
	}
}

// GenerateMySubscriptions returns clusters the demo tenant is subscribed to
// This represents the "My Subscriptions" list showing clusters the user has access to
func GenerateMySubscriptions() []*pb.InfrastructureCluster {
	now := time.Now()
	return []*pb.InfrastructureCluster{
		{
			Id:                    "cluster_demo_platform",
			ClusterId:             "central-primary",
			ClusterName:           "Central Primary Cluster",
			ClusterType:           "origin",
			DeploymentModel:       "managed",
			BaseUrl:               "https://api.demo.frameworks.dev",
			MaxConcurrentStreams:  100,
			MaxConcurrentViewers:  10000,
			MaxBandwidthMbps:      10000,
			CurrentStreamCount:    5,
			CurrentViewerCount:    150,
			CurrentBandwidthMbps:  500,
			HealthStatus:          "healthy",
			IsActive:              true,
			IsDefaultCluster:      true,
			CreatedAt:             timestamppb.New(now.Add(-30 * 24 * time.Hour)),
			UpdatedAt:             timestamppb.New(now.Add(-1 * time.Hour)),
		},
	}
}

// GenerateClusterInvites returns demo cluster invites (for cluster owners)
func GenerateClusterInvites() []*pb.ClusterInvite {
	now := time.Now()
	return []*pb.ClusterInvite{
		{
			Id:               "invite_demo_001",
			ClusterId:        "cluster_demo_enterprise",
			InvitedTenantId:  "tenant_demo_partner",
			InviteToken:      "inv_tok_demo_abc123",
			AccessLevel:      "subscriber",
			Status:           "pending",
			InvitedTenantName: stringPtr("Partner Inc"),
			CreatedAt:        timestamppb.New(now.Add(-24 * time.Hour)),
			ExpiresAt:        timestamppb.New(now.Add(6 * 24 * time.Hour)),
		},
	}
}

// GenerateMyClusterInvites returns demo pending invites for the current tenant
func GenerateMyClusterInvites() []*pb.ClusterInvite {
	now := time.Now()
	return []*pb.ClusterInvite{
		{
			Id:                "invite_demo_002",
			ClusterId:         "cluster_demo_premium",
			InvitedTenantId:   "tenant_demo_frameworks",
			InviteToken:       "inv_tok_demo_xyz789",
			AccessLevel:       "subscriber",
			Status:            "pending",
			InvitedTenantName: stringPtr("Demo User"),
			CreatedAt:         timestamppb.New(now.Add(-2 * time.Hour)),
			ExpiresAt:         timestamppb.New(now.Add(28 * 24 * time.Hour)),
		},
	}
}

// GeneratePendingSubscriptions returns demo pending subscription requests
func GeneratePendingSubscriptions() []*pb.ClusterSubscription {
	now := time.Now()
	return []*pb.ClusterSubscription{
		{
			Id:                 "sub_demo_001",
			TenantId:           "tenant_demo_requester",
			ClusterId:          "cluster_demo_enterprise",
			AccessLevel:        "subscriber",
			SubscriptionStatus: pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL,
			TenantName:         stringPtr("Requester Corp"),
			ClusterName:        stringPtr("Enterprise Private Cloud"),
			RequestedAt:        timestamppb.New(now.Add(-4 * time.Hour)),
			CreatedAt:          timestamppb.New(now.Add(-4 * time.Hour)),
			UpdatedAt:          timestamppb.New(now.Add(-4 * time.Hour)),
		},
	}
}

// GenerateNodePerformance5mConnection creates demo 5-minute node performance data
func GenerateNodePerformance5mConnection(nodeID *string) *model.NodePerformance5mConnection {
	now := time.Now()

	nodeFilter := "node_demo_us_west_01"
	if nodeID != nil && *nodeID != "" {
		nodeFilter = *nodeID
	}

	records := []*pb.NodePerformance5M{
		{
			Id:             "np5m_demo_001",
			Timestamp:      timestamppb.New(now.Truncate(5 * time.Minute)),
			NodeId:         nodeFilter,
			AvgCpu:         45.5,
			MaxCpu:         68.2,
			AvgMemory:      62.3,
			MaxMemory:      75.8,
			TotalBandwidth: 5400000000, // 5.4 GB
			AvgStreams:     12,
			MaxStreams:     18,
		},
		{
			Id:             "np5m_demo_002",
			Timestamp:      timestamppb.New(now.Add(-5 * time.Minute).Truncate(5 * time.Minute)),
			NodeId:         nodeFilter,
			AvgCpu:         52.1,
			MaxCpu:         72.5,
			AvgMemory:      65.8,
			MaxMemory:      78.2,
			TotalBandwidth: 6200000000, // 6.2 GB
			AvgStreams:     15,
			MaxStreams:     22,
		},
		{
			Id:             "np5m_demo_003",
			Timestamp:      timestamppb.New(now.Add(-10 * time.Minute).Truncate(5 * time.Minute)),
			NodeId:         nodeFilter,
			AvgCpu:         38.7,
			MaxCpu:         55.3,
			AvgMemory:      58.1,
			MaxMemory:      68.5,
			TotalBandwidth: 4100000000, // 4.1 GB
			AvgStreams:     10,
			MaxStreams:     14,
		},
	}

	edges := make([]*model.NodePerformance5mEdge, len(records))
	for i, record := range records {
		edges[i] = &model.NodePerformance5mEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.NodePerformance5mConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateViewerHoursHourlyConnection creates demo hourly viewer-hours data
func GenerateViewerHoursHourlyConnection(stream *string) *model.ViewerHoursHourlyConnection {
	now := time.Now()

	internalName := "demo_live_stream_001"
	if stream != nil && *stream != "" {
		internalName = *stream
	}

	records := []*pb.ViewerHoursHourly{
		{
			Id:                  "vhh_demo_001",
			Hour:                timestamppb.New(now.Truncate(time.Hour)),
			TenantId:            "00000000-0000-0000-0000-000000000001",
			InternalName:        internalName,
			CountryCode:         "US",
			UniqueViewers:       185,
			TotalSessionSeconds: 56700,
			TotalBytes:          2_400_000_000, // 2.4 GB
		},
		{
			Id:                  "vhh_demo_002",
			Hour:                timestamppb.New(now.Add(-1 * time.Hour).Truncate(time.Hour)),
			TenantId:            "00000000-0000-0000-0000-000000000001",
			InternalName:        internalName,
			CountryCode:         "DE",
			UniqueViewers:       92,
			TotalSessionSeconds: 28350,
			TotalBytes:          1_150_000_000, // 1.15 GB
		},
		{
			Id:                  "vhh_demo_003",
			Hour:                timestamppb.New(now.Add(-2 * time.Hour).Truncate(time.Hour)),
			TenantId:            "00000000-0000-0000-0000-000000000001",
			InternalName:        internalName,
			CountryCode:         "GB",
			UniqueViewers:       78,
			TotalSessionSeconds: 24120,
			TotalBytes:          980_000_000, // 0.98 GB
		},
	}

	edges := make([]*model.ViewerHoursHourlyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.ViewerHoursHourlyEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.ViewerHoursHourlyConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateViewerGeoHourlyConnection creates demo hourly geographic viewer data
func GenerateViewerGeoHourlyConnection(stream *string) *model.ViewerGeoHourlyConnection {
	now := time.Now()

	records := []*pb.ViewerGeoHourly{
		{
			Id:          "vgh_demo_001",
			Hour:        timestamppb.New(now.Truncate(time.Hour)),
			TenantId:    "00000000-0000-0000-0000-000000000001",
			CountryCode: "US",
			ViewerCount: 185,
			ViewerHours: 15.75,
			EgressGb:    2.4,
		},
		{
			Id:          "vgh_demo_002",
			Hour:        timestamppb.New(now.Truncate(time.Hour)),
			TenantId:    "00000000-0000-0000-0000-000000000001",
			CountryCode: "DE",
			ViewerCount: 92,
			ViewerHours: 7.875,
			EgressGb:    1.15,
		},
		{
			Id:          "vgh_demo_003",
			Hour:        timestamppb.New(now.Truncate(time.Hour)),
			TenantId:    "00000000-0000-0000-0000-000000000001",
			CountryCode: "GB",
			ViewerCount: 78,
			ViewerHours: 6.7,
			EgressGb:    0.98,
		},
		{
			Id:          "vgh_demo_004",
			Hour:        timestamppb.New(now.Truncate(time.Hour)),
			TenantId:    "00000000-0000-0000-0000-000000000001",
			CountryCode: "JP",
			ViewerCount: 45,
			ViewerHours: 3.85,
			EgressGb:    0.62,
		},
	}

	edges := make([]*model.ViewerGeoHourlyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.ViewerGeoHourlyEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.ViewerGeoHourlyConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateStreamHealth5mConnection creates demo 5-minute stream health data
func GenerateStreamHealth5mConnection(stream *string) *model.StreamHealth5mConnection {
	now := time.Now()

	internalName := "demo_live_stream_001"
	if stream != nil && *stream != "" {
		internalName = *stream
	}

	records := []*pb.StreamHealth5M{
		{
			Id:             "sh5m_demo_001",
			Timestamp:      timestamppb.New(now.Truncate(5 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
			InternalName:   internalName,
			NodeId:         "node_demo_us_west_01",
			RebufferCount:  0,
			IssueCount:     0,
			SampleIssues:   "",
			AvgBitrate:     2500,
			AvgFps:         29.97,
			BufferDryCount: 0,
			QualityTier:    "1080p",
		},
		{
			Id:             "sh5m_demo_002",
			Timestamp:      timestamppb.New(now.Add(-5 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
			InternalName:   internalName,
			NodeId:         "node_demo_us_west_01",
			RebufferCount:  2,
			IssueCount:     1,
			SampleIssues:   "buffer_dry",
			AvgBitrate:     2350,
			AvgFps:         28.5,
			BufferDryCount: 1,
			QualityTier:    "1080p",
		},
		{
			Id:             "sh5m_demo_003",
			Timestamp:      timestamppb.New(now.Add(-10 * time.Minute).Truncate(5 * time.Minute)),
			TenantId:       "00000000-0000-0000-0000-000000000001",
			InternalName:   internalName,
			NodeId:         "node_demo_us_west_01",
			RebufferCount:  0,
			IssueCount:     0,
			SampleIssues:   "",
			AvgBitrate:     2480,
			AvgFps:         29.95,
			BufferDryCount: 0,
			QualityTier:    "1080p",
		},
	}

	edges := make([]*model.StreamHealth5mEdge, len(records))
	for i, record := range records {
		edges[i] = &model.StreamHealth5mEdge{
			Cursor: record.Id,
			Node:   record,
		}
	}

	return &model.StreamHealth5mConnection{
		Edges:      edges,
		PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
		TotalCount: len(records),
	}
}

// GenerateTenantDailyStats creates demo daily tenant statistics
func GenerateTenantDailyStats(days *int) []*pb.TenantDailyStat {
	now := time.Now()
	daysCount := 7
	if days != nil && *days > 0 {
		daysCount = *days
	}

	stats := make([]*pb.TenantDailyStat, daysCount)
	for i := 0; i < daysCount; i++ {
		dayTime := now.Add(time.Duration(-i) * 24 * time.Hour).Truncate(24 * time.Hour)
		stats[i] = &pb.TenantDailyStat{
			Id:            fmt.Sprintf("tds_demo_%03d", i+1),
			Date:          timestamppb.New(dayTime),
			TenantId:      "00000000-0000-0000-0000-000000000001",
			EgressGb:      float64(25 + i*3 + (i%3)*5),
			ViewerHours:   float64(150 + i*20 + (i%2)*35),
			UniqueViewers: int32(450 + i*50 + (i%3)*75),
			TotalSessions: int32(1250 + i*100 + (i%2)*180),
			TotalViews:    int64(2500 + int64(i)*200 + int64(i%3)*350),
		}
	}

	return stats
}

// =============================================================================
// VOD DEMO GENERATORS
// =============================================================================

// GenerateVodUploadSession creates a demo VOD upload session
func GenerateVodUploadSession(filename string, sizeBytes int) *model.VodUploadSession {
	now := time.Now()

	// Calculate parts (20MB chunks)
	partSize := int64(20 * 1024 * 1024) // 20MB
	partCount := int(math.Ceil(float64(sizeBytes) / float64(partSize)))
	if partCount < 1 {
		partCount = 1
	}
	if partCount > 100 {
		partCount = 100 // Cap for demo
	}

	parts := make([]*pb.VodUploadPart, partCount)
	for i := 0; i < partCount; i++ {
		parts[i] = &pb.VodUploadPart{
			PartNumber:   int32(i + 1),
			PresignedUrl: fmt.Sprintf("https://demo-s3.example.com/vod/upload/%s?partNumber=%d&uploadId=demo_upload_123", filename, i+1),
		}
	}

	return &model.VodUploadSession{
		ID:           "demo_upload_" + now.Format("20060102150405"),
		ArtifactID:   "artifact_demo_vod_" + now.Format("20060102150405"),
		ArtifactHash: "vod_demo_hash_" + now.Format("150405"),
		PartSize:     int(partSize),
		Parts:        parts,
		ExpiresAt:    now.Add(2 * time.Hour),
	}
}

// GenerateVodAsset creates a single demo VOD asset
func GenerateVodAsset() *model.VodAsset {
	now := time.Now()
	title := "Demo Video Upload"
	description := "A demo video file for testing"
	filename := "demo_video.mp4"
	sizeBytes := float64(150 * 1024 * 1024) // 150MB
	durationMs := 180000                    // 3 minutes
	resolution := "1920x1080"
	videoCodec := "h264"
	audioCodec := "aac"
	bitrateKbps := 5000

	return &model.VodAsset{
		ID:              "vod_demo_001",
		ArtifactHash:    "a1b2c3d4e5f6789012345678901234ab",
		Title:           &title,
		Description:     &description,
		Filename:        &filename,
		Status:          model.VodAssetStatusReady,
		StorageLocation: "s3",
		SizeBytes:       &sizeBytes,
		DurationMs:      &durationMs,
		Resolution:      &resolution,
		VideoCodec:      &videoCodec,
		AudioCodec:      &audioCodec,
		BitrateKbps:     &bitrateKbps,
		CreatedAt:       now.Add(-24 * time.Hour),
		UpdatedAt:       now.Add(-23 * time.Hour),
	}
}

// GenerateVodAssets creates a list of demo VOD assets
func GenerateVodAssets() []*model.VodAsset {
	now := time.Now()

	// Helper for string pointers
	sp := func(s string) *string { return &s }
	fp := func(f float64) *float64 { return &f }
	ip := func(i int) *int { return &i }

	return []*model.VodAsset{
		{
			ID:              "vod_demo_001",
			ArtifactHash:    "a1b2c3d4e5f6789012345678901234ab",
			Title:           sp("Product Demo Video"),
			Description:     sp("Full product demonstration walkthrough"),
			Filename:        sp("product_demo_2024.mp4"),
			Status:          model.VodAssetStatusReady,
			StorageLocation: "s3",
			SizeBytes:       fp(250 * 1024 * 1024),
			DurationMs:      ip(300000), // 5 minutes
			Resolution:      sp("1920x1080"),
			VideoCodec:      sp("h264"),
			AudioCodec:      sp("aac"),
			BitrateKbps:     ip(6000),
			CreatedAt:       now.Add(-48 * time.Hour),
			UpdatedAt:       now.Add(-47 * time.Hour),
		},
		{
			ID:              "vod_demo_002",
			ArtifactHash:    "b2c3d4e5f678901234567890123456bc",
			Title:           sp("Tutorial: Getting Started"),
			Description:     sp("Step-by-step guide for new users"),
			Filename:        sp("getting_started_tutorial.mp4"),
			Status:          model.VodAssetStatusReady,
			StorageLocation: "local",
			SizeBytes:       fp(180 * 1024 * 1024),
			DurationMs:      ip(420000), // 7 minutes
			Resolution:      sp("1280x720"),
			VideoCodec:      sp("h264"),
			AudioCodec:      sp("aac"),
			BitrateKbps:     ip(4000),
			CreatedAt:       now.Add(-24 * time.Hour),
			UpdatedAt:       now.Add(-23 * time.Hour),
		},
		{
			ID:              "vod_demo_003",
			ArtifactHash:    "c3d4e5f67890123456789012345678cd",
			Title:           sp("Feature Highlight Reel"),
			Filename:        sp("feature_highlights.mp4"),
			Status:          model.VodAssetStatusProcessing,
			StorageLocation: "s3",
			SizeBytes:       fp(320 * 1024 * 1024),
			CreatedAt:       now.Add(-2 * time.Hour),
			UpdatedAt:       now.Add(-1 * time.Hour),
		},
		{
			ID:              "vod_demo_004",
			ArtifactHash:    "d4e5f6789012345678901234567890de",
			Title:           sp("Conference Recording"),
			Description:     sp("Annual developer conference keynote"),
			Filename:        sp("conference_2024.mp4"),
			Status:          model.VodAssetStatusUploading,
			StorageLocation: "pending",
			SizeBytes:       fp(1500 * 1024 * 1024),
			CreatedAt:       now.Add(-30 * time.Minute),
			UpdatedAt:       now.Add(-10 * time.Minute),
		},
	}
}

// GenerateProcessingUsageConnection creates demo transcoding/processing usage records
func GenerateProcessingUsageConnection(streamName *string, processType *string) *model.ProcessingUsageConnection {
	now := time.Now()

	records := []*pb.ProcessingUsageRecord{
		{
			Id:               "pu_demo_001",
			Timestamp:        timestamppb.New(now.Add(-1 * time.Hour)),
			TenantId:         "00000000-0000-0000-0000-000000000001",
			NodeId:           "node_demo_us_west_01",
			StreamName:       "demo_live_stream_001",
			ProcessType:      "AV",
			TrackType:        stringPtr("video"),
			DurationMs:       5000,
			InputCodec:       stringPtr("H264"),
			OutputCodec:      stringPtr("H264"),
			InputWidth:       int32Ptr(1920),
			InputHeight:      int32Ptr(1080),
			OutputWidth:      int32Ptr(1280),
			OutputHeight:     int32Ptr(720),
			InputFrames:      int64Ptr(150),
			OutputFrames:     int64Ptr(148),
			InputFramesDelta: int64Ptr(30),
			OutputFramesDelta: int64Ptr(30),
			DecodeUsPerFrame:  int64Ptr(850),
			TransformUsPerFrame: int64Ptr(420),
			EncodeUsPerFrame: int64Ptr(1200),
			RtfIn:            float64Ptr(0.95),
			RtfOut:           float64Ptr(0.97),
			PipelineLagMs:    int64Ptr(45),
			OutputBitrateBps: int64Ptr(4500000),
		},
		{
			Id:               "pu_demo_002",
			Timestamp:        timestamppb.New(now.Add(-1*time.Hour - 5*time.Second)),
			TenantId:         "00000000-0000-0000-0000-000000000001",
			NodeId:           "node_demo_us_west_01",
			StreamName:       "demo_live_stream_001",
			ProcessType:      "AV",
			TrackType:        stringPtr("audio"),
			DurationMs:       5000,
			InputCodec:       stringPtr("AAC"),
			OutputCodec:      stringPtr("AAC"),
			SampleRate:       int32Ptr(48000),
			Channels:         int32Ptr(2),
			InputFrames:      int64Ptr(240),
			OutputFrames:     int64Ptr(240),
			RtfIn:            float64Ptr(1.0),
			RtfOut:           float64Ptr(1.0),
			PipelineLagMs:    int64Ptr(12),
			OutputBitrateBps: int64Ptr(128000),
		},
		{
			Id:                  "pu_demo_003",
			Timestamp:           timestamppb.New(now.Add(-2 * time.Hour)),
			TenantId:            "00000000-0000-0000-0000-000000000001",
			NodeId:              "node_demo_us_west_01",
			StreamName:          "demo_live_stream_002",
			ProcessType:         "Livepeer",
			TrackType:           stringPtr("video"),
			DurationMs:          2000,
			InputCodec:          stringPtr("H264"),
			Width:               int32Ptr(1920),
			Height:              int32Ptr(1080),
			RenditionCount:      int32Ptr(3),
			LivepeerSessionId:   stringPtr("lp_session_abc123"),
			SegmentNumber:       int32Ptr(42),
			SegmentStartMs:      int64Ptr(84000),
			InputBytes:          int64Ptr(512000),
			OutputBytesTotal:    int64Ptr(890000),
			TurnaroundMs:        int64Ptr(320),
			SpeedFactor:         float64Ptr(6.25),
			BroadcasterUrl:      stringPtr("https://livepeer-broadcaster.example.com"),
			RenditionsJson:      stringPtr(`[{"name":"720p","bytes":380000},{"name":"480p","bytes":280000},{"name":"360p","bytes":230000}]`),
		},
	}

	// Filter by streamName if provided
	if streamName != nil && *streamName != "" {
		var filtered []*pb.ProcessingUsageRecord
		for _, r := range records {
			if r.StreamName == *streamName {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	// Filter by processType if provided
	if processType != nil && *processType != "" {
		var filtered []*pb.ProcessingUsageRecord
		for _, r := range records {
			if r.ProcessType == *processType {
				filtered = append(filtered, r)
			}
		}
		records = filtered
	}

	edges := make([]*model.ProcessingUsageEdge, len(records))
	for i, r := range records {
		edges[i] = &model.ProcessingUsageEdge{
			Cursor: r.Id,
			Node:   r,
		}
	}

	// Generate demo summaries (last 7 days) with per-codec breakdown
	summaries := make([]*pb.ProcessingUsageSummary, 7)
	for i := 0; i < 7; i++ {
		day := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)
		// Calculate totals from per-codec values
		lpH264 := float64(800 + i*100)
		lpVp9 := float64(200 + i*30)
		lpAv1 := float64(100 + i*15)
		lpHevc := float64(100 + i*5)
		avH264 := float64(5000 + i*300)
		avVp9 := float64(1500 + i*100)
		avAv1 := float64(800 + i*60)
		avHevc := float64(200 + i*20)
		avAac := float64(800 + i*15)  // Audio is FREE
		avOpus := float64(200 + i*5)  // Audio is FREE

		summaries[i] = &pb.ProcessingUsageSummary{
			Date:                  timestamppb.New(day),
			TenantId:              "00000000-0000-0000-0000-000000000001",
			// Livepeer totals
			LivepeerSeconds:       lpH264 + lpVp9 + lpAv1 + lpHevc,
			LivepeerSegmentCount:  uint64(600 + i*75),
			LivepeerUniqueStreams: uint32(3 + i%2),
			// Livepeer per-codec
			LivepeerH264Seconds: lpH264,
			LivepeerVp9Seconds:  lpVp9,
			LivepeerAv1Seconds:  lpAv1,
			LivepeerHevcSeconds: lpHevc,
			// Native AV totals
			NativeAvSeconds:       avH264 + avVp9 + avAv1 + avHevc + avAac + avOpus,
			NativeAvSegmentCount:  uint64(1700 + i*100),
			NativeAvUniqueStreams: uint32(5 + i%3),
			// Native AV per-codec
			NativeAvH264Seconds: avH264,
			NativeAvVp9Seconds:  avVp9,
			NativeAvAv1Seconds:  avAv1,
			NativeAvHevcSeconds: avHevc,
			NativeAvAacSeconds:  avAac,
			NativeAvOpusSeconds: avOpus,
			// Track type aggregates
			AudioSeconds: avAac + avOpus,
			VideoSeconds: lpH264 + lpVp9 + lpAv1 + lpHevc + avH264 + avVp9 + avAv1 + avHevc,
		}
	}

	return &model.ProcessingUsageConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
			StartCursor:     stringPtr("pu_demo_001"),
			EndCursor:       stringPtr("pu_demo_003"),
		},
		TotalCount: len(records),
		Summaries:  summaries,
	}
}

// GenerateRebufferingEventsConnection creates demo rebuffering events data
func GenerateRebufferingEventsConnection(internalName *string) *model.RebufferingEventsConnection {
	now := time.Now()
	streamName := "demo_live_stream_001"
	if internalName != nil && *internalName != "" {
		streamName = *internalName
	}

	// Generate sample rebuffering events
	bufferStates := []string{"FILLING", "FULL", "DRY", "FILLING"}
	events := make([]*pb.RebufferingEvent, 4)

	for i := 0; i < 4; i++ {
		timestamp := now.Add(-time.Duration(30-i*5) * time.Minute)
		rebufferStart := timestamp.Add(-time.Second * 2)
		rebufferEnd := timestamp

		events[i] = &pb.RebufferingEvent{
			Id:            fmt.Sprintf("rebuf_%d", i+1),
			Timestamp:     timestamppb.New(timestamp),
			InternalName:  streamName,
			NodeId:        "demo_node_001",
			BufferState:   bufferStates[i],
			PrevState:     bufferStates[(i+3)%4], // Previous state
			RebufferStart: timestamppb.New(rebufferStart),
			RebufferEnd:   timestamppb.New(rebufferEnd),
		}
	}

	edges := make([]*model.RebufferingEventEdge, len(events))
	for i, event := range events {
		edges[i] = &model.RebufferingEventEdge{
			Cursor: fmt.Sprintf("rebuf_cursor_%d", i+1),
			Node:   event,
		}
	}

	return &model.RebufferingEventsConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
			StartCursor:     stringPtr("rebuf_cursor_1"),
			EndCursor:       stringPtr("rebuf_cursor_4"),
		},
		TotalCount: len(events),
	}
}

// GenerateTenantAnalyticsDailyConnection creates demo tenant daily analytics data
func GenerateTenantAnalyticsDailyConnection() *model.TenantAnalyticsDailyConnection {
	now := time.Now()

	// Generate 7 days of tenant analytics
	records := make([]*pb.TenantAnalyticsDaily, 7)
	for i := 0; i < 7; i++ {
		day := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)

		// Simulate varying activity levels
		baseStreams := 5 + rand.Intn(10)
		baseViews := 1000 + rand.Intn(5000)
		baseViewers := 100 + rand.Intn(500)
		baseEgress := int64(10_000_000_000 + rand.Intn(50_000_000_000)) // 10-60 GB

		records[i] = &pb.TenantAnalyticsDaily{
			Id:            fmt.Sprintf("tad_%s", day.Format("2006-01-02")),
			Day:           timestamppb.New(day),
			TotalStreams:  int32(baseStreams),
			TotalViews:    int64(baseViews),
			UniqueViewers: int32(baseViewers),
			EgressBytes:   baseEgress,
		}
	}

	edges := make([]*model.TenantAnalyticsDailyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.TenantAnalyticsDailyEdge{
			Cursor: fmt.Sprintf("tad_cursor_%d", i+1),
			Node:   record,
		}
	}

	return &model.TenantAnalyticsDailyConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
			StartCursor:     stringPtr("tad_cursor_1"),
			EndCursor:       stringPtr("tad_cursor_7"),
		},
		TotalCount: len(records),
	}
}

// GenerateStreamAnalyticsDailyConnection creates demo stream daily analytics data
func GenerateStreamAnalyticsDailyConnection(internalName *string) *model.StreamAnalyticsDailyConnection {
	now := time.Now()

	streamNames := []string{"demo_live_stream_001", "demo_live_stream_002", "demo_live_stream_003"}
	if internalName != nil && *internalName != "" {
		streamNames = []string{*internalName}
	}

	// Generate 7 days × N streams
	var records []*pb.StreamAnalyticsDaily
	for _, stream := range streamNames {
		for i := 0; i < 7; i++ {
			day := now.AddDate(0, 0, -i).Truncate(24 * time.Hour)

			// Simulate varying activity levels per stream
			baseViews := 100 + rand.Intn(1000)
			baseViewers := 20 + rand.Intn(200)
			baseCountries := 3 + rand.Intn(10)
			baseCities := 10 + rand.Intn(30)
			baseEgress := int64(1_000_000_000 + rand.Intn(10_000_000_000)) // 1-11 GB

			records = append(records, &pb.StreamAnalyticsDaily{
				Id:              fmt.Sprintf("sad_%s_%s", stream, day.Format("2006-01-02")),
				Day:             timestamppb.New(day),
				InternalName:    stream,
				TotalViews:      int64(baseViews),
				UniqueViewers:   int32(baseViewers),
				UniqueCountries: int32(baseCountries),
				UniqueCities:    int32(baseCities),
				EgressBytes:     baseEgress,
			})
		}
	}

	edges := make([]*model.StreamAnalyticsDailyEdge, len(records))
	for i, record := range records {
		edges[i] = &model.StreamAnalyticsDailyEdge{
			Cursor: fmt.Sprintf("sad_cursor_%d", i+1),
			Node:   record,
		}
	}

	return &model.StreamAnalyticsDailyConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
			StartCursor:     stringPtr("sad_cursor_1"),
			EndCursor:       stringPtr(fmt.Sprintf("sad_cursor_%d", len(records))),
		},
		TotalCount: len(records),
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
