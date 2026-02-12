package resources

import (
	"testing"

	pb "frameworks/pkg/proto"
)

func TestMarketplaceEntryToDetail_FullMapping(t *testing.T) {
	description := "Edge cluster in us-east"
	utilization := 73.5
	owner := "FrameWorks Ops"

	entry := &pb.MarketplaceClusterEntry{
		ClusterId:            "cluster-1",
		ClusterName:          "Virginia Edge",
		ShortDescription:     &description,
		Visibility:           pb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC,
		PricingModel:         pb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY,
		MonthlyPriceCents:    4999,
		RequiresApproval:     true,
		OwnerName:            &owner,
		MaxConcurrentStreams: 42,
		MaxConcurrentViewers: 12000,
		CurrentUtilization:   &utilization,
		IsSubscribed:         true,
		SubscriptionStatus:   pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE,
		IsEligible:           true,
	}

	got := marketplaceEntryToDetail(entry)

	if got.ClusterID != "cluster-1" {
		t.Fatalf("ClusterID: got %q, want %q", got.ClusterID, "cluster-1")
	}
	if got.ClusterName != "Virginia Edge" {
		t.Fatalf("ClusterName: got %q, want %q", got.ClusterName, "Virginia Edge")
	}
	if got.Description != description {
		t.Fatalf("Description: got %q, want %q", got.Description, description)
	}
	if got.ClusterType != "CLUSTER_VISIBILITY_PUBLIC" {
		t.Fatalf("ClusterType: got %q, want %q", got.ClusterType, "CLUSTER_VISIBILITY_PUBLIC")
	}
	if got.PricingModel != "CLUSTER_PRICING_MONTHLY" {
		t.Fatalf("PricingModel: got %q, want %q", got.PricingModel, "CLUSTER_PRICING_MONTHLY")
	}
	if got.MonthlyPriceCents != 4999 {
		t.Fatalf("MonthlyPriceCents: got %d, want %d", got.MonthlyPriceCents, 4999)
	}
	if !got.RequiresApproval {
		t.Fatal("RequiresApproval: got false, want true")
	}
	if got.MaxStreams != 42 {
		t.Fatalf("MaxStreams: got %d, want %d", got.MaxStreams, 42)
	}
	if got.MaxViewers != 12000 {
		t.Fatalf("MaxViewers: got %d, want %d", got.MaxViewers, 12000)
	}
	if got.Utilization != utilization {
		t.Fatalf("Utilization: got %v, want %v", got.Utilization, utilization)
	}
	if !got.IsSubscribed {
		t.Fatal("IsSubscribed: got false, want true")
	}
	if got.SubscriptionStatus != "SUBSCRIPTION_STATUS_ACTIVE" {
		t.Fatalf("SubscriptionStatus: got %q, want %q", got.SubscriptionStatus, "SUBSCRIPTION_STATUS_ACTIVE")
	}
	if !got.IsEligible {
		t.Fatal("IsEligible: got false, want true")
	}
	if got.OwnerName != owner {
		t.Fatalf("OwnerName: got %q, want %q", got.OwnerName, owner)
	}
}

func TestMarketplaceEntryToDetail_OptionalAndEligibilityRules(t *testing.T) {
	denialReason := "billing tier does not allow this cluster"

	entry := &pb.MarketplaceClusterEntry{
		ClusterId:          "cluster-2",
		ClusterName:        "Private Compute",
		Visibility:         pb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE,
		PricingModel:       pb.ClusterPricingModel_CLUSTER_PRICING_METERED,
		IsEligible:         true,
		SubscriptionStatus: pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_UNSPECIFIED,
		DenialReason:       &denialReason,
	}

	got := marketplaceEntryToDetail(entry)

	if got.Description != "" {
		t.Fatalf("Description: got %q, want empty string", got.Description)
	}
	if got.OwnerName != "" {
		t.Fatalf("OwnerName: got %q, want empty string", got.OwnerName)
	}
	if got.SubscriptionStatus != "" {
		t.Fatalf("SubscriptionStatus: got %q, want empty string", got.SubscriptionStatus)
	}
	if got.IsEligible {
		t.Fatal("IsEligible: got true, want false when denial_reason is present")
	}
}
