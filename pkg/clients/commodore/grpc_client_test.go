package commodore

import "testing"

func TestBuildValidateStreamKeyCacheKey(t *testing.T) {
	tests := []struct {
		name      string
		streamKey string
		clusterID string
		want      string
	}{
		{
			name:      "default_route",
			streamKey: "sk_live_abc",
			clusterID: "",
			want:      "commodore:validate:sk_live_abc",
		},
		{
			name:      "cluster_specific_route",
			streamKey: "sk_live_abc",
			clusterID: "cluster-us-west",
			want:      "commodore:validate:sk_live_abc:cluster:cluster-us-west",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildValidateStreamKeyCacheKey(tt.streamKey, tt.clusterID)
			if got != tt.want {
				t.Fatalf("unexpected cache key: got %q want %q", got, tt.want)
			}
		})
	}

	defaultKey := buildValidateStreamKeyCacheKey("sk_live_same", "")
	clusterKey := buildValidateStreamKeyCacheKey("sk_live_same", "cluster-a")
	if defaultKey == clusterKey {
		t.Fatalf("expected cluster-specific cache key to differ from default key")
	}
}
