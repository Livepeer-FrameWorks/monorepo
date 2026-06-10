package provisioner

import (
	"reflect"
	"testing"
)

// The meta* helpers read loosely-typed ServiceConfig.Metadata (decoded from
// YAML/JSON into map[string]any). The contract is: absent key or wrong dynamic
// type falls back to the zero value / supplied default — never a panic.
func TestMetaString(t *testing.T) {
	m := map[string]any{"name": "edge", "port": 8080}
	if got := metaString(m, "name"); got != "edge" {
		t.Fatalf("present string: got %q", got)
	}
	if got := metaString(m, "missing"); got != "" {
		t.Fatalf("absent key: got %q want empty", got)
	}
	if got := metaString(m, "port"); got != "" {
		t.Fatalf("wrong type (int): got %q want empty", got)
	}
}

func TestMetaStringSlice(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		want []string
	}{
		{"absent key", map[string]any{}, "x", nil},
		{"native []string", map[string]any{"x": []string{"a", "b"}}, "x", []string{"a", "b"}},
		{
			name: "[]any filters non-strings and empties",
			m:    map[string]any{"x": []any{"a", "", 7, nil, "b"}},
			key:  "x",
			want: []string{"a", "b"},
		},
		{"wrong scalar type", map[string]any{"x": "a"}, "x", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metaStringSlice(tt.m, tt.key); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("metaStringSlice = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestMetaIntOr(t *testing.T) {
	m := map[string]any{"n": 5, "s": "nope"}
	if got := metaIntOr(m, "n", -1); got != 5 {
		t.Fatalf("present int: got %d", got)
	}
	if got := metaIntOr(m, "missing", -1); got != -1 {
		t.Fatalf("absent key: got %d want default", got)
	}
	if got := metaIntOr(m, "s", -1); got != -1 {
		t.Fatalf("wrong type: got %d want default", got)
	}
}

func TestMetaBool(t *testing.T) {
	m := map[string]any{"yes": true, "s": "true"}
	if got := metaBool(m, "yes", false); !got {
		t.Fatalf("present bool: got %v want true", got)
	}
	if got := metaBool(m, "missing", true); !got {
		t.Fatalf("absent key: got %v want default true", got)
	}
	// A string "true" is the wrong dynamic type and must fall back to the default.
	if got := metaBool(m, "s", false); got {
		t.Fatalf("wrong type: got %v want default false", got)
	}
}
