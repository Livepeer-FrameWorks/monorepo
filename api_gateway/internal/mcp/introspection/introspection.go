package introspection

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
)

// Client provides GraphQL schema introspection with caching.
type Client struct {
	graphqlURL string
	httpClient *http.Client
	logger     logging.Logger

	mu           sync.RWMutex
	cachedSchema *Schema
	cacheTime    time.Time
	cacheTTL     time.Duration
}

// NewClient creates a new introspection client.
func NewClient(graphqlURL string, logger logging.Logger) *Client {
	return &Client{
		graphqlURL: graphqlURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
		cacheTTL:   5 * time.Minute,
	}
}

// introspectionQuery is the full GraphQL introspection query.
const introspectionQuery = `
query IntrospectSchema {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      ...FullType
    }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args {
      ...InputValue
    }
    type {
      ...TypeRef
    }
    isDeprecated
    deprecationReason
  }
  inputFields {
    ...InputValue
  }
  interfaces {
    ...TypeRef
  }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes {
    ...TypeRef
  }
}

fragment InputValue on __InputValue {
  name
  description
  type {
    ...TypeRef
  }
  defaultValue
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
              }
            }
          }
        }
      }
    }
  }
}
`

// GetSchemaWithContext returns the cached schema, fetching it if needed.
// Requires auth context (JWT or API token) for the GraphQL endpoint.
func (c *Client) GetSchemaWithContext(ctx context.Context) (*Schema, error) {
	authHeader, err := authHeaderFromContext(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.RLock()
	if c.cachedSchema != nil && time.Since(c.cacheTime) < c.cacheTTL {
		schema := c.cachedSchema
		c.mu.RUnlock()
		return schema, nil
	}
	c.mu.RUnlock()

	return c.refreshSchema(authHeader)
}

// refreshSchema fetches a fresh schema from the GraphQL endpoint.
func (c *Client) refreshSchema(authHeader string) (*Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.cachedSchema != nil && time.Since(c.cacheTime) < c.cacheTTL {
		return c.cachedSchema, nil
	}

	// Build request
	reqBody, err := json.Marshal(map[string]string{
		"query": introspectionQuery,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal introspection query: %w", err)
	}

	req, err := http.NewRequest("POST", c.graphqlURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("introspection returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var result IntrospectionResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode introspection response: %w", err)
	}

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("introspection error: %s", result.Errors[0].Message)
	}

	// Cache and return
	c.cachedSchema = &result.Data.Schema
	c.cacheTime = time.Now()

	if c.logger != nil {
		c.logger.WithField("types", len(c.cachedSchema.Types)).Info("Schema introspection cached")
	}

	return c.cachedSchema, nil
}

// GetRootFields returns fields for a root operation type.
func (c *Client) GetRootFields(ctx context.Context, operationType string, depth int) ([]FieldSummary, error) {
	schema, err := c.GetSchemaWithContext(ctx)
	if err != nil {
		return nil, err
	}

	rootType, err := rootTypeForOperation(schema, operationType)
	if err != nil {
		return nil, err
	}

	typeIndex := buildTypeIndex(schema)
	return c.fieldsToSummary(rootType.Fields, depth, typeIndex, map[string]bool{rootType.Name: true}), nil
}

// GetType returns information about a specific type.
func (c *Client) GetType(ctx context.Context, typeName string, depth int) (*TypeSummary, error) {
	schema, err := c.GetSchemaWithContext(ctx)
	if err != nil {
		return nil, err
	}

	typeIndex := buildTypeIndex(schema)
	typeDef := typeIndex[typeName]
	if typeDef == nil {
		return nil, fmt.Errorf("type %s not found in schema", typeName)
	}

	summary := &TypeSummary{
		Name:        typeDef.Name,
		Kind:        typeDef.Kind,
		Description: typeDef.Description,
	}

	if typeDef.Kind == "ENUM" {
		for _, ev := range typeDef.EnumValues {
			summary.EnumValues = append(summary.EnumValues, ev.Name)
		}
		return summary, nil
	}

	if depth > 0 {
		summary.Fields = c.fieldsToSummary(typeDef.Fields, depth, typeIndex, map[string]bool{typeDef.Name: true})
	}

	return summary, nil
}

// FindFieldPath resolves a field path (dot-separated) against the schema.
func (c *Client) FindFieldPath(ctx context.Context, operationType, fieldPath string, depth int) (*FieldSummary, error) {
	schema, err := c.GetSchemaWithContext(ctx)
	if err != nil {
		return nil, err
	}

	rootType, err := rootTypeForOperation(schema, operationType)
	if err != nil {
		return nil, err
	}

	typeIndex := buildTypeIndex(schema)
	segments := strings.Split(fieldPath, ".")
	if len(segments) == 0 || segments[0] == "" {
		return nil, fmt.Errorf("invalid field path")
	}

	currentType := rootType
	visited := map[string]bool{currentType.Name: true}

	for i, segment := range segments {
		field := findFieldByName(currentType.Fields, segment)
		if field == nil {
			return nil, fmt.Errorf("field %s not found on type %s", segment, currentType.Name)
		}

		if i == len(segments)-1 {
			summary := c.fieldToSummary(*field, depth, typeIndex, visited)
			return &summary, nil
		}

		baseType := field.Type.GetBaseTypeName()
		nextType := typeIndex[baseType]
		if nextType == nil {
			return nil, fmt.Errorf("type %s not found for field %s", baseType, segment)
		}

		currentType = nextType
	}

	return nil, fmt.Errorf("field path not found")
}

func rootTypeForOperation(schema *Schema, operationType string) (*FullType, error) {
	var typeName string
	switch operationType {
	case "query":
		if schema.QueryType != nil {
			typeName = schema.QueryType.Name
		}
	case "mutation":
		if schema.MutationType != nil {
			typeName = schema.MutationType.Name
		}
	case "subscription":
		if schema.SubscriptionType != nil {
			typeName = schema.SubscriptionType.Name
		}
	default:
		return nil, fmt.Errorf("unknown operation type: %s", operationType)
	}

	if typeName == "" {
		return nil, fmt.Errorf("schema has no %s type", operationType)
	}

	for i := range schema.Types {
		if schema.Types[i].Name == typeName {
			return &schema.Types[i], nil
		}
	}

	return nil, fmt.Errorf("type %s not found in schema", typeName)
}

func buildTypeIndex(schema *Schema) map[string]*FullType {
	index := make(map[string]*FullType, len(schema.Types))
	for i := range schema.Types {
		typeDef := &schema.Types[i]
		if typeDef.Name != "" {
			index[typeDef.Name] = typeDef
		}
	}
	return index
}

func findFieldByName(fields []Field, name string) *Field {
	for i := range fields {
		if fields[i].Name == name {
			return &fields[i]
		}
	}
	return nil
}

// fieldsToSummary converts fields to summaries with depth control.
func (c *Client) fieldsToSummary(fields []Field, depth int, typeIndex map[string]*FullType, visited map[string]bool) []FieldSummary {
	result := make([]FieldSummary, 0, len(fields))

	for _, f := range fields {
		// Skip internal fields
		if len(f.Name) > 1 && f.Name[0] == '_' && f.Name[1] == '_' {
			continue
		}

		summary := c.fieldToSummary(f, depth, typeIndex, visited)
		result = append(result, summary)
	}

	return result
}

func (c *Client) fieldToSummary(f Field, depth int, typeIndex map[string]*FullType, visited map[string]bool) FieldSummary {
	summary := FieldSummary{
		Name: f.Name,
	}

	if depth >= 2 {
		summary.Description = f.Description
		summary.ReturnType = f.Type.GetTypeName()
		summary.Deprecated = f.IsDeprecated

		for _, arg := range f.Args {
			summary.Args = append(summary.Args, ArgSummary{
				Name:     arg.Name,
				Type:     arg.Type.GetTypeName(),
				Required: arg.Type.IsRequired(),
				Default:  defaultValueString(arg.DefaultValue),
			})
		}
	}

	if depth >= 3 {
		baseType := f.Type.GetBaseTypeName()
		if baseType != "" && !isScalarOrEnum(baseType, typeIndex) {
			if visited[baseType] {
				return summary
			}
			if typeDef := typeIndex[baseType]; typeDef != nil {
				visited[baseType] = true
				summary.Fields = c.fieldsToSummary(typeDef.Fields, depth-1, typeIndex, visited)
				delete(visited, baseType)
			}
		}
	}

	return summary
}

func isScalarOrEnum(typeName string, typeIndex map[string]*FullType) bool {
	ref := TypeRef{Name: &typeName}
	if ref.IsScalar() {
		return true
	}

	if typeDef := typeIndex[typeName]; typeDef != nil {
		return typeDef.Kind == "ENUM"
	}

	return false
}

func authHeaderFromContext(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("missing auth context")
	}
	if token := ctxkeys.GetJWTToken(ctx); token != "" {
		return "Bearer " + token, nil
	}
	if token := ctxkeys.GetAPIToken(ctx); token != "" {
		return "Bearer " + token, nil
	}
	return "", fmt.Errorf("missing auth token (jwt or api token) for schema introspection")
}

func defaultValueString(defaultValue *string) string {
	if defaultValue == nil {
		return ""
	}
	return *defaultValue
}
