package quartermasterpb

import quartermastervaluespb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster_values"

// Capacity aliases keep the service client's public types identical to the shared value types.
type CapacitySource = quartermastervaluespb.CapacitySource
type NodeCapacity = quartermastervaluespb.NodeCapacity
type SetNodeCapacityRequest = quartermastervaluespb.SetNodeCapacityRequest
type GetNodeCapacityRequest = quartermastervaluespb.GetNodeCapacityRequest
type ListNodeCapacitiesRequest = quartermastervaluespb.ListNodeCapacitiesRequest
type ListNodeCapacitiesResponse = quartermastervaluespb.ListNodeCapacitiesResponse

const (
	CapacitySource_CAPACITY_SOURCE_UNSPECIFIED = quartermastervaluespb.CapacitySource_CAPACITY_SOURCE_UNSPECIFIED
	CapacitySource_CAPACITY_SOURCE_OPERATOR    = quartermastervaluespb.CapacitySource_CAPACITY_SOURCE_OPERATOR
	CapacitySource_CAPACITY_SOURCE_DEFAULT     = quartermastervaluespb.CapacitySource_CAPACITY_SOURCE_DEFAULT
)

var (
	CapacitySource_name  = quartermastervaluespb.CapacitySource_name
	CapacitySource_value = quartermastervaluespb.CapacitySource_value
)
