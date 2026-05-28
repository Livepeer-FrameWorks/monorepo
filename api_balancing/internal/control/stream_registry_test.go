package control

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type fakeCommodore struct {
	resp *pb.ResolveStreamContextResponse
	err  error
	hits int
}

func (f *fakeCommodore) ResolveStreamContext(ctx context.Context, streamID, playbackID, internalName, clusterID string) (*pb.ResolveStreamContextResponse, error) {
	f.hits++
	return f.resp, f.err
}

func nativeResp() *pb.ResolveStreamContextResponse {
	return &pb.ResolveStreamContextResponse{
		Admitted:     true,
		StreamId:     "stream-uuid-1",
		PlaybackId:   "frameworks-demo",
		InternalName: "60546679b497415db2338cd5cae54992",
		IngestMode:   "mist_native",
		TenantId:     "tenant-1",
	}
}

func TestRuntimeNameFor(t *testing.T) {
	cases := []struct {
		mode IngestMode
		want string
	}{
		{IngestPush, "live+abc"},
		{IngestPull, "pull+abc"},
		{IngestMistNative, "abc"},
	}
	for _, tc := range cases {
		if got := RuntimeNameFor(tc.mode, "abc"); got != tc.want {
			t.Errorf("RuntimeNameFor(%v, abc) = %q, want %q", tc.mode, got, tc.want)
		}
	}

	// Zero-value mode produces empty sentinel — must not silently default
	// to live+ as the prior helper did.
	if got := RuntimeNameFor(0, "abc"); got != "" {
		t.Errorf("RuntimeNameFor(0, abc) = %q, want \"\" (no default-push)", got)
	}
}

func TestIngestModeFromWire(t *testing.T) {
	ok := []struct {
		in   string
		want IngestMode
	}{
		{"push", IngestPush},
		{"pull", IngestPull},
		{"mist_native", IngestMistNative},
		{"  push  ", IngestPush},
	}
	for _, tc := range ok {
		got, err := IngestModeFromWire(tc.in)
		if err != nil {
			t.Errorf("IngestModeFromWire(%q) err = %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("IngestModeFromWire(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "live", "unknown", "PUSH"} {
		if _, err := IngestModeFromWire(bad); err == nil {
			t.Errorf("IngestModeFromWire(%q) err = nil, want error", bad)
		}
	}
}

func TestResolveSourceByInternalName_NativePopulatesEntry(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", time.Minute)
	e, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if e.IngestMode != IngestMistNative {
		t.Errorf("IngestMode = %v, want IngestMistNative", e.IngestMode)
	}
	// Critical: native runtime name must be the bare internal_name, NOT
	// live+<internal_name>. This is the central bug this whole change
	// exists to fix.
	if e.RuntimeName != "60546679b497415db2338cd5cae54992" {
		t.Errorf("RuntimeName = %q, want bare internal_name", e.RuntimeName)
	}
	if e.PlaybackID != "frameworks-demo" {
		t.Errorf("PlaybackID = %q", e.PlaybackID)
	}
}

func TestResolveSourceByInternalName_PushAndPull(t *testing.T) {
	push := nativeResp()
	push.IngestMode = "push"
	push.InternalName = "pushstream"
	r := NewStreamRegistry(&fakeCommodore{resp: push}, "cluster-A", time.Minute)
	e, err := r.ResolveSourceByInternalName(context.Background(), "pushstream")
	if err != nil {
		t.Fatalf("push err = %v", err)
	}
	if e.RuntimeName != "live+pushstream" {
		t.Errorf("push RuntimeName = %q", e.RuntimeName)
	}

	pull := nativeResp()
	pull.IngestMode = "pull"
	pull.InternalName = "pullstream"
	r = NewStreamRegistry(&fakeCommodore{resp: pull}, "cluster-A", time.Minute)
	e, err = r.ResolveSourceByInternalName(context.Background(), "pullstream")
	if err != nil {
		t.Fatalf("pull err = %v", err)
	}
	if e.RuntimeName != "pull+pullstream" {
		t.Errorf("pull RuntimeName = %q", e.RuntimeName)
	}
}

func TestResolveSourceByPlaybackID_DifferentFromInternal(t *testing.T) {
	// Regression test for the central bug: playback_id != internal_name
	// for mist-native streams. Resolving by either key must return the
	// same canonical entry with the correct (bare) runtime name.
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	e, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if e.InternalName != "60546679b497415db2338cd5cae54992" {
		t.Errorf("InternalName = %q", e.InternalName)
	}
	if e.RuntimeName != "60546679b497415db2338cd5cae54992" {
		t.Errorf("RuntimeName = %q, want bare", e.RuntimeName)
	}
}

func TestResolveSourceMiss_FailClosed(t *testing.T) {
	r := NewStreamRegistry(&fakeCommodore{resp: &pb.ResolveStreamContextResponse{Admitted: false}}, "cluster-A", time.Minute)
	_, err := r.ResolveSourceByInternalName(context.Background(), "nonexistent")
	if !errors.Is(err, ErrUnknownStream) {
		t.Errorf("err = %v, want ErrUnknownStream", err)
	}
}

func TestResolveSourceEmptyIngestMode_FailClosed(t *testing.T) {
	// Commodore returning admitted=true but no ingest_mode is a
	// Commodore bug; registry must NOT silently treat it as push.
	resp := nativeResp()
	resp.IngestMode = ""
	r := NewStreamRegistry(&fakeCommodore{resp: resp}, "cluster-A", time.Minute)
	_, err := r.ResolveSourceByInternalName(context.Background(), resp.InternalName)
	if !errors.Is(err, ErrUnknownStream) {
		t.Errorf("err = %v, want ErrUnknownStream", err)
	}
}

func TestResolveSource_CachesAcrossAllKeys(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", time.Minute)

	// First call by internal_name hydrates.
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatalf("err = %v", err)
	}
	// Subsequent calls by playback_id or stream_id hit cache, no extra Commodore calls.
	if _, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo"); err != nil {
		t.Fatalf("playback err = %v", err)
	}
	if _, err := r.ResolveSourceByStreamID(context.Background(), "stream-uuid-1"); err != nil {
		t.Fatalf("streamID err = %v", err)
	}
	if fake.hits != 1 {
		t.Errorf("Commodore hits = %d, want 1 (cache should serve subsequent lookups)", fake.hits)
	}
}

func TestResolveSourceTTL_Expires(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	if fake.hits != 2 {
		t.Errorf("Commodore hits = %d, want 2 after TTL expiry", fake.hits)
	}
}

func TestResolveSource_NilClient_FailClosed(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	_, err := r.ResolveSourceByInternalName(context.Background(), "x")
	if !errors.Is(err, ErrUnknownStream) {
		t.Errorf("err = %v, want ErrUnknownStream", err)
	}
}

func TestMissLogger_FiresOnMiss(t *testing.T) {
	var gotKind, gotKey string
	r := NewStreamRegistry(&fakeCommodore{resp: &pb.ResolveStreamContextResponse{Admitted: false}}, "cluster-A", time.Minute)
	r.SetMissLogger(func(_ context.Context, refKind, key string) {
		gotKind, gotKey = refKind, key
	})
	_, _ = r.ResolveSourceByInternalName(context.Background(), "ghost")
	if gotKind != "internal_name" || gotKey != "ghost" {
		t.Errorf("miss logger got (%q, %q), want (internal_name, ghost)", gotKind, gotKey)
	}
}

func TestInvalidate_ForcesRehydrate(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", time.Minute)
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	r.Invalidate("stream-uuid-1", "60546679b497415db2338cd5cae54992", "frameworks-demo")
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	if fake.hits != 2 {
		t.Errorf("Commodore hits = %d, want 2 after invalidate", fake.hits)
	}
}
