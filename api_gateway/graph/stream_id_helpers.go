package graph

import (
	"fmt"

	"frameworks/pkg/globalid"
)

func encodeStreamID(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("streamId missing")
	}
	return globalid.Encode(globalid.TypeStream, raw), nil
}

func encodeStreamIDOptional(raw string) (*string, error) {
	if raw == "" {
		return nil, nil
	}
	encoded := globalid.Encode(globalid.TypeStream, raw)
	return &encoded, nil
}

func encodeStreamIDOptionalPtr(raw *string) (*string, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	encoded := globalid.Encode(globalid.TypeStream, *raw)
	return &encoded, nil
}
