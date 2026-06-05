package grpc

import (
	"reflect"
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// visibilityStringToProto fails closed: an unrecognized stored value maps to
// PRIVATE (most restrictive), never to a more public setting.
func TestVisibilityStringToProto(t *testing.T) {
	tests := []struct {
		in   string
		want quartermasterpb.ClusterVisibility
	}{
		{"public", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC},
		{"unlisted", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED},
		{"private", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE},
		{"garbage", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE},
		{"", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE},
		// Case-sensitive: uppercase is not recognized and falls closed to PRIVATE.
		{"PUBLIC", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE},
	}
	for _, tt := range tests {
		if got := visibilityStringToProto(tt.in); got != tt.want {
			t.Errorf("visibilityStringToProto(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// subscriptionStatusStringToProto maps each known status; unknown → UNSPECIFIED.
func TestSubscriptionStatusStringToProto(t *testing.T) {
	tests := []struct {
		in   string
		want quartermasterpb.ClusterSubscriptionStatus
	}{
		{"pending_approval", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL},
		{"active", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE},
		{"suspended", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_SUSPENDED},
		{"rejected", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_REJECTED},
		{"unknown", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_UNSPECIFIED},
		{"", quartermasterpb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_UNSPECIFIED},
	}
	for _, tt := range tests {
		if got := subscriptionStatusStringToProto(tt.in); got != tt.want {
			t.Errorf("subscriptionStatusStringToProto(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// normalizeStringSlice trims, drops empties, sorts, and dedupes.
func TestNormalizeStringSlice(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"all-empty input yields empty slice", []string{"", "  "}, []string{}},
		{"trim sort dedupe", []string{" b ", "a", "b", "a"}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		if got := normalizeStringSlice(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: normalizeStringSlice(%v) = %v, want %v", tt.name, tt.in, got, tt.want)
		}
	}
}
