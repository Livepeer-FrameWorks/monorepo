// Package accesspolicy defines the payment/access class of externally exposed
// operations. Authorization, abuse limits, and suspension remain independent
// policy layers.
package accesspolicy

import "strings"

// Class describes whether an operation is usable before a prepaid tenant has
// funded rated work.
type Class string

type X402MutationStrategy string

const (
	Authentication  Class = "authentication"
	Read            Class = "read"
	Control         Class = "control"
	Rated           Class = "rated"
	PaymentRecovery Class = "payment_recovery"
	Privileged      Class = "privileged"

	X402OwnerIdempotency X402MutationStrategy = "owner_idempotency"
	X402Unsupported      X402MutationStrategy = "unsupported"
)

// Direct x402 execution is denied for these rated side effects until the
// owning service exposes a durable quote-bound idempotency contract. Agents can
// still top up through submit_payment and retry the operation from balance.
var graphqlX402MutationStrategies = map[string]X402MutationStrategy{
	"createClip": X402Unsupported, "startDVR": X402Unsupported,
	"createVodUpload": X402Unsupported, "completeVodUpload": X402Unsupported,
	"sendMessage": X402Unsupported, "updateMediaRetention": X402Unsupported,
}

var mcpX402MutationStrategies = map[string]X402MutationStrategy{
	"complete_vod_upload": X402Unsupported, "create_clip": X402Unsupported,
	"create_vod_upload": X402Unsupported, "start_dvr": X402Unsupported,
	"update_asset_retention": X402Unsupported, "ask_consultant": X402Unsupported,
	"execute_query": X402Unsupported,
}

// UnfundedAllowed reports whether balance alone may deny this class. Normal
// authentication, authorization, suspension, and abuse checks still apply.
func (c Class) UnfundedAllowed() bool {
	return c != Rated
}

var graphqlMutationClasses = map[string]Class{
	"createStream":                  Control,
	"updateStream":                  Control,
	"deleteStream":                  Control,
	"refreshStreamKey":              Control,
	"createClip":                    Rated,
	"deleteClip":                    Control,
	"startDVR":                      Rated,
	"stopDVR":                       Control,
	"deleteDVR":                     Control,
	"createVodUpload":               Rated,
	"completeVodUpload":             Rated,
	"abortVodUpload":                Control,
	"deleteVodAsset":                Control,
	"createPayment":                 PaymentRecovery,
	"submitX402Payment":             PaymentRecovery,
	"createStripeCheckout":          PaymentRecovery,
	"createStripeBillingPortal":     PaymentRecovery,
	"createMollieFirstPayment":      PaymentRecovery,
	"createMollieSubscription":      PaymentRecovery,
	"createCardTopup":               PaymentRecovery,
	"createCryptoTopup":             PaymentRecovery,
	"cryptoTopupStatus":             PaymentRecovery,
	"updateBillingDetails":          PaymentRecovery,
	"updateSubscriptionCustomTerms": Privileged,
	"updateTenant":                  Control,
	"subscribeToCluster":            Control,
	"unsubscribeFromCluster":        Control,
	"createEdgeCluster":             Control,
	"createEnrollmentToken":         Control,
	"bootstrapEdge":                 Privileged,
	"updateClusterMarketplace":      Control,
	"createClusterInvite":           Control,
	"revokeClusterInvite":           Control,
	"requestClusterSubscription":    Control,
	"acceptClusterInvite":           Control,
	"approveClusterSubscription":    Control,
	"rejectClusterSubscription":     Control,
	"setPreferredCluster":           Control,
	"createDeveloperToken":          Control,
	"revokeDeveloperToken":          Control,
	"createSigningKey":              Control,
	"revokeSigningKey":              Control,
	"setPlaybackPolicy":             Control,
	"createBootstrapToken":          Control,
	"revokeBootstrapToken":          Control,
	"createStreamKey":               Control,
	"deleteStreamKey":               Control,
	"createPushTarget":              Control,
	"updatePushTarget":              Control,
	"deletePushTarget":              Control,
	"walletLogin":                   Authentication,
	"linkWallet":                    Control,
	"unlinkWallet":                  Control,
	"linkEmail":                     Control,
	"promoteToPaid":                 PaymentRecovery,
	"changeBillingTier":             PaymentRecovery,
	"deleteSkipperConversation":     Control,
	"updateSkipperConversation":     Control,
	"markSkipperReportsRead":        Control,
	"createConversation":            Control,
	"sendMessage":                   Rated,
	"setNodeMode":                   Control,
	"openMistAdminSession":          Privileged,
	"testPlaybackAccess":            Control,
	"setMediaRetentionPolicy":       Control,
	"updateMediaRetention":          Rated,
	"resetMediaRetentionOverride":   Control,
	"setStreamRetentionOverrides":   Control,
}

var mcpToolClasses = map[string]Class{
	"get_tenant_settings":            Read,
	"update_tenant_settings":         Control,
	"check_topup":                    PaymentRecovery,
	"get_payment_options":            PaymentRecovery,
	"complete_mollie_postpaid_setup": PaymentRecovery,
	"pay_invoice":                    PaymentRecovery,
	"start_postpaid_setup":           PaymentRecovery,
	"submit_payment":                 PaymentRecovery,
	"topup_balance":                  PaymentRecovery,
	"update_billing_details":         PaymentRecovery,
	"get_retention_policy":           Read,
	"get_vod_upload_status":          Read,
	"list_push_targets":              Read,
	"list_signing_keys":              Read,
	"list_stream_keys":               Read,
	"resolve_playback_endpoint":      Read,
	"test_playback_access":           Control,
	"validate_stream_key":            Read,
	"complete_vod_upload":            Rated,
	"create_clip":                    Rated,
	"create_push_target":             Control,
	"create_stream":                  Control,
	"create_vod_upload":              Rated,
	"set_retention_policy":           Control,
	"set_stream_retention_overrides": Control,
	"start_dvr":                      Rated,
	"stop_dvr":                       Control,
	"update_asset_retention":         Rated,
	"update_push_target":             Control,
	"update_stream":                  Control,
	"abort_vod_upload":               Control,
	"clear_playback_policy":          Control,
	"create_signing_key":             Control,
	"create_stream_key":              Control,
	"delete_clip":                    Control,
	"delete_dvr":                     Control,
	"delete_push_target":             Control,
	"delete_stream":                  Control,
	"delete_stream_key":              Control,
	"delete_vod_asset":               Control,
	"refresh_stream_key":             Control,
	"reset_asset_retention":          Control,
	"revoke_signing_key":             Control,
	"set_playback_policy":            Control,
	"diagnose_buffer_health":         Read,
	"diagnose_packet_loss":           Read,
	"diagnose_rebuffering":           Read,
	"diagnose_routing":               Read,
	"get_anomaly_report":             Read,
	"get_stream_health_summary":      Read,
	"list_support_conversations":     Read,
	"search_support_history":         Read,
	"ask_consultant":                 Rated,
	"browse_marketplace":             Read,
	"get_node_health":                Read,
	"get_node_info":                  Read,
	"accept_cluster_invite":          Control,
	"approve_subscription_request":   Control,
	"create_cluster_invite":          Control,
	"create_edge_cluster":            Control,
	"create_enrollment_token":        Control,
	"request_cluster_subscription":   Control,
	"set_node_mode":                  Control,
	"set_preferred_cluster":          Control,
	"subscribe_to_cluster":           Control,
	"manage_node":                    Privileged,
	"reject_subscription_request":    Control,
	"revoke_cluster_invite":          Control,
	"unsubscribe_from_cluster":       Control,
	"update_cluster_marketplace":     Control,
	"generate_query":                 Read,
	"introspect_schema":              Read,
	"execute_query":                  Rated,
	"list_linked_wallets":            Read,
	"activate_free_tier":             PaymentRecovery,
	"link_email":                     PaymentRecovery,
	"link_wallet":                    Control,
	"unlink_wallet":                  Control,
	"request_wallet_challenge":       Authentication,
}

// GraphQLMutationClass returns the class for a top-level GraphQL mutation.
func GraphQLMutationClass(name string) (Class, bool) {
	class, ok := graphqlMutationClasses[name]
	return class, ok
}

// GraphQLMutationClasses returns a defensive copy for schema parity tests and
// documentation generation.
func GraphQLMutationClasses() map[string]Class {
	return clone(graphqlMutationClasses)
}

// MCPToolClass returns the class for a registered MCP tool.
func MCPToolClass(name string) (Class, bool) {
	class, ok := mcpToolClasses[name]
	return class, ok
}

// MCPToolClasses returns a defensive copy for registry parity tests and
// documentation generation.
func MCPToolClasses() map[string]Class {
	return clone(mcpToolClasses)
}

func GraphQLX402MutationStrategy(name string) (X402MutationStrategy, bool) {
	strategy, ok := graphqlX402MutationStrategies[name]
	return strategy, ok
}

func MCPX402MutationStrategy(name string) (X402MutationStrategy, bool) {
	strategy, ok := mcpX402MutationStrategies[name]
	return strategy, ok
}

// OperationClass resolves the operation form used by Gateway admission.
func OperationClass(operationType, name string) (Class, bool) {
	switch operationType {
	case "query", "subscription":
		return Read, true
	case "mutation":
		return GraphQLMutationClass(name)
	}

	const toolPrefix = "mcp:tools/call:"
	if strings.HasPrefix(name, toolPrefix) {
		return MCPToolClass(strings.TrimPrefix(name, toolPrefix))
	}
	if strings.HasPrefix(name, "mcp:resources/read:") ||
		name == "mcp:initialize" || name == "mcp:tools/list" ||
		name == "mcp:resources/list" || name == "mcp:resources/templates/list" ||
		name == "mcp:prompts/list" || name == "mcp:prompts/get" {
		return Read, true
	}
	return "", false
}

func clone(source map[string]Class) map[string]Class {
	result := make(map[string]Class, len(source))
	for name, class := range source {
		result[name] = class
	}
	return result
}
