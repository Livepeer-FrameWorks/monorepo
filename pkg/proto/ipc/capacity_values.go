package ipcpb

import capacityvaluespb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/capacity_values"

// IPC consumers use the same enum values as control contracts without importing their service APIs.
type BandwidthBudgetSource = capacityvaluespb.BandwidthBudgetSource
type ObservedSource = capacityvaluespb.ObservedSource

const (
	BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_UNSPECIFIED = capacityvaluespb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_UNSPECIFIED
	BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR    = capacityvaluespb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR
	BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT     = capacityvaluespb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_DEFAULT
	ObservedSource_OBSERVED_SOURCE_UNSPECIFIED                = capacityvaluespb.ObservedSource_OBSERVED_SOURCE_UNSPECIFIED
	ObservedSource_OBSERVED_SOURCE_NIC                        = capacityvaluespb.ObservedSource_OBSERVED_SOURCE_NIC
	ObservedSource_OBSERVED_SOURCE_MIST                       = capacityvaluespb.ObservedSource_OBSERVED_SOURCE_MIST
)

var (
	BandwidthBudgetSource_name  = capacityvaluespb.BandwidthBudgetSource_name
	BandwidthBudgetSource_value = capacityvaluespb.BandwidthBudgetSource_value
	ObservedSource_name         = capacityvaluespb.ObservedSource_name
	ObservedSource_value        = capacityvaluespb.ObservedSource_value
)
