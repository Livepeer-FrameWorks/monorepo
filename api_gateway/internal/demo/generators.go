package demo

import (
	"time"

	"frameworks/api_gateway/graph/model"
	commodore "frameworks/pkg/api/commodore"
	foghorn "frameworks/pkg/api/foghorn"
	"frameworks/pkg/api/periscope"
	"frameworks/pkg/models"
)

// GenerateStreams creates realistic demo stream data
func GenerateStreams() []*models.Stream {
	now := time.Now()

	return []*models.Stream{
		{
			ID:          "demo_live_stream_001",
			Title:       "Live: FrameWorks Demo Stream",
			Description: "Demonstrating live streaming capabilities",
			StreamKey:   "sk_demo_live_a1b2c3d4e5f6",
			PlaybackID:  "pb_demo_live_x7y8z9",
			Status:      "live",
			IsRecording: true,
			CreatedAt:   now.Add(-2 * time.Hour),
			UpdatedAt:   now.Add(-5 * time.Minute),
		},
		{
			ID:          "demo_offline_stream_002",
			Title:       "Gaming Stream Setup",
			Description: "Getting ready for tonight's gaming session",
			StreamKey:   "sk_demo_offline_f6g7h8i9j0k1",
			PlaybackID:  "pb_demo_offline_m3n4o5",
			Status:      "offline",
			IsRecording: false,
			CreatedAt:   now.Add(-24 * time.Hour),
			UpdatedAt:   now.Add(-1 * time.Hour),
		},
		{
			ID:          "demo_recording_stream_003",
			Title:       "Product Demo Recording",
			Description: "Recording a product demo for the marketing team",
			StreamKey:   "sk_demo_rec_l2m3n4o5p6q7",
			PlaybackID:  "pb_demo_rec_r8s9t0",
			Status:      "recording",
			IsRecording: true,
			CreatedAt:   now.Add(-6 * time.Hour),
			UpdatedAt:   now.Add(-10 * time.Minute),
		},
		{
			ID:          "demo_ended_stream_004",
			Title:       "Weekly Team Standup",
			Description: "Our weekly development team standup meeting",
			StreamKey:   "sk_demo_ended_u1v2w3x4y5z6",
			PlaybackID:  "pb_demo_ended_a7b8c9",
			Status:      "ended",
			IsRecording: false,
			CreatedAt:   now.Add(-48 * time.Hour),
			UpdatedAt:   now.Add(-36 * time.Hour),
		},
	}
}

// GenerateStreamAnalytics creates realistic analytics data
func GenerateStreamAnalytics(streamID string) *models.StreamAnalytics {
	now := time.Now()

	startTime := now.Add(-2 * time.Hour)
	endTime := now

	return &models.StreamAnalytics{
		StreamID:             streamID,
		TotalConnections:     1247,
		PeakViewers:          89,
		AvgViewers:           34.7,
		TotalSessionDuration: 18650, // seconds
		UniqueCountries:      12,
		UniqueCities:         28,
		SessionStartTime:     &startTime,
		SessionEndTime:       &endTime,
	}
}

// GenerateViewerMetrics creates demo viewer metrics
func GenerateViewerMetrics() []*models.AnalyticsViewerSession {
	now := time.Now()
	metrics := make([]*models.AnalyticsViewerSession, 20)

	// Simulate viewer count over last 20 intervals (5 min each)
	viewerCounts := []int{12, 15, 23, 34, 45, 67, 89, 85, 78, 92, 87, 76, 65, 58, 71, 69, 54, 47, 39, 31}

	for i, count := range viewerCounts {
		metrics[i] = &models.AnalyticsViewerSession{
			Timestamp:   now.Add(-time.Duration(19-i) * 5 * time.Minute),
			ViewerCount: count,
		}
	}

	return metrics
}

// GenerateBillingTiers creates demo billing tier data
func GenerateBillingTiers() []*models.BillingTier {
	return []*models.BillingTier{
		{
			ID:          "tier_demo_starter",
			DisplayName: "Starter",
			Description: "Perfect for trying out FrameWorks",
			BasePrice:   0.00,
			Currency:    "USD",
			Features: models.BillingFeatures{
				MaxStreams:   models.NewResourceLimit(1),
				MaxViewers:   models.NewResourceLimit(10),
				Recording:    false,
				Analytics:    true,
				SupportLevel: "community",
			},
		},
		{
			ID:          "tier_demo_pro",
			DisplayName: "Professional",
			Description: "For content creators and small businesses",
			BasePrice:   29.99,
			Currency:    "USD",
			Features: models.BillingFeatures{
				MaxStreams:     models.NewResourceLimit(5),
				MaxViewers:     models.NewResourceLimit(100),
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				SupportLevel:   "email",
			},
		},
		{
			ID:          "tier_demo_enterprise",
			DisplayName: "Enterprise",
			Description: "For large organizations with custom needs",
			BasePrice:   299.99,
			Currency:    "USD",
			Features: models.BillingFeatures{
				MaxStreams:     models.NewUnlimitedResource(),
				MaxViewers:     models.NewUnlimitedResource(),
				Recording:      true,
				Analytics:      true,
				CustomBranding: true,
				APIAccess:      true,
				SupportLevel:   "phone",
				SLA:            true,
			},
		},
	}
}

// GenerateInvoices creates demo invoice data
func GenerateInvoices() []*models.Invoice {
	now := time.Now()

	return []*models.Invoice{
		{
			ID:        "inv_demo_current_001",
			Amount:    29.99,
			Currency:  "USD",
			Status:    "paid",
			DueDate:   now.Add(24 * time.Hour),
			CreatedAt: now.Add(-30 * 24 * time.Hour),
			UsageDetails: models.UsageDetails{
				"Streaming hours": {
					Quantity:  42.5,
					UnitPrice: 0.50,
					Unit:      "hours",
				},
				"Storage GB": {
					Quantity:  15.2,
					UnitPrice: 0.25,
					Unit:      "GB",
				},
			},
		},
		{
			ID:        "inv_demo_previous_002",
			Amount:    24.50,
			Currency:  "USD",
			Status:    "paid",
			DueDate:   now.Add(-30 * 24 * time.Hour),
			CreatedAt: now.Add(-60 * 24 * time.Hour),
			UsageDetails: models.UsageDetails{
				"Streaming hours": {
					Quantity:  35.0,
					UnitPrice: 0.50,
					Unit:      "hours",
				},
				"Storage GB": {
					Quantity:  19.0,
					UnitPrice: 0.25,
					Unit:      "GB",
				},
			},
		},
	}
}

// GenerateBillingStatus creates demo billing status
func GenerateBillingStatus() *models.BillingStatus {
	tiers := GenerateBillingTiers()
	nextBilling := time.Now().Add(18 * 24 * time.Hour)

	return &models.BillingStatus{
		TenantID:        "demo_tenant_frameworks",
		Tier:            *tiers[1], // Professional tier
		Status:          "active",
		NextBillingDate: &nextBilling,
		PendingInvoices: []models.Invoice{}, // No pending invoices
		RecentPayments:  []models.Payment{},
	}
}

// GenerateUsageRecords creates demo usage records
func GenerateUsageRecords() []*models.UsageRecord {
	now := time.Now()
	records := make([]*models.UsageRecord, 10)

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

		records[i] = &models.UsageRecord{
			ID:         "usage_demo_" + time.Now().Format("20060102") + "_" + string(rune(i+'a')),
			UsageType:  usageType,
			UsageValue: value,
			CreatedAt:  now.Add(-time.Duration(i) * 24 * time.Hour),
			UsageDetails: models.UsageDetails{
				"cost": {
					Quantity:  value,
					UnitPrice: 0.5,
					Unit:      "units",
				},
			},
		}
	}

	return records
}

// GenerateDeveloperTokens creates demo API tokens
func GenerateDeveloperTokens() []*models.APIToken {
	now := time.Now()

	// Helper function to create time pointers
	timePtr := func(t time.Time) *time.Time { return &t }

	return []*models.APIToken{
		{
			ID:          "token_demo_production",
			TokenName:   "Production API Access",
			TokenValue:  "", // never expose in list
			Permissions: []string{"streams:read", "streams:write", "analytics:read"},
			IsActive:    true,
			LastUsedAt:  timePtr(now.Add(-2 * time.Hour)),
			ExpiresAt:   timePtr(now.Add(365 * 24 * time.Hour)),
			CreatedAt:   now.Add(-60 * 24 * time.Hour),
		},
		{
			ID:          "token_demo_readonly",
			TokenName:   "Analytics Dashboard",
			TokenValue:  "", // never expose in list
			Permissions: []string{"analytics:read", "streams:read"},
			IsActive:    true,
			LastUsedAt:  timePtr(now.Add(-30 * time.Minute)),
			ExpiresAt:   nil, // No expiration
			CreatedAt:   now.Add(-30 * 24 * time.Hour),
		},
		{
			ID:          "token_demo_revoked",
			TokenName:   "Old Integration Token",
			TokenValue:  "", // never expose in list
			Permissions: []string{"streams:read", "streams:write"},
			IsActive:    false,
			LastUsedAt:  timePtr(now.Add(-10 * 24 * time.Hour)),
			ExpiresAt:   timePtr(now.Add(30 * 24 * time.Hour)),
			CreatedAt:   now.Add(-90 * 24 * time.Hour),
		},
	}
}

// GenerateTenant creates demo tenant data
func GenerateTenant() *models.Tenant {
	return &models.Tenant{
		ID:             "demo_tenant_frameworks",
		Name:           "FrameWorks Demo Organization",
		PrimaryColor:   "#3B82F6", // Blue
		SecondaryColor: "#1F2937", // Dark gray
		CreatedAt:      time.Now().Add(-180 * 24 * time.Hour),
	}
}

// GeneratePlatformOverview creates demo platform metrics
func GeneratePlatformOverview() *periscope.PlatformOverviewResponse {
	now := time.Now()

	return &periscope.PlatformOverviewResponse{
		TenantID:       "demo_tenant_frameworks",
		TotalUsers:     156,
		ActiveUsers:    120,
		TotalStreams:   42,
		ActiveStreams:  7,
		TotalViewers:   1247,
		AverageViewers: 54.2,
		PeakBandwidth:  850.5,
		GeneratedAt:    now,
	}
}

// GenerateStreamEvents creates demo stream events for subscription
func GenerateStreamEvents() []*periscope.StreamEvent {
	return []*periscope.StreamEvent{
		{
			Timestamp: time.Now(),
			EventID:   "evt_stream_start",
			EventType: "STREAM_START",
			Status:    "LIVE",
			NodeID:    "node_demo_us_west_01",
			EventData: "{\"internal_name\":\"demo_live_stream_001\"}",
		},
		{
			Timestamp: time.Now().Add(30 * time.Second),
			EventID:   "evt_buffer_update",
			EventType: "BUFFER_UPDATE",
			Status:    "LIVE",
			NodeID:    "node_demo_us_west_01",
			EventData: "{\"buffer_health\":95}",
		},
	}
}

// GenerateViewerMetricsEvents creates demo viewer metrics for subscription
func GenerateViewerMetricsEvents() []*model.ViewerMetrics {
	return []*model.ViewerMetrics{
		{
			Stream:            "demo_live_stream_001",
			CurrentViewers:    45,
			PeakViewers:       89,
			Bandwidth:         25.6,
			ConnectionQuality: func() *float64 { f := 0.95; return &f }(),
			BufferHealth:      func() *float64 { f := 0.98; return &f }(),
			Timestamp:         time.Now(),
		},
		{
			Stream:            "demo_live_stream_001",
			CurrentViewers:    52,
			PeakViewers:       89,
			Bandwidth:         28.1,
			ConnectionQuality: func() *float64 { f := 0.97; return &f }(),
			BufferHealth:      func() *float64 { f := 0.96; return &f }(),
			Timestamp:         time.Now().Add(30 * time.Second),
		},
	}
}

// GenerateTrackListEvents creates demo track list events for subscription
func GenerateTrackListEvents() []*periscope.AnalyticsTrackListEvent {
	return []*periscope.AnalyticsTrackListEvent{
		{
			Timestamp: time.Now(),
			NodeID:    "node_demo_us_west_01",
			Stream:    "demo_live_stream_001",
			TrackList: "Track 1: Intro Music\nTrack 2: Main Content\nTrack 3: Q&A Session",
			Tracks: []periscope.StreamTrack{
				{
					TrackName:   "video_main",
					TrackType:   "video",
					Codec:       "H264",
					BitrateKbps: intPtr(2500),
					Width:       intPtr(1920),
					Height:      intPtr(1080),
					FPS:         float64Ptr(30.0),
				},
				{
					TrackName:   "audio_main",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: intPtr(128),
					Channels:    intPtr(2),
					SampleRate:  intPtr(48000),
				},
			},
			TrackCount: 3,
		},
		{
			Timestamp: time.Now().Add(60 * time.Second),
			NodeID:    "node_demo_us_west_01",
			Stream:    "demo_live_stream_001",
			TrackList: "Track 1: Intro Music\nTrack 2: Main Content\nTrack 3: Q&A Session\nTrack 4: Closing Remarks",
			Tracks: []periscope.StreamTrack{
				{
					TrackName:   "video_main",
					TrackType:   "video",
					Codec:       "H264",
					BitrateKbps: intPtr(2400),
					Width:       intPtr(1920),
					Height:      intPtr(1080),
					FPS:         float64Ptr(29.8),
				},
				{
					TrackName:   "audio_main",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: intPtr(128),
					Channels:    intPtr(2),
					SampleRate:  intPtr(48000),
				},
				{
					TrackName:   "audio_spanish",
					TrackType:   "audio",
					Codec:       "AAC",
					BitrateKbps: intPtr(96),
					Channels:    intPtr(2),
					SampleRate:  intPtr(44100),
				},
			},
			TrackCount: 4,
		},
	}
}

// GenerateSystemHealthEvents creates demo system health events for subscription
func GenerateSystemHealthEvents() []*model.SystemHealthEvent {
	return []*model.SystemHealthEvent{
		{
			Node:        "node_demo_us_west_01",
			Cluster:     "cluster_demo_us_west",
			Status:      "HEALTHY",
			CPUUsage:    65.2,
			MemoryUsage: 78.5,
			DiskUsage:   45.3,
			HealthScore: 0.95,
			Timestamp:   time.Now(),
		},
		{
			Node:        "node_demo_us_west_02",
			Cluster:     "cluster_demo_us_west",
			Status:      "HEALTHY",
			CPUUsage:    72.1,
			MemoryUsage: 82.3,
			DiskUsage:   38.7,
			HealthScore: 0.92,
			Timestamp:   time.Now().Add(15 * time.Second),
		},
	}
}

// GenerateStreamHealthMetrics creates demo stream health metrics
func GenerateStreamHealthMetrics() []*periscope.StreamHealthMetric {
	now := time.Now()

	videoBitrate := 2500
	videoWidth := 1920
	videoHeight := 1080
	videoFPS := 30.0
	videoResolution := "1920x1080"
	audioBitrate := 128
	audioChannels := 2
	audioSampleRate := 48000
	tracks := []periscope.StreamTrack{
		{
			TrackName:   "video_main",
			TrackType:   "video",
			Codec:       "H264",
			BitrateKbps: intPtr(videoBitrate),
			Width:       intPtr(videoWidth),
			Height:      intPtr(videoHeight),
			FPS:         float64Ptr(videoFPS),
			Resolution:  stringPtr(videoResolution),
		},
		{
			TrackName:   "audio_main",
			TrackType:   "audio",
			Codec:       "AAC",
			BitrateKbps: intPtr(audioBitrate),
			Channels:    intPtr(audioChannels),
			SampleRate:  intPtr(audioSampleRate),
		},
	}

	tracksDegraded := []periscope.StreamTrack{
		{
			TrackName:   "video_main",
			TrackType:   "video",
			Codec:       "H264",
			BitrateKbps: intPtr(2200),
			Width:       intPtr(videoWidth),
			Height:      intPtr(videoHeight),
			FPS:         float64Ptr(29.8),
			Resolution:  stringPtr(videoResolution),
		},
		{
			TrackName:   "audio_main",
			TrackType:   "audio",
			Codec:       "AAC",
			BitrateKbps: intPtr(audioBitrate),
			Channels:    intPtr(audioChannels),
			SampleRate:  intPtr(audioSampleRate),
		},
	}

	healthScore1 := 0.94
	healthScore2 := 0.81
	packetLoss1 := 0.08
	packetLoss2 := 0.32
	frameJitter1 := 18.5
	frameJitter2 := 42.7
	qualityTierFull := stringPtr("1080p30")
	qualityTierRecover := stringPtr("1080p30")

	return []*periscope.StreamHealthMetric{
		{
			Timestamp:              now.Add(-5 * time.Minute),
			InternalName:           "demo_live_stream_001",
			TenantID:               "demo_tenant_frameworks",
			NodeID:                 "node_demo_us_west_01",
			Bitrate:                2500000,
			FPS:                    30,
			GOPSize:                60,
			Width:                  1920,
			Height:                 1080,
			BufferHealth:           0.98,
			BufferState:            "FULL",
			PacketsSent:            15420,
			PacketsLost:            12,
			Codec:                  "H264",
			Tracks:                 tracks,
			PrimaryAudioChannels:   intPtr(audioChannels),
			PrimaryAudioSampleRate: intPtr(audioSampleRate),
			PrimaryAudioCodec:      stringPtr("AAC"),
			PrimaryAudioBitrate:    intPtr(audioBitrate),
			HealthScore:            float32(healthScore1),
			FrameJitterMs:          float64Ptr(frameJitter1),
			PacketLossPercentage:   float64Ptr(packetLoss1),
			QualityTier:            qualityTierFull,
		},
		{
			Timestamp:              now.Add(-2 * time.Minute),
			InternalName:           "demo_live_stream_001",
			TenantID:               "demo_tenant_frameworks",
			NodeID:                 "node_demo_us_west_01",
			Bitrate:                2400000,
			FPS:                    29,
			GOPSize:                64,
			Width:                  1920,
			Height:                 1080,
			BufferHealth:           0.72,
			BufferState:            "DRY",
			PacketsSent:            15890,
			PacketsLost:            45,
			Codec:                  "H264",
			Tracks:                 tracksDegraded,
			PrimaryAudioChannels:   intPtr(audioChannels),
			PrimaryAudioSampleRate: intPtr(audioSampleRate),
			PrimaryAudioCodec:      stringPtr("AAC"),
			PrimaryAudioBitrate:    intPtr(audioBitrate),
			HealthScore:            float32(healthScore2),
			FrameJitterMs:          float64Ptr(frameJitter2),
			PacketLossPercentage:   float64Ptr(packetLoss2),
			QualityTier:            qualityTierRecover,
			IssuesDescription:      stringPtr("elevated packet loss on edge node"),
		},
	}
}

// GenerateStreamHealthAlerts creates demo stream health alerts
func GenerateStreamHealthAlerts() []*model.StreamHealthAlert {
	now := time.Now()
	return []*model.StreamHealthAlert{
		{
			Timestamp:            now.Add(-3 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			AlertType:            model.AlertTypeHighJitter,
			Severity:             model.AlertSeverityMedium,
			HealthScore:          func() *float64 { h := 0.75; return &h }(),
			FrameJitterMs:        func() *float64 { f := 45.2; return &f }(),
			PacketLossPercentage: func() *float64 { p := 0.15; return &p }(),
			IssuesDescription:    func() *string { i := "High frame jitter detected"; return &i }(),
			BufferState:          func() *model.BufferState { b := model.BufferStateEmpty; return &b }(),
			QualityTier:          func() *string { q := "1080p30"; return &q }(),
		},
		{
			Timestamp:            now.Add(-1 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			AlertType:            model.AlertTypePacketLoss,
			Severity:             model.AlertSeverityHigh,
			HealthScore:          func() *float64 { h := 0.68; return &h }(),
			FrameJitterMs:        func() *float64 { f := 52.8; return &f }(),
			PacketLossPercentage: func() *float64 { p := 1.2; return &p }(),
			IssuesDescription:    func() *string { i := "Significant packet loss detected"; return &i }(),
			BufferState:          func() *model.BufferState { b := model.BufferStateDry; return &b }(),
			QualityTier:          func() *string { q := "1080p30"; return &q }(),
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
			HealthScore:   func() *float64 { h := 0.65; return &h }(),
			FrameJitterMs: func() *float64 { f := 58.3; return &f }(),
		},
		{
			Timestamp:     now.Add(-6 * time.Minute),
			Stream:        "demo_live_stream_001",
			NodeID:        "node_demo_us_west_01",
			BufferState:   model.BufferStateRecover,
			PreviousState: model.BufferStateDry,
			RebufferStart: false,
			RebufferEnd:   true,
			HealthScore:   func() *float64 { h := 0.78; return &h }(),
			FrameJitterMs: func() *float64 { f := 35.2; return &f }(),
		},
	}
}

// GenerateViewerGeographics creates realistic demo viewer geographic data
func GenerateViewerGeographics() []*periscope.ConnectionEvent {
	now := time.Now()

	return []*periscope.ConnectionEvent{
		{
			EventID:        "evt_demo_1",
			Timestamp:      now.Add(-30 * time.Minute),
			TenantID:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionID:      "sess_demo_1",
			ConnectionAddr: "192.168.1.100",
			Connector:      "webrtc",
			NodeID:         "node_demo_us_west_01",
			CountryCode:    "US",
			City:           "San Francisco",
			Latitude:       37.7749,
			Longitude:      -122.4194,
			EventType:      "connect",
		},
		{
			EventID:        "evt_demo_2",
			Timestamp:      now.Add(-25 * time.Minute),
			TenantID:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionID:      "sess_demo_2",
			ConnectionAddr: "203.0.113.45",
			Connector:      "webrtc",
			NodeID:         "node_demo_eu_west_01",
			CountryCode:    "GB",
			City:           "London",
			Latitude:       51.5074,
			Longitude:      -0.1278,
			EventType:      "connect",
		},
		{
			EventID:        "evt_demo_3",
			Timestamp:      now.Add(-20 * time.Minute),
			TenantID:       "demo_tenant_frameworks",
			InternalName:   "demo_live_stream_001",
			SessionID:      "sess_demo_3",
			ConnectionAddr: "198.51.100.78",
			Connector:      "webrtc",
			NodeID:         "node_demo_ap_east_01",
			CountryCode:    "JP",
			City:           "Tokyo",
			Latitude:       35.6762,
			Longitude:      139.6503,
			EventType:      "connect",
		},
	}
}

// GenerateGeographicDistribution creates realistic demo geographic distribution data
func GenerateGeographicDistribution() *model.GeographicDistribution {
	now := time.Now()

	return &model.GeographicDistribution{
		TimeRange: &model.TimeRange{
			Start: now.Add(-24 * time.Hour),
			End:   now,
		},
		Stream:          func() *string { s := "demo_live_stream_001"; return &s }(),
		UniqueCountries: 5,
		UniqueCities:    8,
		TotalViewers:    1247,
		TopCountries: []*model.CountryMetric{
			{
				CountryCode: "US",
				ViewerCount: 623,
				Percentage:  49.9,
				Cities: []*model.CityMetric{
					{
						City:        "San Francisco",
						CountryCode: func() *string { s := "US"; return &s }(),
						ViewerCount: 234,
						Percentage:  18.8,
						Latitude:    func() *float64 { f := 37.7749; return &f }(),
						Longitude:   func() *float64 { f := -122.4194; return &f }(),
					},
					{
						City:        "New York",
						CountryCode: func() *string { s := "US"; return &s }(),
						ViewerCount: 189,
						Percentage:  15.2,
						Latitude:    func() *float64 { f := 40.7128; return &f }(),
						Longitude:   func() *float64 { f := -74.0060; return &f }(),
					},
				},
			},
			{
				CountryCode: "GB",
				ViewerCount: 298,
				Percentage:  23.9,
				Cities: []*model.CityMetric{
					{
						City:        "London",
						CountryCode: func() *string { s := "GB"; return &s }(),
						ViewerCount: 201,
						Percentage:  16.1,
						Latitude:    func() *float64 { f := 51.5074; return &f }(),
						Longitude:   func() *float64 { f := -0.1278; return &f }(),
					},
				},
			},
			{
				CountryCode: "JP",
				ViewerCount: 187,
				Percentage:  15.0,
				Cities: []*model.CityMetric{
					{
						City:        "Tokyo",
						CountryCode: func() *string { s := "JP"; return &s }(),
						ViewerCount: 123,
						Percentage:  9.9,
						Latitude:    func() *float64 { f := 35.6762; return &f }(),
						Longitude:   func() *float64 { f := 139.6503; return &f }(),
					},
				},
			},
		},
		TopCities: []*model.CityMetric{
			{
				City:        "San Francisco",
				CountryCode: func() *string { s := "US"; return &s }(),
				ViewerCount: 234,
				Percentage:  18.8,
				Latitude:    func() *float64 { f := 37.7749; return &f }(),
				Longitude:   func() *float64 { f := -122.4194; return &f }(),
			},
			{
				City:        "London",
				CountryCode: func() *string { s := "GB"; return &s }(),
				ViewerCount: 201,
				Percentage:  16.1,
				Latitude:    func() *float64 { f := 51.5074; return &f }(),
				Longitude:   func() *float64 { f := -0.1278; return &f }(),
			},
			{
				City:        "New York",
				CountryCode: func() *string { s := "US"; return &s }(),
				ViewerCount: 189,
				Percentage:  15.2,
				Latitude:    func() *float64 { f := 40.7128; return &f }(),
				Longitude:   func() *float64 { f := -74.0060; return &f }(),
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
func GenerateLoadBalancingMetrics() []*model.LoadBalancingMetric {
	now := time.Now()

	return []*model.LoadBalancingMetric{
		{
			Timestamp:       now.Add(-30 * time.Minute),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "node_demo_us_west_01",
			NodeID:          func() *string { s := "node_demo_us_west_01"; return &s }(),
			ClientIP:        func() *string { s := "192.168.1.100"; return &s }(),
			ClientCountry:   func() *string { s := "US"; return &s }(),
			ClientLatitude:  func() *float64 { f := 37.7749; return &f }(),
			ClientLongitude: func() *float64 { f := -122.4194; return &f }(),
			NodeLatitude:    func() *float64 { f := 37.4419; return &f }(),
			NodeLongitude:   func() *float64 { f := -122.1430; return &f }(),
			Score:           func() *int { s := 2850; return &s }(),
			Status:          "success",
			Details:         func() *string { s := "optimal_routing"; return &s }(),
			RoutingDistance: func() *float64 { f := 42.3; return &f }(), // km
			EventType:       func() *string { s := "load_balancing"; return &s }(),
			Source:          func() *string { s := "foghorn"; return &s }(),
		},
		{
			Timestamp:       now.Add(-25 * time.Minute),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "node_demo_eu_west_01",
			NodeID:          func() *string { s := "node_demo_eu_west_01"; return &s }(),
			ClientIP:        func() *string { s := "203.0.113.45"; return &s }(),
			ClientCountry:   func() *string { s := "GB"; return &s }(),
			ClientLatitude:  func() *float64 { f := 51.5074; return &f }(),
			ClientLongitude: func() *float64 { f := -0.1278; return &f }(),
			NodeLatitude:    func() *float64 { f := 51.4994; return &f }(),
			NodeLongitude:   func() *float64 { f := -0.1270; return &f }(),
			Score:           func() *int { s := 3100; return &s }(),
			Status:          "success",
			Details:         func() *string { s := "regional_optimal"; return &s }(),
			RoutingDistance: func() *float64 { f := 1.2; return &f }(), // km
			EventType:       func() *string { s := "load_balancing"; return &s }(),
			Source:          func() *string { s := "foghorn"; return &s }(),
		},
		{
			Timestamp:       now.Add(-20 * time.Minute),
			Stream:          "demo_live_stream_001",
			SelectedNode:    "node_demo_ap_east_01",
			NodeID:          func() *string { s := "node_demo_ap_east_01"; return &s }(),
			ClientIP:        func() *string { s := "198.51.100.78"; return &s }(),
			ClientCountry:   func() *string { s := "JP"; return &s }(),
			ClientLatitude:  func() *float64 { f := 35.6762; return &f }(),
			ClientLongitude: func() *float64 { f := 139.6503; return &f }(),
			NodeLatitude:    func() *float64 { f := 35.6804; return &f }(),
			NodeLongitude:   func() *float64 { f := 139.7690; return &f }(),
			Score:           func() *int { s := 2950; return &s }(),
			Status:          "success",
			Details:         func() *string { s := "ap_regional"; return &s }(),
			RoutingDistance: func() *float64 { f := 13.8; return &f }(), // km
			EventType:       func() *string { s := "load-balancing"; return &s }(),
			Source:          func() *string { s := "foghorn"; return &s }(),
		},
	}
}

// GenerateViewerEndpointResponse returns a demo viewer endpoint resolution payload
func GenerateViewerEndpointResponse(contentType, contentID string) *commodore.ViewerEndpointResponse {
	if contentType == "" {
		contentType = "live"
	}
	if contentID == "" {
		contentID = "demo_live_stream_001"
	}

	primaryOutputs := map[string]foghorn.OutputEndpoint{
		"WHEP": {
			Protocol: "WHEP",
			URL:      "https://edge.demo.frameworks.video/whep/demo_live_stream_001",
			Capabilities: foghorn.OutputCapability{
				SupportsSeek:          false,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
		"HLS": {
			Protocol: "HLS",
			URL:      "https://edge.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
			Capabilities: foghorn.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
	}

	fallbackOutputs := map[string]foghorn.OutputEndpoint{
		"HLS": {
			Protocol: "HLS",
			URL:      "https://edge.eu.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
			Capabilities: foghorn.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: true,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"H264", "AAC"},
			},
		},
		"HTTP": {
			Protocol: "HTTP",
			URL:      "https://edge.eu.demo.frameworks.video/http/demo_live_stream_001",
			Capabilities: foghorn.OutputCapability{
				SupportsSeek:          true,
				SupportsQualitySwitch: false,
				HasAudio:              true,
				HasVideo:              true,
				Codecs:                []string{"MP4"},
			},
		},
	}

	primary := foghorn.ViewerEndpoint{
		NodeID:      "node_demo_us_west_01",
		BaseURL:     "https://edge.demo.frameworks.video",
		Protocol:    "webrtc",
		URL:         "https://edge.demo.frameworks.video/webrtc/demo_live_stream_001",
		GeoDistance: 18.4,
		LoadScore:   0.72,
		HealthScore: 0.94,
		Outputs:     primaryOutputs,
	}

	fallback := foghorn.ViewerEndpoint{
		NodeID:      "node_demo_eu_west_01",
		BaseURL:     "https://edge.eu.demo.frameworks.video",
		Protocol:    "hls",
		URL:         "https://edge.eu.demo.frameworks.video/hls/demo_live_stream_001.m3u8",
		GeoDistance: 4567.0,
		LoadScore:   0.81,
		HealthScore: 0.9,
		Outputs:     fallbackOutputs,
	}

	now := time.Now()
	metadata := &foghorn.PlaybackMetadata{
		Status:        "live",
		IsLive:        true,
		Viewers:       132,
		BufferState:   "FULL",
		HealthScore:   float64Ptr(0.93),
		TenantID:      "demo_tenant_frameworks",
		ContentID:     contentID,
		ContentType:   contentType,
		ProtocolHints: []string{"WHEP", "HLS", "HTTP"},
		Tracks: []foghorn.PlaybackTrack{
			{Type: "video", Codec: "H264", BitrateKbps: 2500, Width: 1920, Height: 1080},
			{Type: "audio", Codec: "AAC", BitrateKbps: 128, Channels: 2, SampleRate: 48000},
		},
		Instances: []foghorn.PlaybackInstance{
			{
				NodeID:           "node_demo_us_west_01",
				Viewers:          78,
				BufferState:      "FULL",
				BytesUp:          3_456_789,
				BytesDown:        5_678_901,
				TotalConnections: 120,
				Inputs:           1,
				LastUpdate:       now.Add(-25 * time.Second),
			},
			{
				NodeID:           "node_demo_eu_west_01",
				Viewers:          54,
				BufferState:      "RECOVER",
				BytesUp:          2_345_678,
				BytesDown:        4_321_987,
				TotalConnections: 96,
				Inputs:           1,
				LastUpdate:       now.Add(-40 * time.Second),
			},
		},
		CreatedAt:       &now,
		DurationSeconds: intPtr(0),
	}

	return &commodore.ViewerEndpointResponse{
		Primary:   primary,
		Fallbacks: []foghorn.ViewerEndpoint{fallback},
		Metadata:  metadata,
	}
}

func intPtr(v int) *int             { return &v }
func int64Ptr(v int64) *int64       { return &v }
func float64Ptr(v float64) *float64 { return &v }
func stringPtr(s string) *string    { return &s }
func boolPtr(v bool) *bool          { return &v }
