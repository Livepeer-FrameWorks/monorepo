package resources

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/introspection"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	catalogIntrospectionClient *introspection.Client
	catalogTemplateLoader      *introspection.TemplateLoader
)

// CatalogTemplateRef references a template that can satisfy a field path.
type CatalogTemplateRef struct {
	Name          string   `json:"name"`
	FilePath      string   `json:"file_path"`
	OperationType string   `json:"operation_type"`
	FieldPaths    []string `json:"field_paths,omitempty"`
}

// CatalogEntry represents a merged schema + template + curated entry.
type CatalogEntry struct {
	ID            string                     `json:"id"`
	OperationType string                     `json:"operation_type"`
	FieldPath     string                     `json:"field_path"`
	Description   string                     `json:"description,omitempty"`
	ReturnType    string                     `json:"return_type,omitempty"`
	Args          []introspection.ArgSummary `json:"args,omitempty"`
	Templates     []CatalogTemplateRef       `json:"templates,omitempty"`
	Tags          []string                   `json:"tags,omitempty"`
	Curated       bool                       `json:"curated"`
	Missing       bool                       `json:"missing"`
}

// CatalogExample represents a curated query example.
type CatalogExample struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	OperationType string   `json:"operation_type"`
	FieldPath     string   `json:"field_path"`
	EntryID       string   `json:"entry_id"`
	Tags          []string `json:"tags,omitempty"`
}

// CatalogSection represents a group of related examples.
type CatalogSection struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Examples    []CatalogExample `json:"examples"`
}

// APICatalog represents the full curated API catalog.
type APICatalog struct {
	Entries  []CatalogEntry   `json:"entries"`
	Sections []CatalogSection `json:"sections"`
	Hint     string           `json:"hint"`
}

// RegisterAPISchemaResources registers API schema resources.
func RegisterAPISchemaResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	graphqlURL := os.Getenv("GRAPHQL_URL")
	if graphqlURL == "" {
		graphqlURL = "http://localhost:8080/graphql/"
	}
	catalogIntrospectionClient = introspection.NewClient(graphqlURL, logger)

	catalogTemplateLoader = introspection.NewTemplateLoader()
	if err := catalogTemplateLoader.Load(); err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Failed to load GraphQL templates for catalog")
		}
	}

	server.AddResource(&mcp.Resource{
		URI:         "schema://catalog",
		Name:        "API Catalog",
		Description: "Merged schema + template + curated catalog for the FrameWorks GraphQL API.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleAPICatalog(ctx, logger)
	})
}

func handleAPICatalog(ctx context.Context, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	sections := curatedCatalogSections()

	curatedEntries := make(map[string]*CatalogEntry)
	curatedTags := make(map[string][]string)
	curatedIDs := make(map[string]bool)

	for i := range sections {
		for j := range sections[i].Examples {
			ex := &sections[i].Examples[j]
			ex.EntryID = entryID(ex.OperationType, ex.FieldPath)
			curatedIDs[ex.EntryID] = true
			if len(ex.Tags) > 0 {
				curatedTags[ex.EntryID] = append(curatedTags[ex.EntryID], ex.Tags...)
			}
			if _, exists := curatedEntries[ex.EntryID]; !exists {
				curatedEntries[ex.EntryID] = &CatalogEntry{
					ID:            ex.EntryID,
					OperationType: ex.OperationType,
					FieldPath:     ex.FieldPath,
					Curated:       true,
				}
			}
		}
	}

	// Build template index by field path
	templateRefsByKey := make(map[string][]CatalogTemplateRef)
	if catalogTemplateLoader != nil {
		for _, tmpl := range catalogTemplateLoader.GetAll() {
			ref := CatalogTemplateRef{
				Name:          tmpl.Name,
				FilePath:      tmpl.FilePath,
				OperationType: tmpl.OperationType,
				FieldPaths:    tmpl.FieldPaths,
			}
			for _, path := range tmpl.FieldPaths {
				if path == "" {
					continue
				}
				if !includeCatalogPath(path, false) {
					continue
				}
				key := entryID(tmpl.OperationType, path)
				templateRefsByKey[key] = append(templateRefsByKey[key], ref)
			}
		}
	}

	// Build entry path set from schema + curated + templates
	entryKeys := make(map[string]bool)
	operations := []string{"query", "mutation", "subscription"}

	for _, opType := range operations {
		fields, err := catalogIntrospectionClient.GetRootFields(ctx, opType, 2)
		if err != nil {
			return nil, err
		}
		for _, field := range fields {
			entryKeys[entryID(opType, field.Name)] = true
		}
	}

	for key := range curatedIDs {
		entryKeys[key] = true
	}

	for key := range templateRefsByKey {
		entryKeys[key] = true
	}

	entries := make([]CatalogEntry, 0, len(entryKeys))

	for key := range entryKeys {
		opType, fieldPath := splitEntryID(key)
		if opType == "" || fieldPath == "" {
			continue
		}

		entry := CatalogEntry{
			ID:            key,
			OperationType: opType,
			FieldPath:     fieldPath,
			Curated:       curatedIDs[key],
			Templates:     templateRefsByKey[key],
			Tags:          dedupeStrings(curatedTags[key]),
		}

		if !includeCatalogPath(fieldPath, entry.Curated) {
			continue
		}

		fieldSummary, err := catalogIntrospectionClient.FindFieldPath(ctx, opType, fieldPath, 2)
		if err != nil {
			entry.Missing = true
			if logger != nil {
				logger.WithError(err).WithField("field_path", fieldPath).Debug("Catalog field path missing from schema")
			}
		} else {
			entry.Description = fieldSummary.Description
			entry.ReturnType = fieldSummary.ReturnType
			entry.Args = fieldSummary.Args
		}

		if curatedEntry, ok := curatedEntries[key]; ok {
			entry.Curated = true
			if entry.Tags == nil {
				entry.Tags = curatedEntry.Tags
			}
		}

		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].OperationType == entries[j].OperationType {
			return entries[i].FieldPath < entries[j].FieldPath
		}
		return entries[i].OperationType < entries[j].OperationType
	})

	catalog := APICatalog{
		Entries:  entries,
		Sections: sections,
		Hint:     "Use generate_query with field_path to get a ready-to-use query. Use introspect_schema for deeper type exploration.",
	}

	return marshalResourceResult("schema://catalog", catalog)
}

func entryID(operationType, fieldPath string) string {
	return fmt.Sprintf("%s:%s", operationType, fieldPath)
}

func splitEntryID(id string) (string, string) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func includeCatalogPath(fieldPath string, curated bool) bool {
	if curated {
		return true
	}
	segments := strings.Split(fieldPath, ".")
	if len(segments) > 4 {
		return false
	}
	last := segments[len(segments)-1]
	if last == "" {
		return false
	}
	blocked := map[string]bool{
		"edges":      true,
		"edge":       true,
		"node":       true,
		"nodes":      true,
		"pageInfo":   true,
		"totalCount": true,
		"cursor":     true,
		"__typename": true,
	}
	return !blocked[last]
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		result = append(result, v)
	}
	return result
}

func curatedCatalogSections() []CatalogSection {
	return []CatalogSection{
		{
			ID:          "analytics-overview",
			Title:       "Analytics Overview",
			Description: "Top-level rollups and usage surfaces.",
			Examples: []CatalogExample{
				{ID: "platform-overview", Title: "Platform Overview", Description: "High-level totals + daily stats", OperationType: "query", FieldPath: "analytics.overview", Tags: []string{"core", "analytics"}},
				{ID: "usage-records", Title: "Usage Records", Description: "Billing usage line items by time range", OperationType: "query", FieldPath: "usageRecordsConnection", Tags: []string{"core", "billing"}},
				{ID: "viewer-hours-hourly", Title: "Viewer Hours (Hourly)", Description: "Hourly viewer hours across streams", OperationType: "query", FieldPath: "analytics.usage.streaming.viewerHoursHourlyConnection", Tags: []string{"analytics"}},
				{ID: "storage-usage", Title: "Storage Usage", Description: "Hot/cold storage usage by artifact class", OperationType: "query", FieldPath: "analytics.usage.storage.storageUsageConnection", Tags: []string{"analytics"}},
				{ID: "processing-usage", Title: "Processing Usage", Description: "Transcode/processing usage summaries", OperationType: "query", FieldPath: "analytics.usage.processing.processingUsageConnection", Tags: []string{"analytics"}},
			},
		},
		{
			ID:          "analytics-health",
			Title:       "Health & QoE",
			Description: "Stream health, QoE metrics, and rebuffering.",
			Examples: []CatalogExample{
				{ID: "stream-health", Title: "Stream Health", Description: "Detailed health + client QoE for a stream", OperationType: "query", FieldPath: "analytics.health.streamHealthConnection", Tags: []string{"core", "health"}},
				{ID: "rebuffering-events", Title: "Rebuffering Events", Description: "Rebuffering transitions for a stream", OperationType: "query", FieldPath: "analytics.health.rebufferingEventsConnection", Tags: []string{"health"}},
				{ID: "stream-overview", Title: "Stream Overview Analytics", Description: "Stream metrics + hourly connections", OperationType: "query", FieldPath: "analytics.usage.streaming.streamAnalyticsSummary", Tags: []string{"core", "analytics"}},
			},
		},
		{
			ID:          "streams",
			Title:       "Stream Management",
			Description: "Create, list, and manage live streams.",
			Examples: []CatalogExample{
				{ID: "streams-list", Title: "List Streams", Description: "Paginated list of all streams", OperationType: "query", FieldPath: "streamsConnection", Tags: []string{"core"}},
				{ID: "stream-detail", Title: "Stream Details", Description: "Full details for a single stream", OperationType: "query", FieldPath: "stream", Tags: []string{"core"}},
				{ID: "stream-keys", Title: "Stream Keys", Description: "Ingest keys for a stream", OperationType: "query", FieldPath: "streamKeysConnection", Tags: []string{"core"}},
			},
		},
		{
			ID:          "lifecycle",
			Title:       "Lifecycle & Events",
			Description: "Stream, viewer, and artifact lifecycle events.",
			Examples: []CatalogExample{
				{ID: "stream-events", Title: "Stream Events", Description: "Lifecycle events for a stream", OperationType: "query", FieldPath: "analytics.lifecycle.streamEventsConnection", Tags: []string{"core", "lifecycle"}},
				{ID: "connection-events", Title: "Viewer Connection Events", Description: "Connect/disconnect events with geo", OperationType: "query", FieldPath: "analytics.lifecycle.connectionEventsConnection", Tags: []string{"lifecycle"}},
				{ID: "stream-sessions", Title: "Viewer Sessions", Description: "Session details by stream", OperationType: "query", FieldPath: "analytics.lifecycle.viewerSessionsConnection", Tags: []string{"lifecycle"}},
			},
		},
		{
			ID:          "infrastructure",
			Title:       "Infrastructure",
			Description: "Nodes, routing, and service health.",
			Examples: []CatalogExample{
				{ID: "clusters-list", Title: "Clusters", Description: "Clusters for the tenant", OperationType: "query", FieldPath: "clustersConnection", Tags: []string{"core", "infra"}},
				{ID: "nodes-list", Title: "Nodes List", Description: "All infrastructure nodes", OperationType: "query", FieldPath: "nodesConnection", Tags: []string{"infra"}},
				{ID: "routing-events", Title: "Routing Events", Description: "Load-balancer routing decisions", OperationType: "query", FieldPath: "analytics.infra.routingEventsConnection", Tags: []string{"infra"}},
			},
		},
		{
			ID:          "subscriptions",
			Title:       "Live Subscriptions",
			Description: "Real-time events via WebSocket. Combine with historical queries for context.",
			Examples: []CatalogExample{
				{ID: "live-stream-events", Title: "Stream Events (Live)", Description: "Stream lifecycle events as they happen", OperationType: "subscription", FieldPath: "liveStreamEvents", Tags: []string{"core", "realtime"}},
				{ID: "live-viewer-metrics", Title: "Viewer Metrics (Live)", Description: "Per-stream viewer connect/disconnect", OperationType: "subscription", FieldPath: "liveViewerMetrics", Tags: []string{"core", "realtime"}},
				{ID: "live-system-health", Title: "System Health", Description: "Cluster health pings and outages", OperationType: "subscription", FieldPath: "liveSystemHealth", Tags: []string{"infra", "realtime"}},
			},
		},
		{
			ID:          "mutations",
			Title:       "Mutations",
			Description: "Create streams, clips, DVR, and assets.",
			Examples: []CatalogExample{
				{ID: "create-stream", Title: "Create Stream", Description: "Create a new live stream", OperationType: "mutation", FieldPath: "createStream", Tags: []string{"core"}},
				{ID: "start-dvr", Title: "Start DVR", Description: "Begin DVR recording for a stream", OperationType: "mutation", FieldPath: "startDVR", Tags: []string{"core"}},
				{ID: "create-clip", Title: "Create Clip", Description: "Create a clip from a stream", OperationType: "mutation", FieldPath: "createClip", Tags: []string{"core"}},
			},
		},
		{
			ID:          "playback",
			Title:       "Playback",
			Description: "Resolve viewer endpoints for HLS/DASH/WebRTC playback.",
			Examples: []CatalogExample{
				{ID: "resolve-endpoint", Title: "Resolve Viewer Endpoint", Description: "Get playback URLs with geo-routing", OperationType: "query", FieldPath: "resolveViewerEndpoint", Tags: []string{"core", "playback"}},
			},
		},
	}
}
