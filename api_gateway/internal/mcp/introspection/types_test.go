package introspection

import "testing"

func strptr(s string) *string { return &s }

// named builds a leaf SCALAR/OBJECT ref.
func named(kind, name string) TypeRef { return TypeRef{Kind: kind, Name: strptr(name)} }

// wrap nests a ref inside a NON_NULL/LIST wrapper.
func wrap(kind string, of TypeRef) TypeRef { return TypeRef{Kind: kind, OfType: &of} }

func TestTypeRef_GetTypeName(t *testing.T) {
	tests := []struct {
		name string
		ref  TypeRef
		want string
	}{
		{"scalar", named("SCALAR", "String"), "String"},
		{"non-null scalar", wrap("NON_NULL", named("SCALAR", "String")), "String!"},
		{"list of scalar", wrap("LIST", named("SCALAR", "Int")), "[Int]"},
		{"non-null list of non-null scalar", wrap("NON_NULL", wrap("LIST", wrap("NON_NULL", named("SCALAR", "ID")))), "[ID!]!"},
		{"non-null missing ofType", TypeRef{Kind: "NON_NULL"}, "Unknown"},
		{"leaf missing name", TypeRef{Kind: "OBJECT"}, "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.GetTypeName(); got != tt.want {
				t.Fatalf("GetTypeName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTypeRef_GetBaseTypeName(t *testing.T) {
	tests := []struct {
		name string
		ref  TypeRef
		want string
	}{
		{"scalar", named("SCALAR", "String"), "String"},
		{"unwraps all wrappers", wrap("NON_NULL", wrap("LIST", wrap("NON_NULL", named("OBJECT", "Stream")))), "Stream"},
		{"wrapper missing ofType", TypeRef{Kind: "LIST"}, "Unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ref.GetBaseTypeName(); got != tt.want {
				t.Fatalf("GetBaseTypeName = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTypeRef_IsRequired(t *testing.T) {
	if !wrap("NON_NULL", named("SCALAR", "String")).IsRequired() {
		t.Fatal("NON_NULL should be required")
	}
	if named("SCALAR", "String").IsRequired() {
		t.Fatal("bare scalar should not be required")
	}
	if wrap("LIST", named("SCALAR", "String")).IsRequired() {
		t.Fatal("LIST (nullable) should not be required")
	}
}

func TestTypeRef_IsScalar(t *testing.T) {
	for _, s := range []string{"ID", "String", "Int", "Float", "Boolean", "Time", "DateTime", "JSON", "Money", "Currency"} {
		if !named("SCALAR", s).IsScalar() {
			t.Fatalf("%s should be classified scalar", s)
		}
	}
	// Wrapped scalar still counts (unwraps to base).
	if !wrap("NON_NULL", named("SCALAR", "ID")).IsScalar() {
		t.Fatal("NON_NULL ID should be scalar")
	}
	// Object type is not a scalar.
	if named("OBJECT", "Stream").IsScalar() {
		t.Fatal("Stream should not be a scalar")
	}
}
