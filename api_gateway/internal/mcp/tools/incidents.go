package tools

import (
	"context"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/resolvers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterIncidentTools registers the tenant incident tools. They run through
// the GraphQL resolvers so access checks and Lookout identity handling match
// the API exactly.
func RegisterIncidentTools(server *mcp.Server, resolver *resolvers.Resolver) {
	addTool(server,
		&mcp.Tool{
			Name:        "list_incidents",
			Description: "List incidents raised by platform alerting on clusters your tenant owns, newest first. Filter by status (firing, acknowledged, resolved) or cluster.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ListIncidentsInput) (*mcp.CallToolResult, any, error) {
			filter := &model.IncidentFilterInput{}
			for _, raw := range args.Statuses {
				status, ok := incidentStatusArg(raw)
				if !ok {
					return toolError("statuses accepts firing, acknowledged, or resolved")
				}
				filter.Statuses = append(filter.Statuses, status)
			}
			if clusterID := strings.TrimSpace(args.ClusterID); clusterID != "" {
				filter.ClusterID = &clusterID
			}
			page := &model.ConnectionInput{}
			if args.First > 0 {
				page.First = &args.First
			}
			if args.After != "" {
				page.After = &args.After
			}
			conn, err := resolver.DoIncidentsConnection(ctx, page, filter)
			if err != nil {
				return toolError(err.Error())
			}
			return toolSuccess(conn)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "get_incident",
			Description: "Get one incident with its alerts (labels, annotations, firing state) and timeline, including any attached Skipper investigation report ID.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args IncidentIDInput) (*mcp.CallToolResult, any, error) {
			detail, err := resolver.DoIncident(ctx, args.IncidentID)
			if err != nil {
				return toolError(err.Error())
			}
			if detail == nil {
				return toolError("Incident not found")
			}
			return toolSuccess(detail)
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "acknowledge_incident",
			Description: "Acknowledge a firing incident. It stays open until its alerts resolve or it is resolved.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args IncidentIDInput) (*mcp.CallToolResult, any, error) {
			return incidentMutationResult(resolver.DoAcknowledgeIncident(ctx, args.IncidentID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "assign_incident",
			Description: "Assign an incident to yourself, or clear its assignment with unassign=true. Incidents cannot be assigned to other users.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AssignIncidentInput) (*mcp.CallToolResult, any, error) {
			var assignee *string
			if !args.Unassign {
				self := userIDFromToolContext(ctx)
				if self == "" {
					return toolError("Assigning requires a signed-in user")
				}
				assignee = &self
			}
			return incidentMutationResult(resolver.DoAssignIncident(ctx, args.IncidentID, assignee))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "resolve_incident",
			Description: "Resolve an incident manually. Repeats of the same firing alerts do not reopen it; a new alert opens a new incident.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args IncidentIDInput) (*mcp.CallToolResult, any, error) {
			return incidentMutationResult(resolver.DoResolveIncident(ctx, args.IncidentID))
		},
	)

	addTool(server,
		&mcp.Tool{
			Name:        "add_incident_note",
			Description: "Add a note to an incident's timeline, for example what was checked or changed.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AddIncidentNoteInput) (*mcp.CallToolResult, any, error) {
			return incidentMutationResult(resolver.DoAddIncidentNote(ctx, args.IncidentID, args.Body))
		},
	)
}

// ListIncidentsInput is the input for list_incidents.
type ListIncidentsInput struct {
	Statuses  []string `json:"statuses,omitempty" jsonschema:"Incident statuses to include: firing, acknowledged, resolved. Omit for all."`
	ClusterID string   `json:"cluster_id,omitempty" jsonschema:"Only incidents on this cluster"`
	First     int      `json:"first,omitempty" jsonschema:"Page size from 1 through 200"`
	After     string   `json:"after,omitempty" jsonschema:"End cursor of the previous page"`
}

// IncidentIDInput identifies one incident.
type IncidentIDInput struct {
	IncidentID string `json:"incident_id" jsonschema:"Incident ID from list_incidents"`
}

// AssignIncidentInput is the input for assign_incident.
type AssignIncidentInput struct {
	IncidentID string `json:"incident_id" jsonschema:"Incident ID from list_incidents"`
	Unassign   bool   `json:"unassign,omitempty" jsonschema:"Clear the assignment instead of assigning the incident to yourself"`
}

func userIDFromToolContext(ctx context.Context) string {
	return strings.TrimSpace(ctxkeys.GetUserID(ctx))
}

// AddIncidentNoteInput is the input for add_incident_note.
type AddIncidentNoteInput struct {
	IncidentID string `json:"incident_id" jsonschema:"Incident ID from list_incidents"`
	Body       string `json:"body" jsonschema:"Note text, at most 10000 bytes"`
}

func incidentStatusArg(raw string) (model.IncidentStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "firing":
		return model.IncidentStatusFiring, true
	case "acknowledged":
		return model.IncidentStatusAcknowledged, true
	case "resolved":
		return model.IncidentStatusResolved, true
	default:
		return "", false
	}
}

func incidentMutationResult(result model.IncidentMutationResult, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return toolError(err.Error())
	}
	switch value := result.(type) {
	case *model.Incident:
		return toolSuccess(value)
	case *model.ValidationError:
		return toolError(value.Message)
	case *model.NotFoundError:
		return toolError(value.Message)
	case *model.AuthError:
		return toolError(value.Message)
	default:
		return toolError("Incident change returned an unsupported result")
	}
}
