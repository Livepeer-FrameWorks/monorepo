package model

import (
	"fmt"
	"io"
	"strconv"
)

// NodeOperationalMode is the GraphQL string enum for a node's operational
// mode. Hand-defined here because the proto-side wire uses a free-form
// string ("normal", "draining", "maintenance") rather than an enum, so
// modelgen has nothing to autobind. The resolver translates between this
// enum and the wire string at the gRPC boundary.
type NodeOperationalMode string

const (
	NodeOperationalModeNormal      NodeOperationalMode = "NORMAL"
	NodeOperationalModeDraining    NodeOperationalMode = "DRAINING"
	NodeOperationalModeMaintenance NodeOperationalMode = "MAINTENANCE"
)

var AllNodeOperationalMode = []NodeOperationalMode{
	NodeOperationalModeNormal,
	NodeOperationalModeDraining,
	NodeOperationalModeMaintenance,
}

func (e NodeOperationalMode) IsValid() bool {
	switch e {
	case NodeOperationalModeNormal, NodeOperationalModeDraining, NodeOperationalModeMaintenance:
		return true
	}
	return false
}

func (e NodeOperationalMode) String() string { return string(e) }

func (e *NodeOperationalMode) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	*e = NodeOperationalMode(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid NodeOperationalMode", str)
	}
	return nil
}

func (e NodeOperationalMode) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(string(e)))
}

// WireValue returns the wire-side string Foghorn expects.
func (e NodeOperationalMode) WireValue() string {
	switch e {
	case NodeOperationalModeNormal:
		return "normal"
	case NodeOperationalModeDraining:
		return "draining"
	case NodeOperationalModeMaintenance:
		return "maintenance"
	}
	return ""
}

// NodeOperationalModeFromWire parses Foghorn's free-form mode string.
func NodeOperationalModeFromWire(s string) (NodeOperationalMode, bool) {
	switch s {
	case "", "normal":
		return NodeOperationalModeNormal, true
	case "draining":
		return NodeOperationalModeDraining, true
	case "maintenance":
		return NodeOperationalModeMaintenance, true
	default:
		return "", false
	}
}
