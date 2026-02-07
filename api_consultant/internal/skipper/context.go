package skipper

import "context"

type contextKey string

const (
	keyTenantID contextKey = "skipper_tenant_id"
	keyUserID   contextKey = "skipper_user_id"
	keyJWTToken contextKey = "skipper_jwt_token"
	keyAuthType contextKey = "skipper_auth_type"
	keyRole     contextKey = "skipper_role"
	keyMode     contextKey = "skipper_mode"
)

func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyTenantID, id)
}

func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(keyTenantID).(string); ok {
		return v
	}
	return ""
}

func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyUserID, id)
}

func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(keyUserID).(string); ok {
		return v
	}
	return ""
}

func WithJWTToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, keyJWTToken, token)
}

func GetJWTToken(ctx context.Context) string {
	if v, ok := ctx.Value(keyJWTToken).(string); ok {
		return v
	}
	return ""
}

func WithAuthType(ctx context.Context, authType string) context.Context {
	return context.WithValue(ctx, keyAuthType, authType)
}

func GetAuthType(ctx context.Context) string {
	if v, ok := ctx.Value(keyAuthType).(string); ok {
		return v
	}
	return ""
}

func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, keyRole, role)
}

func GetRole(ctx context.Context) string {
	if v, ok := ctx.Value(keyRole).(string); ok {
		return v
	}
	return ""
}

func WithMode(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, keyMode, mode)
}

func GetMode(ctx context.Context) string {
	if v, ok := ctx.Value(keyMode).(string); ok {
		return v
	}
	return ""
}
