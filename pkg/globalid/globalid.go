package globalid

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Type names should match GraphQL object type names implementing Node.
const (
	TypeStream             = "Stream"
	TypeClip               = "Clip"
	TypeVodAsset           = "VodAsset"
	TypeCluster            = "Cluster"
	TypeInfrastructureNode = "InfrastructureNode"

	// Messaging nodes
	TypeConversation = "Conversation"
	TypeMessage      = "Message"

	// Analytics nodes
	TypeStreamEvent            = "StreamEvent"
	TypeBufferEvent            = "BufferEvent"
	TypeTrackListEvent         = "TrackListEvent"
	TypeStreamHealthMetric     = "StreamHealthMetric"
	TypeViewerSession          = "ViewerSession"
	TypeStreamHealth5m         = "StreamHealth5m"
	TypeNodePerformance5m      = "NodePerformance5m"
	TypeViewerHoursHourly      = "ViewerHoursHourly"
	TypeViewerGeoHourly        = "ViewerGeoHourly"
	TypeTenantDailyStat        = "TenantDailyStat"
	TypeStreamConnectionHourly = "StreamConnectionHourly"
	TypeClientMetrics5m        = "ClientMetrics5m"
	TypeQualityTierDaily       = "QualityTierDaily"
	TypeStorageUsageRecord     = "StorageUsageRecord"
	TypeStorageEvent           = "StorageEvent"
	TypeProcessingUsageRecord  = "ProcessingUsageRecord"
	TypeStreamAnalyticsDaily   = "StreamAnalyticsDaily"
	TypeConnectionEvent        = "ConnectionEvent"
	TypeArtifactEvent          = "ArtifactEvent"
	TypeNodeMetric             = "NodeMetric"
	TypeNodeMetricHourly       = "NodeMetricHourly"
	TypeAPIUsageRecord         = "APIUsageRecord"
)

const compositeDelimiter = "|"

// Encode builds a Relay-style global ID (base64("Type:ID")).
func Encode(typ, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(typ + ":" + id))
}

// EncodeComposite builds a global ID from multiple parts joined with a delimiter.
func EncodeComposite(typ string, parts ...string) string {
	return Encode(typ, strings.Join(parts, compositeDelimiter))
}

// Decode parses a Relay-style global ID. ok=false means it was not a valid global ID.
func Decode(globalID string) (typ, id string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(globalID)
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// DecodeExpected returns the raw ID if the global ID matches the expected type.
// If the value is not a global ID, it is returned as-is.
func DecodeExpected(globalID, expectedType string) (string, error) {
	typ, id, ok := Decode(globalID)
	if !ok {
		return globalID, nil
	}
	if typ != expectedType {
		return "", fmt.Errorf("global id type mismatch: expected %s, got %s", expectedType, typ)
	}
	return id, nil
}

// DecodeCompositeExpected decodes a composite global ID into parts.
func DecodeCompositeExpected(globalID, expectedType string, parts int) ([]string, error) {
	raw, err := DecodeExpected(globalID, expectedType)
	if err != nil {
		return nil, err
	}
	split := strings.Split(raw, compositeDelimiter)
	if len(split) != parts {
		return nil, fmt.Errorf("global id parts mismatch: expected %d, got %d", parts, len(split))
	}
	return split, nil
}
