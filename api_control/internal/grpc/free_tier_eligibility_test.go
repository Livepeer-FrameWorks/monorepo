package grpc

import (
	"context"
	"errors"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"math"
	"testing"
)

type freeResourceFixture struct {
	cells  []string
	data   map[string]*foghornpb.TenantMediaFootprint
	failed string
}

func (f freeResourceFixture) OwnerCells(context.Context, string) ([]string, error) {
	return append([]string(nil), f.cells...), nil
}
func (f freeResourceFixture) Footprint(ctx context.Context, cell, tenant string, days int32) (*foghornpb.TenantMediaFootprint, error) {
	if cell == f.failed {
		return nil, errors.New("owner unavailable")
	}
	return f.data[cell], nil
}
func TestFreeResourceProofNeverConvertsIncompatiblePaidMedia(t *testing.T) {
	const gib = int64(1) << 30
	cases := []struct {
		name      string
		fixture   freeResourceFixture
		limit     int64
		reason    string
		wantError bool
		eligible  bool
	}{
		{name: "global storage", fixture: freeResourceFixture{cells: []string{"us", "eu"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: 6 * gib}, "eu": {StorageBytes: 6 * gib}}}, limit: 10 * gib, reason: "STORAGE_LIMIT_REACHED"},
		{name: "keep forever paid asset", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: gib, IncompatibleRetention: 1}}}, limit: 10 * gib, reason: "RETENTION_LIMIT_REACHED"},
		{name: "unknown or unfinished media", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {PendingArtifacts: 1}}}, limit: 10 * gib, reason: "MEDIA_WORK_PENDING"},
		{name: "active publisher", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {ActiveIngest: 1}}}, limit: 10 * gib, reason: "MEDIA_SESSIONS_ACTIVE"},
		{name: "active VOD viewer", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {ActiveViewers: 1}}}, limit: 10 * gib, reason: "MEDIA_SESSIONS_ACTIVE"},
		{name: "unknown owner", fixture: freeResourceFixture{cells: []string{""}}, limit: 10 * gib, reason: "MEDIA_OWNERSHIP_UNKNOWN"},
		{name: "missing response", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{}}, limit: 10 * gib, wantError: true},
		{name: "partial fleet", fixture: freeResourceFixture{cells: []string{"us", "eu"}, failed: "eu", data: map[string]*foghornpb.TenantMediaFootprint{"us": {}}}, limit: 10 * gib, wantError: true},
		{name: "overflow", fixture: freeResourceFixture{cells: []string{"us", "eu"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: math.MaxInt64}, "eu": {StorageBytes: 1}}}, limit: 10 * gib, wantError: true},
		{name: "invalid owner data", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: -1}}}, limit: 10 * gib, wantError: true},
		{name: "compatible immutable media", fixture: freeResourceFixture{cells: []string{"us", "us", "eu"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: 4 * gib}, "eu": {StorageBytes: 6 * gib}}}, limit: 10 * gib, eligible: true},
		{name: "reviewed uncapped override", fixture: freeResourceFixture{cells: []string{"us"}, data: map[string]*foghornpb.TenantMediaFootprint{"us": {StorageBytes: 20 * gib}}}, limit: 0, eligible: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkTenantFreeResources(t.Context(), &commodorepb.GetTenantFreeEligibilityRequest{TenantId: "tenant", StorageLimitBytes: tc.limit, RetentionLimitDays: 30}, tc.fixture)
			if (err != nil) != tc.wantError {
				t.Fatalf("resource proof err=%v", err)
			}
			if err != nil {
				return
			}
			if got.GetEligible() != tc.eligible || got.GetReason() != tc.reason {
				t.Fatalf("unsafe Free proof=%+v want eligible=%v reason=%s", got, tc.eligible, tc.reason)
			}
			if tc.name == "compatible immutable media" && got.GetStorageBytes() != 10*gib {
				t.Fatalf("same owner counted twice: %d", got.GetStorageBytes())
			}
		})
	}
}
