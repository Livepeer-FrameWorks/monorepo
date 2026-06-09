package preflight

import "testing"

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   uint64
		want string
	}{
		{name: "zero", in: 0, want: "0B"},
		{name: "sub-kilo stays bytes", in: 512, want: "512B"},
		{name: "exactly one KB", in: 1024, want: "1.0KB"},
		{name: "one and a half KB", in: 1536, want: "1.5KB"},
		{name: "one MB", in: 1024 * 1024, want: "1.0MB"},
		{name: "one GB", in: 1024 * 1024 * 1024, want: "1.0GB"},
		{name: "one TB", in: 1024 * 1024 * 1024 * 1024, want: "1.0TB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := formatBytes(tc.in); got != tc.want {
				t.Fatalf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatPercent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   float64
		want string
	}{
		{in: 0, want: "0.0%"},
		{in: 5, want: "5.0%"},
		{in: 12.34, want: "12.3%"},
	}
	for _, tc := range cases {
		if got := formatPercent(tc.in); got != tc.want {
			t.Fatalf("formatPercent(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiskCheckName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{in: "/", want: "disk-root"},
		{in: "/var/lib", want: "disk-var-lib"},
		{in: "/opt", want: "disk-opt"},
		{in: "", want: "disk"},
	}
	for _, tc := range cases {
		if got := diskCheckName(tc.in); got != tc.want {
			t.Fatalf("diskCheckName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
