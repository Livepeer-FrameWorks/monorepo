package models

import "testing"

func TestClusterTypeCanOwnLiveIngest(t *testing.T) {
	for _, tc := range []struct {
		clusterType string
		want        bool
	}{
		{clusterType: ClusterTypeEdge, want: true},
		{clusterType: ClusterTypeCentral, want: false},
		{clusterType: "", want: false},
		{clusterType: "storage", want: false},
	} {
		if got := ClusterTypeCanOwnLiveIngest(tc.clusterType); got != tc.want {
			t.Fatalf("ClusterTypeCanOwnLiveIngest(%q) = %v, want %v", tc.clusterType, got, tc.want)
		}
	}
}
