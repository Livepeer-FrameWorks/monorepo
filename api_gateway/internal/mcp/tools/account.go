// Package tools implements MCP tools for the FrameWorks platform.
package tools

import (
	"context"
	"fmt"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAccountTools registers account-related MCP tools.
func RegisterAccountTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// update_billing_details - Always allowed
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "update_billing_details",
			Description: "Update billing details (company, address, VAT). Required before any payments or billable operations.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args UpdateBillingDetailsInput) (*mcp.CallToolResult, any, error) {
			return handleUpdateBillingDetails(ctx, args, clients, logger)
		},
	)
}

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

// BillingDetailsResult represents the result of updating billing details.
type BillingDetailsResult struct {
	Success         bool   `json:"success"`
	DetailsComplete bool   `json:"details_complete"`
	Message         string `json:"message"`
}

func handleUpdateBillingDetails(ctx context.Context, args UpdateBillingDetailsInput, clients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, nil, fmt.Errorf("not authenticated")
	}

	// Validate required fields
	if args.Line1 == "" || args.City == "" || args.PostalCode == "" || args.Country == "" {
		return toolError("Missing required fields: address_line1, city, postal_code, and country are required")
	}

	// Validate country code format (2 letters)
	if len(args.Country) != 2 {
		return toolError("Country must be a 2-letter ISO country code (e.g., US, DE, NL)")
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
			Country:    args.Country,
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
		Message:         "Billing details updated successfully. You can now top up your balance and perform billable operations.",
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
}

// strPtr returns a pointer to a string, or nil if empty.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
