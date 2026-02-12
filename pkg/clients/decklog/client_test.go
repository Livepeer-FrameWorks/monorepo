package decklog

import (
	"testing"

	pb "frameworks/pkg/proto"
)

func TestBuildArtifactLifecycleEvent(t *testing.T) {
	startedAt := int64(100)
	completedAt := int64(200)
	expiresAt := int64(300)

	t.Run("returns nil when required fields are missing", func(t *testing.T) {
		if got := buildArtifactLifecycleEvent(pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "", "stream-1", "completed", nil, nil, nil, "tenant-1", "user-1"); got != nil {
			t.Fatalf("expected nil for missing artifact id, got %#v", got)
		}
		if got := buildArtifactLifecycleEvent(pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "artifact-1", "stream-1", "completed", nil, nil, nil, "", "user-1"); got != nil {
			t.Fatalf("expected nil for missing tenant id, got %#v", got)
		}
	})

	t.Run("builds service event with artifact payload", func(t *testing.T) {
		event := buildArtifactLifecycleEvent(
			pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			"artifact-1",
			"stream-1",
			"completed",
			&startedAt,
			&completedAt,
			&expiresAt,
			"tenant-1",
			"user-1",
		)
		if event == nil {
			t.Fatal("expected non-nil event")
		}
		if event.EventType != "artifact_lifecycle" {
			t.Fatalf("unexpected event type: %q", event.EventType)
		}
		if event.Source != "foghorn" {
			t.Fatalf("unexpected source: %q", event.Source)
		}
		if event.ResourceType != "artifact" || event.ResourceId != "artifact-1" {
			t.Fatalf("unexpected resource fields: type=%q id=%q", event.ResourceType, event.ResourceId)
		}
		payload, ok := event.Payload.(*pb.ServiceEvent_ArtifactEvent)
		if !ok {
			t.Fatalf("expected artifact event payload, got %T", event.Payload)
		}
		artifact := payload.ArtifactEvent
		if artifact.GetArtifactType() != pb.ArtifactEvent_ARTIFACT_TYPE_CLIP {
			t.Fatalf("unexpected artifact type: %v", artifact.GetArtifactType())
		}
		if artifact.GetArtifactId() != "artifact-1" || artifact.GetStreamId() != "stream-1" {
			t.Fatalf("unexpected artifact identifiers: id=%q stream=%q", artifact.GetArtifactId(), artifact.GetStreamId())
		}
		if artifact.GetStatus() != "completed" {
			t.Fatalf("unexpected status: %q", artifact.GetStatus())
		}
		if artifact.StartedAt == nil || *artifact.StartedAt != startedAt {
			t.Fatalf("unexpected started_at: %#v", artifact.StartedAt)
		}
		if artifact.CompletedAt == nil || *artifact.CompletedAt != completedAt {
			t.Fatalf("unexpected completed_at: %#v", artifact.CompletedAt)
		}
		if artifact.ExpiresAt == nil || *artifact.ExpiresAt != expiresAt {
			t.Fatalf("unexpected expires_at: %#v", artifact.ExpiresAt)
		}
	})
}

func TestClipStageToStatus(t *testing.T) {
	cases := []struct {
		name     string
		input    pb.ClipLifecycleData_Stage
		expected string
	}{
		{name: "requested", input: pb.ClipLifecycleData_STAGE_REQUESTED, expected: "requested"},
		{name: "queued", input: pb.ClipLifecycleData_STAGE_QUEUED, expected: "queued"},
		{name: "progress", input: pb.ClipLifecycleData_STAGE_PROGRESS, expected: "processing"},
		{name: "done", input: pb.ClipLifecycleData_STAGE_DONE, expected: "completed"},
		{name: "failed", input: pb.ClipLifecycleData_STAGE_FAILED, expected: "failed"},
		{name: "deleted", input: pb.ClipLifecycleData_STAGE_DELETED, expected: "deleted"},
		{name: "unknown", input: pb.ClipLifecycleData_Stage(999), expected: "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clipStageToStatus(tc.input); got != tc.expected {
				t.Fatalf("clipStageToStatus(%v) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDVRStatusToStatus(t *testing.T) {
	cases := []struct {
		name     string
		input    pb.DVRLifecycleData_Status
		expected string
	}{
		{name: "started", input: pb.DVRLifecycleData_STATUS_STARTED, expected: "started"},
		{name: "recording", input: pb.DVRLifecycleData_STATUS_RECORDING, expected: "recording"},
		{name: "stopped", input: pb.DVRLifecycleData_STATUS_STOPPED, expected: "stopped"},
		{name: "failed", input: pb.DVRLifecycleData_STATUS_FAILED, expected: "failed"},
		{name: "deleted", input: pb.DVRLifecycleData_STATUS_DELETED, expected: "deleted"},
		{name: "unknown", input: pb.DVRLifecycleData_Status(999), expected: "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dvrStatusToStatus(tc.input); got != tc.expected {
				t.Fatalf("dvrStatusToStatus(%v) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestVodStatusToStatus(t *testing.T) {
	cases := []struct {
		name     string
		input    pb.VodLifecycleData_Status
		expected string
	}{
		{name: "requested", input: pb.VodLifecycleData_STATUS_REQUESTED, expected: "requested"},
		{name: "uploading", input: pb.VodLifecycleData_STATUS_UPLOADING, expected: "uploading"},
		{name: "processing", input: pb.VodLifecycleData_STATUS_PROCESSING, expected: "processing"},
		{name: "completed", input: pb.VodLifecycleData_STATUS_COMPLETED, expected: "completed"},
		{name: "failed", input: pb.VodLifecycleData_STATUS_FAILED, expected: "failed"},
		{name: "deleted", input: pb.VodLifecycleData_STATUS_DELETED, expected: "deleted"},
		{name: "unknown", input: pb.VodLifecycleData_Status(999), expected: "unknown"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vodStatusToStatus(tc.input); got != tc.expected {
				t.Fatalf("vodStatusToStatus(%v) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestInt64Ptr(t *testing.T) {
	cases := []struct {
		name     string
		input    int64
		expected *int64
	}{
		{name: "negative becomes nil", input: -1, expected: nil},
		{name: "zero becomes nil", input: 0, expected: nil},
		{name: "positive returns pointer", input: 9, expected: int64Pointer(9)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := int64Ptr(tc.input)
			if tc.expected == nil {
				if got != nil {
					t.Fatalf("expected nil, got %#v", got)
				}
				return
			}
			if got == nil || *got != *tc.expected {
				t.Fatalf("expected %d, got %#v", *tc.expected, got)
			}
		})
	}
}

func int64Pointer(v int64) *int64 {
	return &v
}
