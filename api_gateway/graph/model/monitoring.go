package model

import (
	"fmt"
	"io"
	"strconv"
)

// MonitoringToggle is the GraphQL string enum for a stream's Skipper
// monitoring override. Hand-defined here because Commodore's proto enum is
// int32-based; resolvers translate between the GraphQL enum and proto enum at
// the gRPC boundary.
type MonitoringToggle string

const (
	MonitoringToggleInherit MonitoringToggle = "INHERIT"
	MonitoringToggleOn      MonitoringToggle = "ON"
	MonitoringToggleOff     MonitoringToggle = "OFF"
)

var AllMonitoringToggle = []MonitoringToggle{
	MonitoringToggleInherit,
	MonitoringToggleOn,
	MonitoringToggleOff,
}

func (e MonitoringToggle) IsValid() bool {
	switch e {
	case MonitoringToggleInherit, MonitoringToggleOn, MonitoringToggleOff:
		return true
	}
	return false
}

func (e MonitoringToggle) String() string { return string(e) }

func (e *MonitoringToggle) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	*e = MonitoringToggle(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid MonitoringToggle", str)
	}
	return nil
}

func (e MonitoringToggle) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(string(e)))
}
