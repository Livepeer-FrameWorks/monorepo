package model

import (
	"fmt"
	"io"
	"strconv"
)

// SourceLocationMode is the GraphQL string enum for a stream's source location.
// Hand-defined here because Commodore's proto declares a same-named int32 enum
// that gqlgen would otherwise autobind; resolvers translate at the gRPC boundary.
type SourceLocationMode string

const (
	SourceLocationModeAny        SourceLocationMode = "ANY"
	SourceLocationModeRestricted SourceLocationMode = "RESTRICTED"
	SourceLocationModeCustom     SourceLocationMode = "CUSTOM"
)

var AllSourceLocationMode = []SourceLocationMode{
	SourceLocationModeAny,
	SourceLocationModeRestricted,
	SourceLocationModeCustom,
}

func (e SourceLocationMode) IsValid() bool {
	switch e {
	case SourceLocationModeAny, SourceLocationModeRestricted, SourceLocationModeCustom:
		return true
	}
	return false
}

func (e SourceLocationMode) String() string { return string(e) }

func (e *SourceLocationMode) UnmarshalGQL(v any) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	*e = SourceLocationMode(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid SourceLocationMode", str)
	}
	return nil
}

func (e SourceLocationMode) MarshalGQL(w io.Writer) {
	_, _ = fmt.Fprint(w, strconv.Quote(string(e)))
}

// SourceLocationCluster is one cluster of a restricted source location.
// Hand-defined because Commodore's proto declares a same-named message.
type SourceLocationCluster struct {
	ClusterID string `json:"clusterId"`
	// Empty means any node of the cluster.
	NodeIds []string `json:"nodeIds"`
}
