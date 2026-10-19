package resolvers

import (
	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"

	"github.com/google/uuid"
)

func normalizeStreamID(streamID string) (string, error) {
	if streamID == "" {
		return streamID, nil
	}
	if typ, id, ok := decodeKnownGlobalID(streamID); ok {
		if typ != globalid.TypeStream {
			return "", middleware.InvalidInput("invalid stream relay ID type: %s", typ)
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

// decodeKnownGlobalID decodes input as a global ID only when the decoded type
// is a known Node type. Raw 32-hex artifact and clip hashes are valid base64
// and can decode into an arbitrary "prefix:rest" pair; those stay raw.
func decodeKnownGlobalID(input string) (typ, id string, ok bool) {
	typ, id, ok = globalid.Decode(input)
	if !ok || !globalid.IsKnownType(typ) {
		return "", "", false
	}
	return typ, id, true
}

// isCanonicalUUID matches only the dashed 36-character UUID form. uuid.Parse
// also accepts 32 bare hex characters, which is the shape of every artifact
// and clip hash.
func isCanonicalUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}

func normalizeClipHash(input string) (string, error) {
	if input == "" {
		return input, nil
	}
	if typ, id, ok := decodeKnownGlobalID(input); ok {
		if typ != globalid.TypeClip {
			return "", middleware.InvalidInput("invalid clip relay ID type: %s", typ)
		}
		if isCanonicalUUID(id) {
			return "", middleware.InvalidInput("clip relay IDs now encode clipHash; use clipHash instead")
		}
		return id, nil
	}
	return input, nil
}

func normalizeVodHash(input string) (string, error) {
	if input == "" {
		return input, nil
	}
	if typ, id, ok := decodeKnownGlobalID(input); ok {
		if typ != globalid.TypeVodAsset {
			return "", middleware.InvalidInput("invalid VOD relay ID type: %s", typ)
		}
		if isCanonicalUUID(id) {
			return "", middleware.InvalidInput("VOD relay IDs now encode artifactHash; use artifactHash instead")
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
