package graph

import (
	"frameworks/api_gateway/graph/model"
	"testing"
)

func intPtr(v int) *int { return &v }

func TestGetPageMultiplier(t *testing.T) {
	tests := []struct {
		name string
		page *model.ConnectionInput
		want int
	}{
		{"nil input", nil, DefaultPageSize},
		{"empty input", &model.ConnectionInput{}, DefaultPageSize},
		{"first=10", &model.ConnectionInput{First: intPtr(10)}, 10},
		{"first=0 falls back to default", &model.ConnectionInput{First: intPtr(0)}, DefaultPageSize},
		{"last=25", &model.ConnectionInput{Last: intPtr(25)}, 25},
		{"first exceeds max", &model.ConnectionInput{First: intPtr(999)}, MaxPageSize},
		{"last exceeds max", &model.ConnectionInput{Last: intPtr(600)}, MaxPageSize},
		{"first takes precedence over last", &model.ConnectionInput{First: intPtr(5), Last: intPtr(20)}, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getPageMultiplier(tt.page); got != tt.want {
				t.Errorf("getPageMultiplier() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestConnectionComplexity(t *testing.T) {
	tests := []struct {
		name            string
		childComplexity int
		page            *model.ConnectionInput
		want            int
	}{
		{
			name:            "subtracts meta overhead before multiplying",
			childComplexity: 46, // edges(39) + pageInfo(6) + totalCount(1)
			page:            &model.ConnectionInput{First: intPtr(50)},
			// perItemCost = 46 - 8 = 38; result = 2 + 8 + (50 * 38) = 1910
			want: ConnectionBaseCost + ConnectionMetaOverhead + (50 * (46 - ConnectionMetaOverhead)),
		},
		{
			name:            "small child complexity floors perItemCost to 1",
			childComplexity: 3,
			page:            &model.ConnectionInput{First: intPtr(10)},
			// perItemCost = max(3 - 8, 1) = 1; result = 2 + 8 + (10 * 1) = 20
			want: ConnectionBaseCost + ConnectionMetaOverhead + 10,
		},
		{
			name:            "nil page uses default page size",
			childComplexity: 20,
			page:            nil,
			// perItemCost = 20 - 8 = 12; result = 2 + 8 + (50 * 12) = 610
			want: ConnectionBaseCost + ConnectionMetaOverhead + (DefaultPageSize * 12),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := connectionComplexity(tt.childComplexity, tt.page); got != tt.want {
				t.Errorf("connectionComplexity(%d, page) = %d, want %d", tt.childComplexity, got, tt.want)
			}
		})
	}
}
