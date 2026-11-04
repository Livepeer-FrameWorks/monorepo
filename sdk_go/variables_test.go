package frameworks

import (
	"encoding/json"
	"testing"
)

func TestVariableEncodingPreservesExplicitNull(t *testing.T) {
	type input struct {
		Unset *string         `json:"unset"`
		Value json.RawMessage `json:"value"`
	}
	type Embedded struct {
		Unset *string `json:"unset"`
		Name  string  `json:"name"`
	}
	type wrapper struct{ Embedded }
	type shadowed struct {
		Embedded
		Unset string `json:"unset"`
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"map", map[string]any{"input": map[string]any{"description": nil}}, `{"input":{"description":null}}`},
		{"raw-json", json.RawMessage(`{"input":{"description":null}}`), `{"input":{"description":null}}`},
		{"typed-json-scalar", input{Value: json.RawMessage(`{"nested":null}`)}, `{"value":{"nested":null}}`},
		{"struct-in-map", map[string]any{"input": CreateStreamInput{Name: "a"}, "explicit": nil}, `{"explicit":null,"input":{"name":"a"}}`},
		{"embedded", wrapper{Embedded: Embedded{Name: "a"}}, `{"name":"a"}`},
		{"shadowed", shadowed{Embedded: Embedded{Name: "a"}}, `{"name":"a","unset":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := omitNulls(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(got, []byte(tc.want)) {
				t.Fatalf("encoded %s, want %s", got, tc.want)
			}
		})
	}
}
