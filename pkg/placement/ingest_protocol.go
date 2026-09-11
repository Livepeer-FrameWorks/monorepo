package placement

import (
	"fmt"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// IngestProtocolName rejects unknown wire values instead of widening them to any protocol.
func IngestProtocolName(protocol sharedpb.IngestProtocol) (string, error) {
	switch protocol {
	case sharedpb.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED:
		return "", nil
	case sharedpb.IngestProtocol_INGEST_PROTOCOL_WHIP:
		return "whip", nil
	case sharedpb.IngestProtocol_INGEST_PROTOCOL_RTMP:
		return "rtmp", nil
	case sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT:
		return "srt", nil
	default:
		return "", fmt.Errorf("unsupported ingest protocol")
	}
}
