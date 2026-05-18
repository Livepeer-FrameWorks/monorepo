package resolvers

import "testing"

func TestArtifactLifecycleStageCanOverrideRegistry(t *testing.T) {
	tests := []struct {
		name      string
		registry  string
		lifecycle string
		want      bool
	}{
		{name: "terminal registry keeps authority over queued lifecycle", registry: "ready", lifecycle: "queued", want: false},
		{name: "terminal lifecycle can replace nonterminal registry", registry: "processing", lifecycle: "completed", want: true},
		{name: "terminal lifecycle can confirm terminal registry", registry: "ready", lifecycle: "synced", want: true},
		{name: "nonterminal lifecycle can fill unknown registry", registry: "unknown", lifecycle: "processing", want: true},
		{name: "empty lifecycle is ignored", registry: "ready", lifecycle: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := artifactLifecycleStageCanOverrideRegistry(tt.registry, tt.lifecycle)
			if got != tt.want {
				t.Fatalf("artifactLifecycleStageCanOverrideRegistry(%q, %q) = %v, want %v", tt.registry, tt.lifecycle, got, tt.want)
			}
		})
	}
}
