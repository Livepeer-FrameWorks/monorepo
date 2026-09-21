package tools

import (
	"context"
	"errors"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/resolvers"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Confirmation strings of the webhook write tools.
const (
	confirmCreateWebhookEndpoint  = "CREATE WEBHOOK ENDPOINT"
	confirmUpdateWebhookEndpoint  = "UPDATE WEBHOOK ENDPOINT"
	confirmDeleteWebhookEndpoint  = "DELETE WEBHOOK ENDPOINT"
	confirmEnableWebhookEndpoint  = "ENABLE WEBHOOK ENDPOINT"
	confirmDisableWebhookEndpoint = "DISABLE WEBHOOK ENDPOINT"
	confirmRotateWebhookSecret    = "ROTATE WEBHOOK SECRET"
	confirmTestWebhookEndpoint    = "TEST WEBHOOK ENDPOINT"
	confirmReplayWebhookDelivery  = "REPLAY WEBHOOK DELIVERY"
	confirmReplayWebhookRange     = "REPLAY WEBHOOK DELIVERIES"
)

const webhookSecretWarning = "The signing secret is returned once. Store it now; FrameWorks never returns it again. Rotate the secret if it is lost."

// RegisterWebhookTools registers the outbound webhook tools. They run through
// the GraphQL resolvers, so authorization, tenant scoping, and error mapping
// match the API exactly. Every write tool requires a confirm string.
func RegisterWebhookTools(server *mcp.Server, resolver *resolvers.Resolver) {
	addTool(server,
		&mcp.Tool{
			Name:        "list_webhook_endpoints",
			Description: "List the tenant's outbound webhook endpoints (at most 10) with status, subscribed event types, and failure counters. Signing secrets are never returned.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWebhookEndpointsInput) (*mcp.CallToolResult, any, error) {
			conn, err := resolver.DoWebhookEndpointsConnection(ctx, nil)
			if err != nil {
				return toolError(err.Error())
			}
			return toolSuccess(conn)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "get_webhook_endpoint",
			Description: "Get one webhook endpoint. The signing secret is never returned.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args WebhookEndpointIDInput) (*mcp.CallToolResult, any, error) {
			ep, err := resolver.DoWebhookEndpoint(ctx, args.EndpointID)
			if err != nil {
				return toolError(err.Error())
			}
			if ep == nil {
				return toolError("Webhook endpoint not found")
			}
			return toolSuccess(ep)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "list_webhook_deliveries",
			Description: "List webhook deliveries newest first. Filter by endpoint, status (pending, succeeded, failed, skipped), event type, event ID, or creation time. Use get_webhook_delivery for the HTTP attempts of one delivery.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ListWebhookDeliveriesInput) (*mcp.CallToolResult, any, error) {
			filter, err := args.filter()
			if err != nil {
				return toolError(err.Error())
			}
			page := &model.ConnectionInput{}
			if args.First > 0 {
				page.First = &args.First
			}
			if args.After != "" {
				page.After = &args.After
			}
			conn, err := resolver.DoWebhookDeliveriesConnection(ctx, filter, page)
			if err != nil {
				return toolError(err.Error())
			}
			return toolSuccess(conn)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "get_webhook_delivery",
			Description: "Get one webhook delivery with every HTTP attempt: status code, error class, latency, and up to 1 KiB of the response body.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args WebhookDeliveryIDInput) (*mcp.CallToolResult, any, error) {
			d, err := resolver.DoWebhookDelivery(ctx, args.DeliveryID)
			if err != nil {
				return toolError(err.Error())
			}
			if d == nil {
				return toolError("Webhook delivery not found")
			}
			return toolSuccess(d)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "list_webhook_event_types",
			Description: "List the public event types a webhook endpoint can subscribe to. \"*\" subscribes to every type.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWebhookEventTypesInput) (*mcp.CallToolResult, any, error) {
			types, err := resolver.DoWebhookEventTypes(ctx)
			if err != nil {
				return toolError(err.Error())
			}
			return toolSuccess(WebhookEventTypesResult{EventTypes: types})
		},
	)

	addTool(server,
		&mcp.Tool{
			Name: "create_webhook_endpoint",
			Description: "Create an outbound webhook endpoint for an https URL on a public host. At most 10 endpoints per tenant. " +
				"The signing secret is returned ONCE in the response. The endpoint receives only events that happen after it is created. " +
				"Requires confirm=\"" + confirmCreateWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args CreateWebhookEndpointToolInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmCreateWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			input := model.CreateWebhookEndpointInput{URL: args.URL, EventTypes: args.EventTypes}
			if args.Description != "" {
				input.Description = &args.Description
			}
			return webhookSecretToolResult(resolver.DoCreateWebhookEndpoint(ctx, input))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "update_webhook_endpoint",
			Description: "Change a webhook endpoint's URL, description, or event types. Omitted fields keep their value; event_types replaces the list. Requires confirm=\"" + confirmUpdateWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args UpdateWebhookEndpointToolInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmUpdateWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			input := model.UpdateWebhookEndpointInput{URL: args.URL, Description: args.Description}
			if len(args.EventTypes) > 0 {
				input.EventTypes = args.EventTypes
			}
			return webhookToolResult(resolver.DoUpdateWebhookEndpoint(ctx, args.EndpointID, input))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "delete_webhook_endpoint",
			Description: "Delete a webhook endpoint with its signing secrets and delivery log. Requires confirm=\"" + confirmDeleteWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ConfirmedWebhookEndpointInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmDeleteWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			return webhookToolResult(resolver.DoDeleteWebhookEndpoint(ctx, args.EndpointID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "enable_webhook_endpoint",
			Description: "Enable a disabled webhook endpoint. Deliveries skipped while it was disabled are not sent again; use replay_webhook_deliveries. Requires confirm=\"" + confirmEnableWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ConfirmedWebhookEndpointInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmEnableWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			return webhookToolResult(resolver.DoEnableWebhookEndpoint(ctx, args.EndpointID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "disable_webhook_endpoint",
			Description: "Disable a webhook endpoint. Its pending deliveries are skipped. Requires confirm=\"" + confirmDisableWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ConfirmedWebhookEndpointInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmDisableWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			return webhookToolResult(resolver.DoDisableWebhookEndpoint(ctx, args.EndpointID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name: "rotate_webhook_secret",
			Description: "Replace a webhook endpoint's signing secret and return the new one ONCE. The previous secret keeps signing alongside it for 24 hours unless revoke_previous is true. " +
				"Requires confirm=\"" + confirmRotateWebhookSecret + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args RotateWebhookSecretInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmRotateWebhookSecret); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			revoke := args.RevokePrevious
			return webhookSecretToolResult(resolver.DoRotateWebhookEndpointSecret(ctx, args.EndpointID, &revoke))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "test_webhook_endpoint",
			Description: "Send a signed webhook.test event to the endpoint's URL and return the HTTP attempt. At most one test per endpoint every 10 seconds. Requires confirm=\"" + confirmTestWebhookEndpoint + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ConfirmedWebhookEndpointInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmTestWebhookEndpoint); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			return webhookToolResult(resolver.DoTestWebhookEndpoint(ctx, args.EndpointID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "replay_webhook_delivery",
			Description: "Send a finished event delivery again under the same webhook-id. The endpoint must be enabled. Requires confirm=\"" + confirmReplayWebhookDelivery + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ReplayWebhookDeliveryInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmReplayWebhookDelivery); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			return webhookToolResult(resolver.DoReplayWebhookDelivery(ctx, args.DeliveryID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name: "replay_webhook_deliveries",
			Description: "Send again the failed and skipped deliveries of one endpoint created in [created_after, created_before), oldest first, at most 1000 per call. Call again while has_more is true. " +
				"Requires confirm=\"" + confirmReplayWebhookRange + "\".",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ReplayWebhookDeliveriesInput) (*mcp.CallToolResult, any, error) {
			if result, meta, err := requireConfirmation(args.Confirm, confirmReplayWebhookRange); result != nil || meta != nil || err != nil {
				return result, meta, err
			}
			after, err := time.Parse(time.RFC3339, strings.TrimSpace(args.CreatedAfter))
			if err != nil {
				return toolError("created_after must be an RFC 3339 time")
			}
			before, err := time.Parse(time.RFC3339, strings.TrimSpace(args.CreatedBefore))
			if err != nil {
				return toolError("created_before must be an RFC 3339 time")
			}
			return webhookToolResult(resolver.DoReplayWebhookDeliveries(ctx, args.EndpointID, after, before))
		},
	)
}

// ListWebhookEndpointsInput is the input for list_webhook_endpoints.
type ListWebhookEndpointsInput struct{}

// ListWebhookEventTypesInput is the input for list_webhook_event_types.
type ListWebhookEventTypesInput struct{}

// WebhookEventTypesResult is the result of list_webhook_event_types.
type WebhookEventTypesResult struct {
	EventTypes []string `json:"event_types"`
}

// WebhookEndpointIDInput identifies one endpoint.
type WebhookEndpointIDInput struct {
	EndpointID string `json:"endpoint_id" jsonschema:"Endpoint ID from list_webhook_endpoints"`
}

// WebhookDeliveryIDInput identifies one delivery.
type WebhookDeliveryIDInput struct {
	DeliveryID string `json:"delivery_id" jsonschema:"Delivery ID from list_webhook_deliveries"`
}

// ListWebhookDeliveriesInput is the input for list_webhook_deliveries.
type ListWebhookDeliveriesInput struct {
	EndpointID    string   `json:"endpoint_id,omitempty" jsonschema:"Only deliveries to this endpoint"`
	Statuses      []string `json:"statuses,omitempty" jsonschema:"Delivery statuses to include: pending, succeeded, failed, skipped. Omit for all."`
	EventType     string   `json:"event_type,omitempty" jsonschema:"Only deliveries of this event type, e.g. stream.live"`
	EventID       string   `json:"event_id,omitempty" jsonschema:"Only deliveries of this event ID"`
	CreatedAfter  string   `json:"created_after,omitempty" jsonschema:"RFC 3339 time; only deliveries created at or after it"`
	CreatedBefore string   `json:"created_before,omitempty" jsonschema:"RFC 3339 time; only deliveries created before it"`
	First         int      `json:"first,omitempty" jsonschema:"Page size from 1 through 200"`
	After         string   `json:"after,omitempty" jsonschema:"End cursor of the previous page"`
}

func (in ListWebhookDeliveriesInput) filter() (resolvers.WebhookDeliveryFilter, error) {
	var filter resolvers.WebhookDeliveryFilter
	if v := strings.TrimSpace(in.EndpointID); v != "" {
		filter.EndpointID = &v
	}
	if v := strings.TrimSpace(in.EventType); v != "" {
		filter.EventType = &v
	}
	if v := strings.TrimSpace(in.EventID); v != "" {
		filter.EventID = &v
	}
	for _, raw := range in.Statuses {
		st := model.WebhookDeliveryStatus(strings.ToUpper(strings.TrimSpace(raw)))
		if !st.IsValid() {
			return filter, errors.New("statuses accepts pending, succeeded, failed, or skipped")
		}
		filter.Statuses = append(filter.Statuses, st)
	}
	for _, bound := range []struct {
		raw, name string
		dst       **time.Time
	}{{in.CreatedAfter, "created_after", &filter.CreatedAfter}, {in.CreatedBefore, "created_before", &filter.CreatedBefore}} {
		if strings.TrimSpace(bound.raw) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(bound.raw))
		if err != nil {
			return filter, errors.New(bound.name + " must be an RFC 3339 time")
		}
		*bound.dst = &t
	}
	return filter, nil
}

// CreateWebhookEndpointToolInput is the input for create_webhook_endpoint.
type CreateWebhookEndpointToolInput struct {
	URL         string   `json:"url" jsonschema:"https URL of a public host"`
	Description string   `json:"description,omitempty" jsonschema:"Free-text description"`
	EventTypes  []string `json:"event_types" jsonschema:"Public event types to receive (see list_webhook_event_types); [\"*\"] receives every type"`
	Confirm     string   `json:"confirm" jsonschema:"Must be exactly 'CREATE WEBHOOK ENDPOINT'."`
}

// UpdateWebhookEndpointToolInput is the input for update_webhook_endpoint.
type UpdateWebhookEndpointToolInput struct {
	EndpointID  string   `json:"endpoint_id" jsonschema:"Endpoint ID from list_webhook_endpoints"`
	URL         *string  `json:"url,omitempty" jsonschema:"New https URL"`
	Description *string  `json:"description,omitempty" jsonschema:"New description"`
	EventTypes  []string `json:"event_types,omitempty" jsonschema:"Replaces the subscribed event types when non-empty"`
	Confirm     string   `json:"confirm" jsonschema:"Must be exactly 'UPDATE WEBHOOK ENDPOINT'."`
}

// ConfirmedWebhookEndpointInput identifies one endpoint for a confirmed change.
type ConfirmedWebhookEndpointInput struct {
	EndpointID string `json:"endpoint_id" jsonschema:"Endpoint ID from list_webhook_endpoints"`
	Confirm    string `json:"confirm" jsonschema:"The confirmation string named in the tool description"`
}

// RotateWebhookSecretInput is the input for rotate_webhook_secret.
type RotateWebhookSecretInput struct {
	EndpointID     string `json:"endpoint_id" jsonschema:"Endpoint ID from list_webhook_endpoints"`
	RevokePrevious bool   `json:"revoke_previous,omitempty" jsonschema:"Stop signing with the previous secret immediately instead of after 24 hours"`
	Confirm        string `json:"confirm" jsonschema:"Must be exactly 'ROTATE WEBHOOK SECRET'."`
}

// ReplayWebhookDeliveryInput is the input for replay_webhook_delivery.
type ReplayWebhookDeliveryInput struct {
	DeliveryID string `json:"delivery_id" jsonschema:"Delivery ID from list_webhook_deliveries"`
	Confirm    string `json:"confirm" jsonschema:"Must be exactly 'REPLAY WEBHOOK DELIVERY'."`
}

// ReplayWebhookDeliveriesInput is the input for replay_webhook_deliveries.
type ReplayWebhookDeliveriesInput struct {
	EndpointID    string `json:"endpoint_id" jsonschema:"Endpoint ID from list_webhook_endpoints"`
	CreatedAfter  string `json:"created_after" jsonschema:"RFC 3339 start of the range, inclusive"`
	CreatedBefore string `json:"created_before" jsonschema:"RFC 3339 end of the range, exclusive"`
	Confirm       string `json:"confirm" jsonschema:"Must be exactly 'REPLAY WEBHOOK DELIVERIES'."`
}

// WebhookSecretToolResult carries a newly issued signing secret.
type WebhookSecretToolResult struct {
	Endpoint *model.WebhookEndpoint `json:"endpoint"`
	Secret   string                 `json:"secret"`
	Warning  string                 `json:"warning"`
}

func webhookSecretToolResult(result any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return toolError(err.Error())
	}
	if secret, ok := result.(*model.WebhookEndpointSecret); ok {
		return toolSuccess(WebhookSecretToolResult{Endpoint: secret.Endpoint, Secret: secret.Secret, Warning: webhookSecretWarning})
	}
	return webhookToolResult(result, nil)
}

// webhookToolResult turns a webhook mutation union into a tool result: error
// members become tool errors, anything else is returned as is.
func webhookToolResult(result any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return toolError(err.Error())
	}
	switch value := result.(type) {
	case nil:
		return toolError("Webhook change returned no result")
	case *model.ValidationError:
		return toolError(value.Message)
	case *model.NotFoundError:
		return toolError(value.Message)
	case *model.RateLimitError:
		return toolError(value.Message)
	case *model.AuthError:
		return toolError(value.Message)
	default:
		return toolSuccess(value)
	}
}
