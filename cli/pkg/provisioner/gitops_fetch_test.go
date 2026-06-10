package provisioner

import (
	"reflect"
	"testing"
)

// gitopsRepositoriesFromMetadata normalizes the manifest-source list, which may
// arrive as a native []string, a YAML/JSON-decoded []any, or a single string,
// and additionally appends the singular gitops_repository key when present.
func TestGitopsRepositoriesFromMetadata(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want []string
	}{
		{"nil metadata", nil, nil},
		{"empty metadata", map[string]any{}, nil},
		{
			name: "native []string",
			meta: map[string]any{"gitops_repositories": []string{"a", "b"}},
			want: []string{"a", "b"},
		},
		{
			name: "[]any drops non-strings and empties",
			meta: map[string]any{"gitops_repositories": []any{"a", "", 1, nil, "b"}},
			want: []string{"a", "b"},
		},
		{
			name: "single string form",
			meta: map[string]any{"gitops_repositories": "only"},
			want: []string{"only"},
		},
		{
			name: "singular key appends after the plural list",
			meta: map[string]any{
				"gitops_repositories": []string{"a"},
				"gitops_repository":   "b",
			},
			want: []string{"a", "b"},
		},
		{
			name: "singular key alone",
			meta: map[string]any{"gitops_repository": "solo"},
			want: []string{"solo"},
		},
		{
			name: "empty singular key contributes nothing",
			meta: map[string]any{"gitops_repository": ""},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := gitopsRepositoriesFromMetadata(tt.meta); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}
