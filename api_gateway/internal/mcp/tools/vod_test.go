package tools

import "testing"

func TestRecommendedActionForVodUpload(t *testing.T) {
	tests := []struct {
		name          string
		state         pbVodStatusEnum
		missingCount  int
		lastErrorCode string
		want          string
	}{
		{
			name:         "uploading with no missing parts can complete",
			state:        vodStatusUploading,
			missingCount: 0,
			want:         "complete_upload",
		},
		{
			name:         "uploading with missing parts retries missing parts",
			state:        vodStatusUploading,
			missingCount: 2,
			want:         "retry_missing_parts",
		},
		{
			name:          "reconciliation failure does not recommend completion",
			state:         vodStatusUploading,
			lastErrorCode: "storage_reconciliation_failed",
			want:          "retry_missing_parts",
		},
		{
			name:  "ready",
			state: vodStatusReady,
			want:  "ready",
		},
		{
			name:  "processing",
			state: vodStatusProcessing,
			want:  "wait_processing",
		},
		{
			name:  "expired",
			state: vodStatusExpired,
			want:  "restart_expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := recommendedActionForVodUpload(tt.state, tt.missingCount, tt.lastErrorCode)
			if got != tt.want {
				t.Fatalf("recommendedActionForVodUpload() = %q, want %q", got, tt.want)
			}
		})
	}
}
