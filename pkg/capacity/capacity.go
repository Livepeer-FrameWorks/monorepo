// Package capacity defines bandwidth admission in bytes per second for each direction of a node's link.
package capacity

import "math"

const (
	DefaultBudget         uint64 = 125_000_000
	DefaultIngestEstimate uint64 = 1_250_000
	TargetUtilization            = 0.70
	MaxUtilization               = 0.85
	MinimumViewerEstimate uint64 = 64 * 1024
)

type BudgetSource string

const (
	Default  BudgetSource = "DEFAULT"
	Operator BudgetSource = "OPERATOR"
)

// Link holds one direction's effective budget, measured load and outstanding admissions.
// Observed retains the last valid measurement when a new observation is invalid.
type Link struct {
	Budget        uint64
	Observed      uint64
	Reserved      uint64
	Source        BudgetSource
	ObservedValid bool
}

func (l Link) Committed() uint64 {
	return saturatedAdd(l.Observed, l.Reserved)
}

func (l Link) LoadFraction() float64 {
	if l.Budget == 0 {
		return math.Inf(1)
	}
	return (float64(l.Observed) + float64(l.Reserved)) / float64(l.Budget)
}

func (l Link) Usable() uint64 {
	return fraction(l.Budget, MaxUtilization*100)
}

func (l Link) Available() uint64 {
	return remaining(l.Usable(), l.Committed())
}

// RawAvailable preserves the federation wire meaning: budget less committed load, before headroom.
func (l Link) RawAvailable() uint64 {
	return remaining(l.Budget, l.Committed())
}

func (l Link) Admits(demand uint64) bool {
	usable := l.Usable()
	return l.Budget > 0 && l.Observed <= usable && l.Reserved <= usable-l.Observed && demand <= usable-l.Observed-l.Reserved
}

func (l Link) WithinTarget() bool {
	return l.Budget > 0 && l.Committed() <= fraction(l.Budget, TargetUtilization*100)
}

// EstimateFromTopBitrateKbps reserves the top track rate plus 10% transport overhead.
// Fractional bytes round upward; unknown and small streams reserve at least 64 KiB/s.
func EstimateFromTopBitrateKbps(kbps uint64) uint64 {
	// 1000 bits/kbit / 8 bits/byte * 1.1 = 275/2. Divide before multiplying to avoid overflow.
	whole := kbps / 2
	if whole > math.MaxUint64/275 {
		return math.MaxUint64
	}
	return max(MinimumViewerEstimate, saturatedAdd(whole*275, (kbps%2)*138))
}

func fraction(value, percent uint64) uint64 {
	return value/100*percent + value%100*percent/100
}

func remaining(budget, used uint64) uint64 {
	if used >= budget {
		return 0
	}
	return budget - used
}

func saturatedAdd(a, b uint64) uint64 {
	if b > math.MaxUint64-a {
		return math.MaxUint64
	}
	return a + b
}
