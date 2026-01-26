package markers

// Analytics root marker types (resolver-driven, no direct fields).
// These empty structs force gqlgen to generate resolvers for analytics domains.
type Analytics struct{}
type AnalyticsUsage struct{}
type StreamingUsage struct{}
type StorageUsage struct{}
type ProcessingUsage struct{}
type APIUsage struct{}
type AnalyticsHealth struct{}
type AnalyticsLifecycle struct{}
type AnalyticsInfra struct{}
