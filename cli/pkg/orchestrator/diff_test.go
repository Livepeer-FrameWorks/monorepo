package orchestrator

import (
	"testing"

	"frameworks/cli/pkg/detect"
)

// expected sha values used across test cases.
const (
	binA  = "1111111111111111111111111111111111111111111111111111111111111111"
	binB  = "2222222222222222222222222222222222222222222222222222222222222222"
	envA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	envB  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	unitA = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	certA = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

func TestClassify(t *testing.T) {
	binPath := "/opt/frameworks/foghorn/foghorn"
	envPath := "/etc/frameworks/foghorn.env"
	unitPath := "/etc/systemd/system/frameworks-foghorn.service"
	certPath := "/etc/frameworks/pki/services/foghorn/tls.crt"

	binFP := func(sha string) *detect.Fingerprint {
		return &detect.Fingerprint{
			ServiceName: "foghorn",
			Host:        "regional-eu-1",
			Files: map[detect.FileKind]detect.ExpectedFile{
				detect.FileKindBinary: {Path: binPath, SHA256: sha},
			},
		}
	}
	multiFP := &detect.Fingerprint{
		ServiceName: "foghorn",
		Host:        "regional-eu-1",
		Files: map[detect.FileKind]detect.ExpectedFile{
			detect.FileKindBinary: {Path: binPath, SHA256: binA},
			detect.FileKindEnv:    {Path: envPath, SHA256: envA},
			detect.FileKindUnit:   {Path: unitPath, SHA256: unitA},
			detect.FileKindCert:   {Path: certPath, SHA256: certA},
		},
	}

	tests := []struct {
		name     string
		desired  *detect.Fingerprint
		observed map[string]string
		want     []DiffKind
	}{
		{
			name:     "nil fingerprint falls through to DiffUnknown",
			desired:  nil,
			observed: map[string]string{binPath: binA},
			want:     []DiffKind{DiffUnknown},
		},
		{
			name:     "empty Files map falls through to DiffUnknown",
			desired:  &detect.Fingerprint{ServiceName: "foghorn", Files: map[detect.FileKind]detect.ExpectedFile{}},
			observed: map[string]string{},
			want:     []DiffKind{DiffUnknown},
		},
		{
			name:     "binary match → no diff",
			desired:  binFP(binA),
			observed: map[string]string{binPath: binA},
			want:     nil,
		},
		{
			name:     "binary mismatch → DiffBinary",
			desired:  binFP(binA),
			observed: map[string]string{binPath: binB},
			want:     []DiffKind{DiffBinary},
		},
		{
			name:     "binary missing on host → DiffBinary",
			desired:  binFP(binA),
			observed: map[string]string{binPath: ""},
			want:     []DiffKind{DiffBinary},
		},
		{
			name:     "binary path absent from observed → DiffBinary",
			desired:  binFP(binA),
			observed: map[string]string{},
			want:     []DiffKind{DiffBinary},
		},
		{
			name:    "multi-kind: env-only mismatch",
			desired: multiFP,
			observed: map[string]string{
				binPath:  binA,
				envPath:  envB,
				unitPath: unitA,
				certPath: certA,
			},
			want: []DiffKind{DiffEnv},
		},
		{
			name:    "multi-kind: binary + cert mismatch in stable order",
			desired: multiFP,
			observed: map[string]string{
				binPath:  binB,
				envPath:  envA,
				unitPath: unitA,
				certPath: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			},
			want: []DiffKind{DiffBinary, DiffCert},
		},
		{
			name:    "multi-kind: all four differ → all four kinds in stable order",
			desired: multiFP,
			observed: map[string]string{
				binPath:  binB,
				envPath:  envB,
				unitPath: "9999999999999999999999999999999999999999999999999999999999999999",
				certPath: "8888888888888888888888888888888888888888888888888888888888888888",
			},
			want: []DiffKind{DiffBinary, DiffCert, DiffEnv, DiffUnit},
		},
		{
			name: "empty SHA256 in desired = no expectation, no diff",
			desired: &detect.Fingerprint{
				ServiceName: "foghorn",
				Files: map[detect.FileKind]detect.ExpectedFile{
					detect.FileKindBinary: {Path: binPath, SHA256: ""},
				},
			},
			observed: map[string]string{binPath: binA},
			want:     nil,
		},
		{
			name:     "all kinds match → no diff",
			desired:  multiFP,
			observed: map[string]string{binPath: binA, envPath: envA, unitPath: unitA, certPath: certA},
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify("foghorn", "regional-eu-1", tc.desired, tc.observed)
			if got.Host != "regional-eu-1" || got.Service != "foghorn" {
				t.Fatalf("identity mismatch: host=%q service=%q", got.Host, got.Service)
			}
			if !equalKinds(got.Kinds, tc.want) {
				t.Fatalf("kinds mismatch:\n  want %v\n  got  %v\n  details: %v", tc.want, got.Kinds, got.Details)
			}
			// Detail entries must exist for every emitted kind.
			for _, k := range got.Kinds {
				if _, ok := got.Details[k]; !ok {
					t.Errorf("missing detail for kind %q", k)
				}
			}
		})
	}
}

func TestHasKind(t *testing.T) {
	d := HostDiff{Kinds: []DiffKind{DiffBinary, DiffEnv}}
	if !d.HasKind(DiffBinary) {
		t.Error("expected HasKind(DiffBinary) = true")
	}
	if d.HasKind(DiffCert) {
		t.Error("expected HasKind(DiffCert) = false")
	}
}

// TestClassify_StableOrder verifies that multi-kind diffs come out in
// alphabetical FileKind order. This is what callers rely on for
// deterministic output (cluster diff text rendering, golden tests).
func TestClassify_StableOrder(t *testing.T) {
	fp := &detect.Fingerprint{
		Files: map[detect.FileKind]detect.ExpectedFile{
			detect.FileKindUnit:   {Path: "/u", SHA256: "u"},
			detect.FileKindBinary: {Path: "/b", SHA256: "b"},
			detect.FileKindCert:   {Path: "/c", SHA256: "c"},
			detect.FileKindEnv:    {Path: "/e", SHA256: "e"},
		},
	}
	observed := map[string]string{"/u": "x", "/b": "x", "/c": "x", "/e": "x"}
	got := Classify("svc", "host", fp, observed)
	want := []DiffKind{DiffBinary, DiffCert, DiffEnv, DiffUnit}
	if !equalKinds(got.Kinds, want) {
		t.Fatalf("want %v, got %v", want, got.Kinds)
	}
}

func equalKinds(a, b []DiffKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
