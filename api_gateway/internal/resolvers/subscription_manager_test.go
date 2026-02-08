package resolvers

import (
	"testing"

	pb "frameworks/pkg/proto"
)

func TestTenantMismatch(t *testing.T) {
	tenant := "tenant-1"
	otherTenant := "tenant-2"

	tests := []struct {
		name     string
		tenantID string
		event    *pb.SignalmanEvent
		want     bool
	}{
		{
			name:     "empty tenant skips mismatch",
			tenantID: "",
			event:    &pb.SignalmanEvent{TenantId: &tenant},
			want:     false,
		},
		{
			name:     "missing event tenant treated as mismatch",
			tenantID: tenant,
			event:    &pb.SignalmanEvent{},
			want:     true,
		},
		{
			name:     "tenant match passes",
			tenantID: tenant,
			event:    &pb.SignalmanEvent{TenantId: &tenant},
			want:     false,
		},
		{
			name:     "tenant mismatch blocks",
			tenantID: tenant,
			event:    &pb.SignalmanEvent{TenantId: &otherTenant},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tenantMismatch(tt.tenantID, tt.event); got != tt.want {
				t.Fatalf("tenantMismatch = %v, want %v", got, tt.want)
			}
		})
	}
}
