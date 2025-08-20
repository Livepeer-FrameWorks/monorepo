package demo

import (
	"time"

	"frameworks/api_gateway/graph/model"
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

	return &models.StreamAnalytics{
		StreamID:             streamID,
		TotalConnections:     1247,
		PeakViewers:          89,
		AvgViewers:           34.7,
		TotalSessionDuration: 18650, // seconds
		UniqueCountries:      12,
		UniqueCities:         28,
		SessionStartTime:     now.Add(-2 * time.Hour),
		SessionEndTime:       now,
	}
}

// GenerateViewerMetrics creates demo viewer metrics
func GenerateViewerMetrics() []*model.ViewerMetric {
	now := time.Now()
	metrics := make([]*model.ViewerMetric, 20)

	// Simulate viewer count over last 20 intervals (5 min each)
	viewerCounts := []int{12, 15, 23, 34, 45, 67, 89, 85, 78, 92, 87, 76, 65, 58, 71, 69, 54, 47, 39, 31}

	for i, count := range viewerCounts {
		metrics[i] = &model.ViewerMetric{
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
			Features: map[string]interface{}{
				"max_streams":   1,
				"max_viewers":   10,
				"recording":     false,
				"analytics":     true,
				"support_level": "community",
			},
		},
		{
			ID:          "tier_demo_pro",
			DisplayName: "Professional",
			Description: "For content creators and small businesses",
			BasePrice:   29.99,
			Currency:    "USD",
			Features: map[string]interface{}{
				"max_streams":     5,
				"max_viewers":     100,
				"recording":       true,
				"analytics":       true,
				"custom_branding": true,
				"support_level":   "email",
			},
		},
		{
			ID:          "tier_demo_enterprise",
			DisplayName: "Enterprise",
			Description: "For large organizations with custom needs",
			BasePrice:   299.99,
			Currency:    "USD",
			Features: map[string]interface{}{
				"max_streams":     "unlimited",
				"max_viewers":     "unlimited",
				"recording":       true,
				"analytics":       true,
				"custom_branding": true,
				"api_access":      true,
				"support_level":   "phone",
				"sla":             true,
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
			UsageDetails: map[string]interface{}{
				"Streaming hours": map[string]interface{}{
					"quantity":   42.5,
					"unit_price": 0.50,
				},
				"Storage GB": map[string]interface{}{
					"quantity":   15.2,
					"unit_price": 0.25,
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
			UsageDetails: map[string]interface{}{
				"Streaming hours": map[string]interface{}{
					"quantity":   35.0,
					"unit_price": 0.50,
				},
				"Storage GB": map[string]interface{}{
					"quantity":   19.0,
					"unit_price": 0.25,
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
			UsageDetails: map[string]interface{}{
				"cost": value * 0.5,
			},
		}
	}

	return records
}

// GenerateDeveloperTokens creates demo API tokens
func GenerateDeveloperTokens() []*model.DeveloperToken {
	now := time.Now()

	// Helper function to create time pointers
	timePtr := func(t time.Time) *time.Time { return &t }
	tokenPtr := func(s string) *string { return &s }

	return []*model.DeveloperToken{
		{
			ID:          "token_demo_production",
			Name:        "Production API Access",
			Token:       tokenPtr("***********************************uc12"), // Masked
			Permissions: "streams:read,streams:write,analytics:read",
			Status:      "active",
			LastUsedAt:  timePtr(now.Add(-2 * time.Hour)),
			ExpiresAt:   timePtr(now.Add(365 * 24 * time.Hour)),
			CreatedAt:   now.Add(-60 * 24 * time.Hour),
		},
		{
			ID:          "token_demo_readonly",
			Name:        "Analytics Dashboard",
			Token:       tokenPtr("***********************************kl89"), // Masked
			Permissions: "analytics:read,streams:read",
			Status:      "active",
			LastUsedAt:  timePtr(now.Add(-30 * time.Minute)),
			ExpiresAt:   nil, // No expiration
			CreatedAt:   now.Add(-30 * 24 * time.Hour),
		},
		{
			ID:          "token_demo_revoked",
			Name:        "Old Integration Token",
			Token:       tokenPtr("***********************************xy56"), // Masked
			Permissions: "streams:read,streams:write",
			Status:      "revoked",
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
func GeneratePlatformOverview() *model.PlatformOverview {
	now := time.Now()

	return &model.PlatformOverview{
		TotalStreams:   42,
		TotalViewers:   1247,
		TotalBandwidth: 850.5, // Gbps
		TotalUsers:     156,
		TimeRange: &model.TimeRange{
			Start: now.Add(-24 * time.Hour),
			End:   now,
		},
	}
}

// GenerateStreamEvents creates demo stream events for subscription
func GenerateStreamEvents() []*model.StreamEvent {
	return []*model.StreamEvent{
		{
			Type:      "STREAM_START",
			Stream:    "demo_live_stream_001",
			Status:    "LIVE",
			Timestamp: time.Now(),
			Details:   func() *string { s := "Stream started successfully"; return &s }(),
		},
		{
			Type:      "BUFFER_UPDATE",
			Stream:    "demo_live_stream_001",
			Status:    "LIVE",
			Timestamp: time.Now().Add(30 * time.Second),
			Details:   func() *string { s := "Buffer health: 95%"; return &s }(),
		},
		{
			Type:      "TRACK_LIST_UPDATE",
			Stream:    "demo_live_stream_001",
			Status:    "LIVE",
			Timestamp: time.Now().Add(60 * time.Second),
			Details:   func() *string { s := "New track added to playlist"; return &s }(),
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
func GenerateTrackListEvents() []*model.TrackListEvent {
	return []*model.TrackListEvent{
		{
			Stream:     "demo_live_stream_001",
			TrackList:  "Track 1: Intro Music\nTrack 2: Main Content\nTrack 3: Q&A Session",
			TrackCount: 3,
			Timestamp:  time.Now(),
		},
		{
			Stream:     "demo_live_stream_001",
			TrackList:  "Track 1: Intro Music\nTrack 2: Main Content\nTrack 3: Q&A Session\nTrack 4: Closing Remarks",
			TrackCount: 4,
			Timestamp:  time.Now().Add(60 * time.Second),
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
func GenerateStreamHealthMetrics() []*model.StreamHealthMetric {
	now := time.Now()
	return []*model.StreamHealthMetric{
		{
			Timestamp:            now.Add(-5 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			HealthScore:          0.95,
			FrameJitterMs:        func() *float64 { f := 12.5; return &f }(),
			KeyframeStabilityMs:  func() *float64 { f := 2000.0; return &f }(),
			IssuesDescription:    func() *string { s := "Stream performing well"; return &s }(),
			HasIssues:            false,
			Bitrate:              func() *int { b := 2500000; return &b }(),
			Fps:                  func() *float64 { f := 30.0; return &f }(),
			Width:                func() *int { w := 1920; return &w }(),
			Height:               func() *int { h := 1080; return &h }(),
			Codec:                func() *string { c := "H264"; return &c }(),
			QualityTier:          func() *string { q := "1080p30"; return &q }(),
			PacketsSent:          func() *int { p := 15420; return &p }(),
			PacketsLost:          func() *int { p := 12; return &p }(),
			PacketLossPercentage: func() *float64 { p := 0.08; return &p }(),
			BufferState:          model.BufferStateFull,
			BufferHealth:         func() *float64 { b := 0.98; return &b }(),
			AudioChannels:        func() *int { a := 2; return &a }(),
			AudioSampleRate:      func() *int { a := 44100; return &a }(),
			AudioCodec:           func() *string { a := "AAC"; return &a }(),
			AudioBitrate:         func() *int { a := 128; return &a }(),
		},
		{
			Timestamp:            now.Add(-2 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			HealthScore:          0.87,
			FrameJitterMs:        func() *float64 { f := 25.3; return &f }(),
			KeyframeStabilityMs:  func() *float64 { f := 2100.0; return &f }(),
			IssuesDescription:    func() *string { s := "Minor jitter detected"; return &s }(),
			HasIssues:            true,
			Bitrate:              func() *int { b := 2400000; return &b }(),
			Fps:                  func() *float64 { f := 29.8; return &f }(),
			Width:                func() *int { w := 1920; return &w }(),
			Height:               func() *int { h := 1080; return &h }(),
			Codec:                func() *string { c := "H264"; return &c }(),
			QualityTier:          func() *string { q := "1080p30"; return &q }(),
			PacketsSent:          func() *int { p := 15890; return &p }(),
			PacketsLost:          func() *int { p := 45; return &p }(),
			PacketLossPercentage: func() *float64 { p := 0.28; return &p }(),
			BufferState:          model.BufferStateEmpty,
			BufferHealth:         func() *float64 { b := 0.82; return &b }(),
			AudioChannels:        func() *int { a := 2; return &a }(),
			AudioSampleRate:      func() *int { a := 44100; return &a }(),
			AudioCodec:           func() *string { a := "AAC"; return &a }(),
			AudioBitrate:         func() *int { a := 128; return &a }(),
		},
	}
}

// GenerateStreamQualityChanges creates demo stream quality changes
func GenerateStreamQualityChanges() []*model.StreamQualityChange {
	now := time.Now()
	return []*model.StreamQualityChange{
		{
			Timestamp:           now.Add(-10 * time.Minute),
			Stream:              "demo_live_stream_001",
			NodeID:              "node_demo_us_west_01",
			ChangeType:          model.QualityChangeTypeResolutionChange,
			PreviousResolution:  func() *string { r := "720p30"; return &r }(),
			NewResolution:       func() *string { r := "1080p30"; return &r }(),
			PreviousQualityTier: func() *string { q := "HD"; return &q }(),
			NewQualityTier:      func() *string { q := "Full HD"; return &q }(),
		},
		{
			Timestamp:      now.Add(-5 * time.Minute),
			Stream:         "demo_live_stream_001",
			NodeID:         "node_demo_us_west_01",
			ChangeType:     model.QualityChangeTypeTrackUpdate,
			PreviousTracks: func() *string { t := "Video: H264 720p30"; return &t }(),
			NewTracks:      func() *string { t := "Video: H264 1080p30, Audio: AAC 128k"; return &t }(),
			NewQualityTier: func() *string { q := "Full HD with Audio"; return &q }(),
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
			Timestamp:            now.Add(-8 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			BufferState:          model.BufferStateDry,
			PreviousState:        model.BufferStateFull,
			RebufferStart:        true,
			RebufferEnd:          false,
			HealthScore:          func() *float64 { h := 0.65; return &h }(),
			FrameJitterMs:        func() *float64 { f := 58.3; return &f }(),
			PacketLossPercentage: func() *float64 { p := 2.1; return &p }(),
		},
		{
			Timestamp:            now.Add(-6 * time.Minute),
			Stream:               "demo_live_stream_001",
			NodeID:               "node_demo_us_west_01",
			BufferState:          model.BufferStateRecover,
			PreviousState:        model.BufferStateDry,
			RebufferStart:        false,
			RebufferEnd:          true,
			HealthScore:          func() *float64 { h := 0.78; return &h }(),
			FrameJitterMs:        func() *float64 { f := 35.2; return &f }(),
			PacketLossPercentage: func() *float64 { p := 0.8; return &p }(),
		},
	}
}

// GenerateViewerGeographics creates realistic demo viewer geographic data
func GenerateViewerGeographics() []*model.ViewerGeographic {
	now := time.Now()

	return []*model.ViewerGeographic{
		{
			Timestamp:      now.Add(-30 * time.Minute),
			Stream:         func() *string { s := "demo_live_stream_001"; return &s }(),
			NodeID:         func() *string { s := "node_demo_us_west_01"; return &s }(),
			CountryCode:    func() *string { s := "US"; return &s }(),
			City:           func() *string { s := "San Francisco"; return &s }(),
			Latitude:       func() *float64 { f := 37.7749; return &f }(),
			Longitude:      func() *float64 { f := -122.4194; return &f }(),
			ViewerCount:    func() *int { i := 1; return &i }(),
			ConnectionAddr: func() *string { s := "192.168.1.100"; return &s }(),
			EventType:      func() *string { s := "user_new"; return &s }(),
			Source:         func() *string { s := "mistserver_webhook"; return &s }(),
		},
		{
			Timestamp:      now.Add(-25 * time.Minute),
			Stream:         func() *string { s := "demo_live_stream_001"; return &s }(),
			NodeID:         func() *string { s := "node_demo_eu_west_01"; return &s }(),
			CountryCode:    func() *string { s := "GB"; return &s }(),
			City:           func() *string { s := "London"; return &s }(),
			Latitude:       func() *float64 { f := 51.5074; return &f }(),
			Longitude:      func() *float64 { f := -0.1278; return &f }(),
			ViewerCount:    func() *int { i := 1; return &i }(),
			ConnectionAddr: func() *string { s := "203.0.113.45"; return &s }(),
			EventType:      func() *string { s := "user_new"; return &s }(),
			Source:         func() *string { s := "mistserver_webhook"; return &s }(),
		},
		{
			Timestamp:      now.Add(-20 * time.Minute),
			Stream:         func() *string { s := "demo_live_stream_001"; return &s }(),
			NodeID:         func() *string { s := "node_demo_ap_east_01"; return &s }(),
			CountryCode:    func() *string { s := "JP"; return &s }(),
			City:           func() *string { s := "Tokyo"; return &s }(),
			Latitude:       func() *float64 { f := 35.6762; return &f }(),
			Longitude:      func() *float64 { f := 139.6503; return &f }(),
			ViewerCount:    func() *int { i := 1; return &i }(),
			ConnectionAddr: func() *string { s := "198.51.100.78"; return &s }(),
			EventType:      func() *string { s := "user_new"; return &s }(),
			Source:         func() *string { s := "mistserver_webhook"; return &s }(),
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
			EventType:       func() *string { s := "load-balancing"; return &s }(),
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
			EventType:       func() *string { s := "load-balancing"; return &s }(),
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
