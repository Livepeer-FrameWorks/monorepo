// GraphQL union interface markers and enum marshaling for quartermaster proto types.

package quartermasterpb

import (
	"fmt"
	"io"
	"strconv"
)

// Tenant implements union interfaces
func (*Tenant) IsUpdateTenantResult() {}

// BootstrapToken implements union interfaces
func (*BootstrapToken) IsCreateBootstrapTokenResult() {}

// CreatePrivateClusterResponse implements union interfaces
func (*CreatePrivateClusterResponse) IsCreatePrivateClusterResult() {}

// InfrastructureCluster (GraphQL type: Cluster) implements union interfaces
func (*InfrastructureCluster) IsUpdateClusterResult()       {}
func (*InfrastructureCluster) IsSetPreferredClusterResult() {}

// ClusterInvite implements union interfaces
func (*ClusterInvite) IsCreateClusterInviteResult() {}

// ClusterSubscription implements union interfaces
func (*ClusterSubscription) IsClusterSubscriptionResult() {}

// InfrastructureNode (GraphQL type: InfrastructureNode) implements union interfaces
func (*InfrastructureNode) IsSetNodeModeResult() {}

// MarshalGQL implements the graphql.Marshaler interface for ClusterVisibility
func (e ClusterVisibility) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC:
		s = "PUBLIC"
	case ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED:
		s = "UNLISTED"
	case ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE:
		s = "PRIVATE"
	default:
		s = "PRIVATE"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for ClusterVisibility
func (e *ClusterVisibility) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "PUBLIC":
		*e = ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC
	case "UNLISTED":
		*e = ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED
	case "PRIVATE":
		*e = ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE
	default:
		return fmt.Errorf("%s is not a valid ClusterVisibility", str)
	}
	return nil
}

// MarshalGQL implements the graphql.Marshaler interface for ClusterPricingModel
func (e ClusterPricingModel) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED:
		s = "FREE_UNMETERED"
	case ClusterPricingModel_CLUSTER_PRICING_METERED:
		s = "METERED"
	case ClusterPricingModel_CLUSTER_PRICING_MONTHLY:
		s = "MONTHLY"
	case ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT:
		s = "TIER_INHERIT"
	case ClusterPricingModel_CLUSTER_PRICING_CUSTOM:
		s = "CUSTOM"
	default:
		s = "TIER_INHERIT"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for ClusterPricingModel
func (e *ClusterPricingModel) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "FREE_UNMETERED":
		*e = ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	case "METERED":
		*e = ClusterPricingModel_CLUSTER_PRICING_METERED
	case "MONTHLY":
		*e = ClusterPricingModel_CLUSTER_PRICING_MONTHLY
	case "TIER_INHERIT":
		*e = ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT
	case "CUSTOM":
		*e = ClusterPricingModel_CLUSTER_PRICING_CUSTOM
	default:
		return fmt.Errorf("%s is not a valid ClusterPricingModel", str)
	}
	return nil
}

// MarshalGQL implements the graphql.Marshaler interface for ClusterSubscriptionStatus
func (e ClusterSubscriptionStatus) MarshalGQL(w io.Writer) {
	var s string
	switch e {
	case ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL:
		s = "PENDING_APPROVAL"
	case ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE:
		s = "ACTIVE"
	case ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_SUSPENDED:
		s = "SUSPENDED"
	case ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_REJECTED:
		s = "REJECTED"
	default:
		s = "PENDING_APPROVAL"
	}
	io.WriteString(w, strconv.Quote(s)) //nolint:errcheck // MarshalGQL has no error return
}

// UnmarshalGQL implements the graphql.Unmarshaler interface for ClusterSubscriptionStatus
func (e *ClusterSubscriptionStatus) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	switch str {
	case "PENDING_APPROVAL":
		*e = ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL
	case "ACTIVE":
		*e = ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_ACTIVE
	case "SUSPENDED":
		*e = ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_SUSPENDED
	case "REJECTED":
		*e = ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_REJECTED
	default:
		return fmt.Errorf("%s is not a valid ClusterSubscriptionStatus", str)
	}
	return nil
}
