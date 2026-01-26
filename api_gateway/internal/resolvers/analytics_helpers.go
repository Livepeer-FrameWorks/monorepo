package resolvers

import (
	"fmt"

	"frameworks/pkg/globalid"

	"github.com/google/uuid"
)

func normalizeStreamID(streamID string) (string, error) {
	if streamID == "" {
		return streamID, nil
	}
	if typ, id, ok := globalid.Decode(streamID); ok {
		if typ != globalid.TypeStream {
			return "", fmt.Errorf("invalid stream relay ID type: %s", typ)
		}
		return id, nil
	}
	return streamID, nil
}

func normalizeStreamIDPtr(streamID *string) (*string, error) {
	if streamID == nil || *streamID == "" {
		return streamID, nil
	}
	rawID, err := normalizeStreamID(*streamID)
	if err != nil {
		return nil, err
	}
	return &rawID, nil
}

func normalizeClipHash(input string) (string, error) {
	if input == "" {
		return input, nil
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeClip {
			return "", fmt.Errorf("invalid clip relay ID type: %s", typ)
		}
		if _, err := uuid.Parse(id); err == nil {
			return "", fmt.Errorf("clip relay IDs now encode clipHash; use clipHash instead")
		}
		return id, nil
	}
	return input, nil
}

func normalizeVodHash(input string) (string, error) {
	if input == "" {
		return input, nil
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeVodAsset {
			return "", fmt.Errorf("invalid VOD relay ID type: %s", typ)
		}
		if _, err := uuid.Parse(id); err == nil {
			return "", fmt.Errorf("VOD relay IDs now encode artifactHash; use artifactHash instead")
		}
		return id, nil
	}
	return input, nil
}

// NormalizeStreamID exposes stream ID normalization to other packages.
func NormalizeStreamID(streamID string) (string, error) {
	return normalizeStreamID(streamID)
}

// NormalizeClipHash exposes clip hash normalization to other packages.
func NormalizeClipHash(input string) (string, error) {
	return normalizeClipHash(input)
}

// NormalizeVodHash exposes VOD hash normalization to other packages.
func NormalizeVodHash(input string) (string, error) {
	return normalizeVodHash(input)
}
