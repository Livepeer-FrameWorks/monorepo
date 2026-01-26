package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/globalid"

	"github.com/google/uuid"
)

func decodeStreamID(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("stream_id is required")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeStream {
			return "", fmt.Errorf("invalid stream relay ID type: %s", typ)
		}
		return id, nil
	}
	return input, nil
}

func resolveVodIdentifier(ctx context.Context, input string, clients *clients.ServiceClients) (string, error) {
	if input == "" {
		return "", fmt.Errorf("invalid artifact hash")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeVodAsset {
			return "", fmt.Errorf("invalid VOD relay ID type: %s", typ)
		}
		if _, err := uuid.Parse(id); err == nil {
			if clients == nil || clients.Commodore == nil {
				return "", fmt.Errorf("VOD resolver unavailable")
			}
			resp, err := clients.Commodore.ResolveVodID(ctx, id)
			if err != nil {
				return "", fmt.Errorf("failed to resolve VOD relay ID: %w", err)
			}
			if resp == nil || !resp.Found {
				return "", fmt.Errorf("VOD asset not found")
			}
			return resp.VodHash, nil
		}
		return id, nil
	}
	return input, nil
}
