package tools

import (
	"fmt"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/accesspolicy"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolRisk is the server-enforced risk class for an MCP operation.
type ToolRisk string

const (
	ToolRiskRead  ToolRisk = "read"
	ToolRiskWrite ToolRisk = "write"
	ToolRiskHigh  ToolRisk = "high"
)

// ToolPolicy is the authoritative security and discovery metadata for a tool.
// MCP annotations are populated from it but remain advisory to clients.
type ToolPolicy struct {
	Scope       string
	Risk        ToolRisk
	AccessClass accesspolicy.Class
	Destructive bool
	Idempotent  bool
	OpenWorld   bool
	Public      bool
}

var toolPolicies = buildToolPolicies()

func buildToolPolicies() map[string]ToolPolicy {
	policies := make(map[string]ToolPolicy)
	add := func(scope string, risk ToolRisk, names ...string) {
		for _, name := range names {
			accessClass, ok := accesspolicy.MCPToolClass(name)
			if !ok {
				panic(fmt.Sprintf("MCP tool %q has no access policy", name))
			}
			policies[name] = ToolPolicy{Scope: scope, Risk: risk, AccessClass: accessClass, Idempotent: risk == ToolRiskRead}
		}
	}

	add("account:read", ToolRiskRead, "get_tenant_settings")
	add("account:write", ToolRiskWrite, "update_tenant_settings")
	add("billing:read", ToolRiskRead, "check_topup", "get_payment_options")
	add("billing:write", ToolRiskHigh, "complete_mollie_postpaid_setup", "pay_invoice", "start_postpaid_setup", "submit_payment", "topup_balance", "update_billing_details")
	add("streams:read", ToolRiskRead,
		"get_retention_policy", "get_vod_upload_status", "list_push_targets", "list_signing_keys",
		"list_stream_keys", "resolve_playback_endpoint", "test_playback_access", "validate_stream_key")
	add("streams:write", ToolRiskWrite,
		"complete_vod_upload", "create_clip", "create_push_target", "create_stream", "create_vod_upload",
		"set_retention_policy", "set_stream_retention_overrides", "start_dvr", "stop_dvr", "update_asset_retention", "update_push_target", "update_stream")
	add("streams:write", ToolRiskHigh,
		"abort_vod_upload", "clear_playback_policy", "create_signing_key", "create_stream_key", "delete_clip",
		"delete_dvr", "delete_push_target", "delete_stream", "delete_stream_key", "delete_vod_asset",
		"refresh_stream_key", "reset_asset_retention", "revoke_signing_key", "set_playback_policy")
	add("analytics:read", ToolRiskRead,
		"diagnose_buffer_health", "diagnose_packet_loss", "diagnose_rebuffering", "diagnose_routing",
		"get_anomaly_report", "get_stream_health_summary")
	add("support:read", ToolRiskRead, "list_support_conversations", "search_support_history")
	add("infrastructure:read", ToolRiskRead, "browse_marketplace", "get_node_health", "get_node_info")
	add("infrastructure:write", ToolRiskWrite,
		"accept_cluster_invite", "approve_subscription_request", "create_cluster_invite", "create_edge_cluster",
		"create_enrollment_token", "request_cluster_subscription", "set_node_mode", "set_preferred_cluster", "subscribe_to_cluster")
	add("infrastructure:write", ToolRiskHigh,
		"manage_node", "reject_subscription_request", "revoke_cluster_invite", "unsubscribe_from_cluster", "update_cluster_marketplace")
	add("developer:read", ToolRiskRead, "generate_query", "introspect_schema")
	add("developer:write", ToolRiskHigh, "execute_query")
	add("consultant:use", ToolRiskRead, "ask_consultant")
	add("security:read", ToolRiskRead, "list_linked_wallets")
	add("security:write", ToolRiskHigh, "link_wallet", "unlink_wallet")
	add("security:write", ToolRiskWrite, "link_email", "request_wallet_challenge")
	add("billing:write", ToolRiskWrite, "activate_free_tier")

	for _, name := range []string{"abort_vod_upload", "clear_playback_policy", "delete_clip", "delete_dvr", "delete_push_target", "delete_stream", "delete_stream_key", "delete_vod_asset", "execute_query", "manage_node", "refresh_stream_key", "reject_subscription_request", "reset_asset_retention", "revoke_cluster_invite", "revoke_signing_key", "set_playback_policy", "unlink_wallet", "unsubscribe_from_cluster", "update_cluster_marketplace"} {
		policy := policies[name]
		policy.Destructive = true
		policies[name] = policy
	}
	for _, name := range []string{"abort_vod_upload", "clear_playback_policy", "complete_mollie_postpaid_setup", "delete_clip", "delete_dvr", "delete_push_target", "delete_stream", "delete_stream_key", "delete_vod_asset", "reset_asset_retention", "revoke_cluster_invite", "revoke_signing_key", "set_node_mode", "set_playback_policy", "set_preferred_cluster", "set_retention_policy", "set_stream_retention_overrides", "stop_dvr", "unlink_wallet", "unsubscribe_from_cluster", "update_asset_retention", "update_billing_details", "update_cluster_marketplace", "update_push_target", "update_stream", "update_tenant_settings"} {
		policy := policies[name]
		policy.Idempotent = true
		policies[name] = policy
	}
	for _, name := range []string{"ask_consultant", "create_push_target", "set_playback_policy", "test_playback_access", "update_push_target"} {
		policy := policies[name]
		policy.OpenWorld = true
		policies[name] = policy
	}
	for _, name := range []string{"browse_marketplace", "request_wallet_challenge", "resolve_playback_endpoint"} {
		policy := policies[name]
		policy.Public = true
		policies[name] = policy
	}
	return policies
}

// ToolPolicyForName returns the authoritative policy for a registered tool.
func ToolPolicyForName(name string) (ToolPolicy, bool) {
	policy, ok := toolPolicies[name]
	return policy, ok
}

// ToolPolicies returns a copy for contract tests and documentation generation.
func ToolPolicies() map[string]ToolPolicy {
	result := make(map[string]ToolPolicy, len(toolPolicies))
	for name, policy := range toolPolicies {
		result[name] = policy
	}
	return result
}

func addTool[In, Out any](server *mcp.Server, tool *mcp.Tool, handler mcp.ToolHandlerFor[In, Out]) {
	policy, ok := ToolPolicyForName(tool.Name)
	if !ok {
		panic(fmt.Sprintf("MCP tool %q has no security policy", tool.Name))
	}
	readOnly := policy.Risk == ToolRiskRead
	destructive := policy.Destructive
	openWorld := policy.OpenWorld
	tool.Title = toolTitle(tool.Name)
	tool.Annotations = &mcp.ToolAnnotations{
		DestructiveHint: &destructive,
		IdempotentHint:  policy.Idempotent,
		OpenWorldHint:   &openWorld,
		ReadOnlyHint:    readOnly,
	}
	inputSchema, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(fmt.Sprintf("MCP tool %q input schema: %v", tool.Name, err))
	}
	closeStructSchemas(inputSchema)
	requireNonEmptyStrings(inputSchema)
	for property, values := range toolEnums[tool.Name] {
		if schema, exists := inputSchema.Properties[property]; exists {
			schema.Enum = values
		}
	}
	for property, bounds := range toolNumericBounds[tool.Name] {
		if schema, exists := inputSchema.Properties[property]; exists {
			schema.Minimum = bounds.Minimum
			schema.Maximum = bounds.Maximum
		}
	}
	tool.InputSchema = inputSchema
	mcp.AddTool(server, tool, handler)
}

var toolEnums = map[string]map[string][]any{
	"ask_consultant":            {"mode": {"full", "docs"}},
	"browse_marketplace":        {"pricing_model": {"FREE_UNMETERED", "TIER_INHERIT", "METERED", "MONTHLY"}},
	"create_cluster_invite":     {"access_level": {"viewer", "subscriber", "admin"}},
	"create_push_target":        {"platform": {"twitch", "youtube", "facebook", "kick", "x", "custom"}},
	"create_signing_key":        {"confirm": {"CREATE SIGNING KEY"}},
	"create_stream":             {"ingest_mode": {"push", "pull"}},
	"create_stream_key":         {"confirm": {"CREATE STREAM KEY"}},
	"delete_dvr":                {"confirm": {"DELETE DVR"}},
	"delete_stream_key":         {"confirm": {"DELETE STREAM KEY"}},
	"diagnose_buffer_health":    {"time_range": {"last_1h", "last_6h", "last_24h", "last_7d"}},
	"diagnose_packet_loss":      {"time_range": {"last_1h", "last_6h", "last_24h", "last_7d"}},
	"diagnose_rebuffering":      {"time_range": {"last_1h", "last_6h", "last_24h", "last_7d"}},
	"diagnose_routing":          {"time_range": {"last_1h", "last_6h", "last_24h", "last_7d"}},
	"get_anomaly_report":        {"sensitivity": {"low", "medium", "high"}},
	"get_stream_health_summary": {"time_range": {"last_1h", "last_6h", "last_24h", "last_7d"}},
	"list_signing_keys":         {"status": {"active", "revoked"}},
	"manage_node":               {"action": {"drain", "maintenance", "restore", "status", "diagnose", "logs"}},
	"pay_invoice":               {"method": {"card", "crypto_usdc", "crypto_eth"}},
	"refresh_stream_key":        {"confirm": {"ROTATE STREAM KEY"}},
	"reset_asset_retention":     {"target_type": {"dvr", "clip", "vod"}},
	"revoke_signing_key":        {"confirm": {"REVOKE SIGNING KEY"}},
	"set_node_mode":             {"mode": {"normal", "draining", "maintenance"}},
	"set_playback_policy":       {"type": {"public", "jwt", "webhook"}, "confirm": {"SET PLAYBACK POLICY"}},
	"clear_playback_policy":     {"confirm": {"CLEAR PLAYBACK POLICY"}},
	"set_retention_policy":      {"target_type": {"vod", "dvr", "clip"}},
	"start_postpaid_setup": {
		"provider":       {"stripe", "mollie"},
		"billing_period": {"monthly", "yearly"},
		"method":         {"creditcard", "ideal", "bancontact"},
	},
	"topup_balance":          {"asset": {"USDC", "ETH"}},
	"update_asset_retention": {"target_type": {"dvr", "clip", "vod"}},
	"update_cluster_marketplace": {
		"visibility":    {"PUBLIC", "UNLISTED", "PRIVATE"},
		"pricing_model": {"FREE_UNMETERED", "METERED", "MONTHLY", "TIER_INHERIT", "CUSTOM"},
		"confirm":       {"UPDATE CLUSTER MARKETPLACE"},
	},
	"update_stream":            {"ingest_mode": {"push", "pull"}},
	"unsubscribe_from_cluster": {"confirm": {"UNSUBSCRIBE FROM CLUSTER"}},
}

type numericBounds struct {
	Minimum *float64
	Maximum *float64
}

func number(value float64) *float64 { return &value }

var toolNumericBounds = map[string]map[string]numericBounds{
	"introspect_schema":          {"depth": {Minimum: number(1), Maximum: number(4)}},
	"list_support_conversations": {"limit": {Minimum: number(1), Maximum: number(50)}},
	"search_support_history":     {"limit": {Minimum: number(1), Maximum: number(50)}},
	"topup_balance":              {"amount_cents": {Minimum: number(1), Maximum: number(10_000_000)}},
}

func requireNonEmptyStrings(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	for _, name := range schema.Required {
		property := schema.Properties[name]
		if property != nil && property.Type == "string" && property.MinLength == nil {
			property.MinLength = new(int)
			*property.MinLength = 1
		}
	}
	for _, property := range schema.Properties {
		requireNonEmptyStrings(property)
	}
	for _, definition := range schema.Defs {
		requireNonEmptyStrings(definition)
	}
	for _, definition := range schema.Definitions {
		requireNonEmptyStrings(definition)
	}
	requireNonEmptyStrings(schema.Items)
}

func closeStructSchemas(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	if schema.Type == "object" && schema.AdditionalProperties == nil {
		schema.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
	}
	for _, property := range schema.Properties {
		closeStructSchemas(property)
	}
	for _, definition := range schema.Defs {
		closeStructSchemas(definition)
	}
	for _, definition := range schema.Definitions {
		closeStructSchemas(definition)
	}
	closeStructSchemas(schema.Items)
	for _, child := range schema.PrefixItems {
		closeStructSchemas(child)
	}
	for _, child := range schema.ItemsArray {
		closeStructSchemas(child)
	}
	for _, child := range schema.AllOf {
		closeStructSchemas(child)
	}
	for _, child := range schema.AnyOf {
		closeStructSchemas(child)
	}
	for _, child := range schema.OneOf {
		closeStructSchemas(child)
	}
	for _, child := range schema.PatternProperties {
		closeStructSchemas(child)
	}
	for _, child := range schema.DependentSchemas {
		closeStructSchemas(child)
	}
	closeStructSchemas(schema.AdditionalItems)
	closeStructSchemas(schema.Contains)
	closeStructSchemas(schema.UnevaluatedItems)
	closeStructSchemas(schema.PropertyNames)
	closeStructSchemas(schema.UnevaluatedProperties)
	closeStructSchemas(schema.If)
	closeStructSchemas(schema.Then)
	closeStructSchemas(schema.Else)
	closeStructSchemas(schema.ContentSchema)
}

func toolTitle(name string) string {
	parts := strings.Split(name, "_")
	for i := range parts {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, " ")
}
