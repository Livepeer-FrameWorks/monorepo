// Package introspection provides GraphQL schema introspection for MCP API integration tools.
package introspection

// IntrospectionResult is the response from a GraphQL introspection query.
type IntrospectionResult struct {
	Data   IntrospectionData    `json:"data"`
	Errors []IntrospectionError `json:"errors,omitempty"`
}

// IntrospectionError represents a GraphQL error.
type IntrospectionError struct {
	Message string `json:"message"`
}

// IntrospectionData holds the __schema field.
type IntrospectionData struct {
	Schema Schema `json:"__schema"`
}

// Schema is the GraphQL schema as returned by introspection.
type Schema struct {
	QueryType        *TypeName  `json:"queryType"`
	MutationType     *TypeName  `json:"mutationType"`
	SubscriptionType *TypeName  `json:"subscriptionType"`
	Types            []FullType `json:"types"`
}

// TypeName is a simple name reference.
type TypeName struct {
	Name string `json:"name"`
}

// FullType represents a complete GraphQL type definition.
type FullType struct {
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	Fields        []Field      `json:"fields,omitempty"`
	InputFields   []InputValue `json:"inputFields,omitempty"`
	Interfaces    []TypeRef    `json:"interfaces,omitempty"`
	EnumValues    []EnumValue  `json:"enumValues,omitempty"`
	PossibleTypes []TypeRef    `json:"possibleTypes,omitempty"`
}

// Field represents a field on an object or interface type.
type Field struct {
	Name              string       `json:"name"`
	Description       string       `json:"description,omitempty"`
	Args              []InputValue `json:"args,omitempty"`
	Type              TypeRef      `json:"type"`
	IsDeprecated      bool         `json:"isDeprecated"`
	DeprecationReason string       `json:"deprecationReason,omitempty"`
}

// InputValue represents an argument or input field.
type InputValue struct {
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	Type         TypeRef `json:"type"`
	DefaultValue *string `json:"defaultValue,omitempty"`
}

// TypeRef is a recursive type reference (handles NON_NULL, LIST wrappers).
type TypeRef struct {
	Kind   string   `json:"kind"`
	Name   *string  `json:"name,omitempty"`
	OfType *TypeRef `json:"ofType,omitempty"`
}

// EnumValue represents a value in an enum type.
type EnumValue struct {
	Name              string `json:"name"`
	Description       string `json:"description,omitempty"`
	IsDeprecated      bool   `json:"isDeprecated"`
	DeprecationReason string `json:"deprecationReason,omitempty"`
}

// FieldSummary is a simplified field representation for MCP responses.
type FieldSummary struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	ReturnType  string         `json:"return_type,omitempty"`
	Args        []ArgSummary   `json:"args,omitempty"`
	Deprecated  bool           `json:"deprecated,omitempty"`
	Fields      []FieldSummary `json:"fields,omitempty"`
}

// ArgSummary is a simplified argument representation.
type ArgSummary struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Default  string `json:"default,omitempty"`
}

// TypeSummary is a simplified type representation.
type TypeSummary struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Description string         `json:"description,omitempty"`
	Fields      []FieldSummary `json:"fields,omitempty"`
	EnumValues  []string       `json:"enum_values,omitempty"`
}

// GetTypeName returns the full type name including wrappers (e.g., "[String!]!").
func (t TypeRef) GetTypeName() string {
	switch t.Kind {
	case "NON_NULL":
		if t.OfType != nil {
			return t.OfType.GetTypeName() + "!"
		}
	case "LIST":
		if t.OfType != nil {
			return "[" + t.OfType.GetTypeName() + "]"
		}
	default:
		if t.Name != nil {
			return *t.Name
		}
	}
	return "Unknown"
}

// GetBaseTypeName returns the innermost type name (unwraps NON_NULL and LIST).
func (t TypeRef) GetBaseTypeName() string {
	switch t.Kind {
	case "NON_NULL", "LIST":
		if t.OfType != nil {
			return t.OfType.GetBaseTypeName()
		}
	default:
		if t.Name != nil {
			return *t.Name
		}
	}
	return "Unknown"
}

// IsRequired returns true if the type is non-null.
func (t TypeRef) IsRequired() bool {
	return t.Kind == "NON_NULL"
}

// IsScalar returns true if the base type is a scalar.
func (t TypeRef) IsScalar() bool {
	base := t.GetBaseTypeName()
	scalars := map[string]bool{
		"ID":       true,
		"String":   true,
		"Int":      true,
		"Float":    true,
		"Boolean":  true,
		"Time":     true,
		"DateTime": true,
		"JSON":     true,
		"Money":    true,
		"Currency": true,
	}
	return scalars[base]
}
