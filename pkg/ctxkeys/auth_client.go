package ctxkeys

import "context"

type authClientMetadataKey struct{}

// AuthClientMetadata carries request provenance and client-provided display
// hints. None of these fields grants authentication or authorization.
type AuthClientMetadata struct {
	IP, UserAgent, DeviceLabel, Version string
}

func WithAuthClientMetadata(ctx context.Context, value AuthClientMetadata) context.Context {
	return context.WithValue(ctx, authClientMetadataKey{}, value)
}

func GetAuthClientMetadata(ctx context.Context) AuthClientMetadata {
	if value, ok := ctx.Value(authClientMetadataKey{}).(AuthClientMetadata); ok {
		return value
	}
	return AuthClientMetadata{}
}
