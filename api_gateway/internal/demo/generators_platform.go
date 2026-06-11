package demo

import (
	"math/rand"
	"time"

	"frameworks/api_gateway/graph/model"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// Platform god-view generators. Synthetic-but-plausible rows covering the
// billing/activity variety the admin pages must render (prepaid + postpaid,
// trial + suspended, active + dormant tenants). Values are rand/time-based:
// tests assert contracts, never golden values.

func GenerateTenantActivitySummary() *model.TenantActivitySummary {
	last := time.Now().Add(-time.Duration(rand.Intn(48)) * time.Hour)
	return &model.TenantActivitySummary{
		LiveStreams:    1 + rand.Intn(3),
		CurrentViewers: 20 + rand.Intn(400),
		IngestHours:    10 + rand.Float64()*90,
		ViewerHours:    100 + rand.Float64()*900,
		EgressGb:       50 + rand.Float64()*450,
		UniqueViewers:  200 + rand.Intn(5000),
		TotalSessions:  500 + rand.Intn(9000),
		APIRequests:    1000 + rand.Intn(90000),
		APIErrors:      rand.Intn(50),
		LastStreamAt:   &last,
	}
}

// GenerateTenantBillingSnapshot returns the proto shape directly: the
// TenantBillingSnapshot GraphQL type is autobound to the purser proto.
func GenerateTenantBillingSnapshot(tenantID string) *purserpb.TenantBillingSnapshot {
	return &purserpb.TenantBillingSnapshot{
		TenantId:            tenantID,
		BillingModel:        "postpaid",
		Status:              "active",
		TierId:              "demo-tier-pro",
		TierName:            "pro",
		NextBillingDate:     timestamppb.New(time.Now().AddDate(0, 1, 0)),
		OutstandingAmount:   float64(rand.Intn(20000)) / 100,
		OverdueInvoices:     int32(rand.Intn(2)),
		PrepaidBalanceCents: 0,
		Currency:            "EUR",
		SubscribedAt:        timestamppb.New(time.Now().AddDate(0, -6, 0)),
	}
}

func GeneratePlatformTenantIndex() *model.PlatformTenantIndex {
	specs := []struct {
		id           string
		name         string
		tier         string
		billingModel string
		status       string
		trialDays    int
		active       bool
	}{
		{DemoTenantID, "FrameWorks Demo Organization", "pro", "postpaid", "active", 0, true},
		{"5eed517e-ba5e-da7a-517e-ba5eda7a0002", "Acme Streams", "scale", "postpaid", "active", 0, true},
		{"5eed517e-ba5e-da7a-517e-ba5eda7a0003", "Pixel Praise TV", "free", "prepaid", "active", 0, true},
		{"5eed517e-ba5e-da7a-517e-ba5eda7a0004", "Trial Labs", "pro", "postpaid", "active", 9, true},
		{"5eed517e-ba5e-da7a-517e-ba5eda7a0005", "Dormant Media", "free", "prepaid", "active", 0, false},
		{"5eed517e-ba5e-da7a-517e-ba5eda7a0006", "Past Due Productions", "scale", "postpaid", "suspended", 0, false},
	}

	rows := make([]*model.PlatformTenantRow, 0, len(specs))
	for _, spec := range specs {
		tenant := GenerateTenant()
		tenant.Id = spec.id
		tenant.Name = spec.name
		tenant.DeploymentTier = spec.tier

		activity := &model.TenantActivitySummary{}
		if spec.active {
			activity = GenerateTenantActivitySummary()
		}

		billing := GenerateTenantBillingSnapshot(spec.id)
		billing.BillingModel = spec.billingModel
		billing.Status = spec.status
		billing.TierName = spec.tier
		if spec.billingModel == "prepaid" {
			billing.PrepaidBalanceCents = int64(500 + rand.Intn(10000))
			billing.OutstandingAmount = 0
		}
		if spec.status == "suspended" {
			billing.OverdueInvoices = int32(2 + rand.Intn(3))
			billing.OutstandingAmount = 150 + float64(rand.Intn(50000))/100
		}
		if spec.trialDays > 0 {
			billing.TrialEndsAt = timestamppb.New(time.Now().AddDate(0, 0, spec.trialDays))
		}

		rows = append(rows, &model.PlatformTenantRow{
			TenantID: spec.id,
			Tenant:   tenant,
			Activity: activity,
			Billing:  billing,
		})
	}

	return &model.PlatformTenantIndex{Rows: rows, GeneratedAt: time.Now()}
}

func GenerateTenantAdminContent() *model.TenantAdminContent {
	last := time.Now().Add(-time.Duration(rand.Intn(24)) * time.Hour)
	return &model.TenantAdminContent{
		ArtifactCount: 5 + rand.Intn(120),
		UserCount:     1 + rand.Intn(25),
		LiveStreams:   rand.Intn(4),
		LastStreamAt:  &last,
	}
}

func GenerateClusterPivotRows() []*model.ClusterPivotRow {
	clusters := []struct {
		clusterID string
		name      string
	}{
		{DemoCentralClusterID, "Demo Central Cluster"},
		{DemoMediaClusterID, "Demo Media Cluster"},
	}

	index := GeneratePlatformTenantIndex()
	rows := make([]*model.ClusterPivotRow, 0, len(clusters))
	for i, spec := range clusters {
		tenants := make([]*quartermasterpb.Tenant, 0, 3)
		for j, row := range index.Rows {
			if j%len(clusters) == i && row.Tenant != nil {
				tenants = append(tenants, row.Tenant)
			}
		}
		rows = append(rows, &model.ClusterPivotRow{
			Cluster: &quartermasterpb.InfrastructureCluster{
				Id:          spec.clusterID,
				ClusterId:   spec.clusterID,
				ClusterName: spec.name,
				ClusterType: "platform_official",
			},
			LiveStats: &model.ClusterLiveStats{
				ClusterID:           spec.clusterID,
				ActiveStreams:       1 + rand.Intn(8),
				CurrentViewers:      50 + rand.Intn(2000),
				UploadBytesPerSec:   float64(1_000_000 + rand.Intn(50_000_000)),
				DownloadBytesPerSec: float64(10_000_000 + rand.Intn(500_000_000)),
				ActiveNodes:         2 + rand.Intn(6),
				EgressCapacityBps:   float64(1_000_000_000),
			},
			Tenants:     tenants,
			TenantCount: len(tenants),
		})
	}
	return rows
}
