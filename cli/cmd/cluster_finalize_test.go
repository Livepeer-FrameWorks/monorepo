package cmd

import (
	"reflect"
	"testing"
)

func TestClusterFinalizePlan(t *testing.T) {
	tests := []struct {
		name           string
		only           string
		skipValidation bool
		want           []clusterFinalizeStep
	}{
		{
			name: "all",
			only: clusterFinalizeOnlyAll,
			want: []clusterFinalizeStep{
				clusterFinalizeStepPurserBootstrap,
				clusterFinalizeStepPurserValidate,
				clusterFinalizeStepCommodore,
				clusterFinalizeStepControlPlane,
			},
		},
		{
			name:           "all skip validation",
			only:           clusterFinalizeOnlyAll,
			skipValidation: true,
			want: []clusterFinalizeStep{
				clusterFinalizeStepPurserBootstrap,
				clusterFinalizeStepPurserValidate,
				clusterFinalizeStepCommodore,
			},
		},
		{
			name: "purser",
			only: clusterFinalizeOnlyPurser,
			want: []clusterFinalizeStep{
				clusterFinalizeStepPurserBootstrap,
				clusterFinalizeStepPurserValidate,
			},
		},
		{
			name:           "purser skip validation",
			only:           clusterFinalizeOnlyPurser,
			skipValidation: true,
			want:           []clusterFinalizeStep{clusterFinalizeStepPurserBootstrap},
		},
		{
			name: "commodore",
			only: clusterFinalizeOnlyCommodore,
			want: []clusterFinalizeStep{clusterFinalizeStepCommodore},
		},
		{
			name: "validation",
			only: clusterFinalizeOnlyValidation,
			want: []clusterFinalizeStep{clusterFinalizeStepControlPlane},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := clusterFinalizePlan(tt.only, tt.skipValidation)
			if err != nil {
				t.Fatalf("clusterFinalizePlan returned error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("clusterFinalizePlan = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestClusterFinalizePlanRejectsInvalidSlices(t *testing.T) {
	if _, err := clusterFinalizePlan("bogus", false); err == nil {
		t.Fatal("expected invalid --only value to fail")
	}
	if _, err := clusterFinalizePlan(clusterFinalizeOnlyValidation, true); err == nil {
		t.Fatal("expected --only=validation --skip-validation to fail")
	}
}

func TestClusterCommandIncludesFinalize(t *testing.T) {
	cmd := newClusterCmd()
	finalize, _, err := cmd.Find([]string{"finalize", "--help"})
	if err != nil {
		t.Fatalf("find finalize command: %v", err)
	}
	if finalize == nil || finalize.Use != "finalize" {
		t.Fatalf("finalize command not registered: %#v", finalize)
	}
	if finalize.Flags().Lookup("only") == nil {
		t.Fatal("finalize command missing --only flag")
	}
	if finalize.Flags().Lookup("bootstrap-reset-credentials") == nil {
		t.Fatal("finalize command missing Commodore bootstrap flags")
	}
}
