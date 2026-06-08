package resolvers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
)

// A non-relay raw string (contains a '-', so it is not valid base64) must pass
// through Decode untouched. UUIDs also satisfy this.
const rawUUID = "550e8400-e29b-41d4-a716-446655440000"

func TestNormalizeStreamID(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty passes through", input: "", want: ""},
		{name: "raw id passes through", input: "stream-raw-123", want: "stream-raw-123"},
		{
			name:  "stream relay id is decoded to the raw id",
			input: globalid.Encode(globalid.TypeStream, "internal-name-42"),
			want:  "internal-name-42",
		},
		{
			name:    "wrong relay type is rejected",
			input:   globalid.Encode(globalid.TypeClip, "x"),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeStreamID(tc.input)
			assertNormalize(t, got, err, tc.want, tc.wantErr)
		})
	}
}

func TestNormalizeStreamIDPtr(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		got, err := normalizeStreamIDPtr(nil)
		if err != nil || got != nil {
			t.Fatalf("normalizeStreamIDPtr(nil) = (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("empty pointer passes through", func(t *testing.T) {
		empty := ""
		got, err := normalizeStreamIDPtr(&empty)
		if err != nil || got != &empty {
			t.Fatalf("normalizeStreamIDPtr(&\"\") = (%v, %v), want same pointer", got, err)
		}
	})

	t.Run("relay id is decoded", func(t *testing.T) {
		in := globalid.Encode(globalid.TypeStream, "raw-9")
		got, err := normalizeStreamIDPtr(&in)
		if err != nil || got == nil || *got != "raw-9" {
			t.Fatalf("normalizeStreamIDPtr = (%v, %v), want *got=raw-9", got, err)
		}
	})

	t.Run("error propagates as nil pointer", func(t *testing.T) {
		in := globalid.Encode(globalid.TypeClip, "x")
		got, err := normalizeStreamIDPtr(&in)
		if err == nil || got != nil {
			t.Fatalf("normalizeStreamIDPtr = (%v, %v), want (nil, err)", got, err)
		}
	})
}

func TestNormalizeClipHash(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty passes through", input: "", want: ""},
		{name: "raw hash passes through", input: "cliphash123", want: "cliphash123"},
		{
			name:  "clip relay id encoding a hash is decoded",
			input: globalid.Encode(globalid.TypeClip, "abc123hash"),
			want:  "abc123hash",
		},
		{
			name:    "wrong relay type is rejected",
			input:   globalid.Encode(globalid.TypeStream, "abc"),
			wantErr: true,
		},
		{
			name:    "legacy relay id encoding a UUID is rejected (migration guard)",
			input:   globalid.Encode(globalid.TypeClip, rawUUID),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeClipHash(tc.input)
			assertNormalize(t, got, err, tc.want, tc.wantErr)
		})
	}
}

func TestNormalizeVodHash(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty passes through", input: "", want: ""},
		{name: "raw hash passes through", input: "vodhash123", want: "vodhash123"},
		{
			name:  "vod relay id encoding a hash is decoded",
			input: globalid.Encode(globalid.TypeVodAsset, "artifact-hash-7"),
			want:  "artifact-hash-7",
		},
		{
			name:    "wrong relay type is rejected",
			input:   globalid.Encode(globalid.TypeClip, "abc"),
			wantErr: true,
		},
		{
			name:    "legacy relay id encoding a UUID is rejected (migration guard)",
			input:   globalid.Encode(globalid.TypeVodAsset, rawUUID),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeVodHash(tc.input)
			assertNormalize(t, got, err, tc.want, tc.wantErr)
		})
	}
}

// TestNormalizeExportedAliases pins that the public wrappers delegate to the
// unexported implementations rather than re-implementing the logic.
func TestNormalizeExportedAliases(t *testing.T) {
	if got, err := NormalizeStreamID(globalid.Encode(globalid.TypeStream, "s")); err != nil || got != "s" {
		t.Errorf("NormalizeStreamID = (%q, %v), want (s, nil)", got, err)
	}
	if got, err := NormalizeClipHash(globalid.Encode(globalid.TypeClip, "c")); err != nil || got != "c" {
		t.Errorf("NormalizeClipHash = (%q, %v), want (c, nil)", got, err)
	}
	if got, err := NormalizeVodHash(globalid.Encode(globalid.TypeVodAsset, "v")); err != nil || got != "v" {
		t.Errorf("NormalizeVodHash = (%q, %v), want (v, nil)", got, err)
	}
}

func assertNormalize(t *testing.T, got string, err error, want string, wantErr bool) {
	t.Helper()
	if wantErr {
		if err == nil {
			t.Fatalf("expected error, got result %q", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
