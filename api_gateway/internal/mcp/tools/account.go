// Package tools implements MCP tools for the FrameWorks platform.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/countries"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAccountTools registers account-related MCP tools.
func RegisterAccountTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_tenant_settings",
			Description: "Read the current tenant's account identity and routing settings. Agents can use this to inspect wallet-provisioned accounts before changing streams, billing, or edge routing.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetTenantSettingsInput) (*mcp.CallToolResult, any, error) {
			return handleGetTenantSettings(ctx, resolver, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_tenant_settings",
			Description: "Update the current tenant's account identity and routing settings. Supports tenant name, preferred primary cluster, and deployment model.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateTenantSettingsInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateTenantSettings(ctx, args, resolver, logger)
		},
	)

	// update_billing_details - Always allowed
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_billing_details",
			Description: "Update billing details (company, address, VAT). Required for payments over €100 and for proper invoicing.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateBillingDetailsInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateBillingDetails(ctx, args, clients, logger)
		},
	)
}

type GetTenantSettingsInput struct{}

// UpdateBillingDetailsInput represents input for update_billing_details tool.
type UpdateBillingDetailsInput struct {
	Email      string `json:"email,omitempty" jsonschema_description:"Billing email address"`
	Company    string `json:"company,omitempty" jsonschema_description:"Company or organization name"`
	VATNumber  string `json:"vat_number,omitempty" jsonschema_description:"VAT number (EU) or tax ID"`
	Line1      string `json:"address_line1" jsonschema:"required" jsonschema_description:"Street address line 1"`
	Line2      string `json:"address_line2,omitempty" jsonschema_description:"Street address line 2"`
	City       string `json:"city" jsonschema:"required" jsonschema_description:"City"`
	PostalCode string `json:"postal_code" jsonschema:"required" jsonschema_description:"Postal/ZIP code"`
	Country    string `json:"country" jsonschema:"required" jsonschema_description:"Country code (ISO 3166-1 alpha-2)"`
}

type UpdateTenantSettingsInput struct {
	Name             string `json:"name,omitempty" jsonschema_description:"Tenant display name"`
	PrimaryClusterID string `json:"primary_cluster_id,omitempty" jsonschema_description:"Preferred primary cluster for ingest/routing decisions"`
	DeploymentModel  string `json:"deployment_model,omitempty" jsonschema_description:"Deployment model, for example shared, dedicated, or self_hosted"`
}

// BillingDetailsResult represents the result of updating billing details.
type BillingDetailsResult struct {
	Success         bool   `json:"success"`
	DetailsComplete bool   `json:"details_complete"`
	Message         string `json:"message"`
}

type TenantSettingsResult struct {
	TenantID              string `json:"tenant_id"`
	Name                  string `json:"name"`
	DeploymentModel       string `json:"deployment_model"`
	DeploymentTier        string `json:"deployment_tier"`
	PrimaryDeploymentTier string `json:"primary_deployment_tier"`
	PrimaryClusterID      string `json:"primary_cluster_id,omitempty"`
	OfficialClusterID     string `json:"official_cluster_id,omitempty"`
	IsActive              bool   `json:"is_active"`
	Message               string `json:"message"`
}

func (r TenantSettingsResult) String() string {
	if r.Message != "" {
		return r.Message
	}
	return fmt.Sprintf("Tenant %s (%s)", r.Name, r.TenantID)
}

func handleGetTenantSettings(ctx context.Context, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}
	tenant, err := resolver.DoGetTenant(ctx)
	if err != nil {
		logger.WithError(err).Warn("Failed to read tenant settings")
		return toolError(fmt.Sprintf("Failed to read tenant settings: %v", err))
	}
	return toolSuccess(tenantSettingsResult(tenant, "Tenant settings loaded."))
}

func handleUpdateTenantSettings(ctx context.Context, args UpdateTenantSettingsInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	name := strings.TrimSpace(args.Name)
	primaryClusterID := strings.TrimSpace(args.PrimaryClusterID)
	deploymentModel := strings.TrimSpace(args.DeploymentModel)
	if name == "" && primaryClusterID == "" && deploymentModel == "" {
		return toolError("Provide at least one of name, primary_cluster_id, or deployment_model")
	}

	input := model.UpdateTenantInput{}
	if name != "" {
		input.Name = &name
	}
	settings := map[string]string{}
	if primaryClusterID != "" {
		settings["primaryClusterId"] = primaryClusterID
	}
	if deploymentModel != "" {
		settings["deploymentModel"] = deploymentModel
	}
	if len(settings) > 0 {
		raw, err := json.Marshal(settings)
		if err != nil {
			return toolError(fmt.Sprintf("Failed to encode tenant settings: %v", err))
		}
		settingsJSON := string(raw)
		input.Settings = &settingsJSON
	}

	tenant, err := resolver.DoUpdateTenant(ctx, input)
	if err != nil {
		logger.WithError(err).Warn("Failed to update tenant settings")
		return toolError(fmt.Sprintf("Failed to update tenant settings: %v", err))
	}
	return toolSuccess(tenantSettingsResult(tenant, "Tenant settings updated."))
}

func tenantSettingsResult(tenant *pb.Tenant, message string) TenantSettingsResult {
	result := TenantSettingsResult{Message: message}
	if tenant == nil {
		return result
	}
	result.TenantID = tenant.GetId()
	result.Name = tenant.GetName()
	result.DeploymentModel = tenant.GetDeploymentModel()
	result.DeploymentTier = tenant.GetDeploymentTier()
	result.PrimaryDeploymentTier = tenant.GetPrimaryDeploymentTier()
	result.PrimaryClusterID = tenant.GetPrimaryClusterId()
	result.OfficialClusterID = tenant.GetOfficialClusterId()
	result.IsActive = tenant.GetIsActive()
	return result
}

func handleUpdateBillingDetails(ctx context.Context, args UpdateBillingDetailsInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil, mcperrors.AuthRequired()
	}

	// Validate required fields
	if args.Line1 == "" || args.City == "" || args.PostalCode == "" || args.Country == "" {
		return toolError("Missing required fields: address_line1, city, postal_code, and country are required")
	}

	// Validate and normalize country code
	countryCode := countries.Normalize(args.Country)
	if !countries.IsValid(countryCode) {
		return toolError(fmt.Sprintf("Invalid country code %q: must be a valid ISO 3166-1 alpha-2 code (e.g., US, DE, NL)", args.Country))
	}

	// Build address if provided
	var address *pb.BillingAddress
	if args.Line1 != "" || args.City != "" {
		street := args.Line1
		if args.Line2 != "" {
			street = args.Line1 + "\n" + args.Line2
		}
		address = &pb.BillingAddress{
			Street:     street,
			City:       args.City,
			PostalCode: args.PostalCode,
			Country:    countryCode,
		}
	}

	// Call Purser to update billing details
	updated, err := clients.Purser.UpdateBillingDetails(ctx, &pb.UpdateBillingDetailsRequest{
		TenantId:  tenantID,
		Email:     strPtr(args.Email),
		Company:   strPtr(args.Company),
		VatNumber: strPtr(args.VATNumber),
		Address:   address,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to update billing details")
		return toolError(fmt.Sprintf("Failed to update billing details: %v", err))
	}

	result := BillingDetailsResult{
		Success:         true,
		DetailsComplete: updated.IsComplete,
		Message:         "Billing details updated successfully.",
	}

	return toolSuccess(result)
}

// toolError returns an error result for a tool call.
func toolError(message string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: message},
		},
		IsError: true,
	}, nil, nil
}

// toolSuccess returns a success result for a tool call.
func toolSuccess(result interface{}) (*mcp.CallToolResult, any, error) {
	text := fmt.Sprintf("%+v", result)
	if r, ok := result.(fmt.Stringer); ok {
		text = r.String()
	} else if r, ok := result.(BillingDetailsResult); ok {
		text = r.Message
	} else if r, ok := result.(TopupResult); ok {
		text = fmt.Sprintf("Top-up initiated. Deposit address: %s (%s). Amount: %d cents. Expires: %s",
			r.DepositAddress, r.Asset, r.AmountCents, r.ExpiresAt)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: text},
		},
	}, result, nil
}

// TopupResult is defined here for use in toolSuccess
type TopupResult struct {
	TopupID        string `json:"topup_id"`
	DepositAddress string `json:"deposit_address"`
	Asset          string `json:"asset"`
	AmountCents    int64  `json:"amount_cents"`
	ExpiresAt      string `json:"expires_at"`
	Message        string `json:"message"`

	// Locked quote — agents send exactly TokenAmount of Asset, get credited at PriceUSD per token.
	TokenAmount string `json:"token_amount,omitempty"`
	PriceUSD    string `json:"price_usd,omitempty"`
	QuoteSource string `json:"quote_source,omitempty"`
	Network     string `json:"network,omitempty"`
}

// strPtr returns a pointer to a string, or nil if empty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
