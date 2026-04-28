package bootstrap

import "testing"

func TestParseMoney(t *testing.T) {
	cases := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"0.00", 0, false},
		{"1", 1, false},
		{"99.99", 99.99, false},
		{"1234.5", 1234.5, false},
		{"0.0001", 0.0001, false},

		// Auditor-flagged regressions: each of these used to silently parse
		// or coerce to zero under fmt.Sscanf("%f"). They must error now.
		{"10oops", 0, true},
		{"oops", 0, true},
		{"1e3", 0, true},
		{"Inf", 0, true},
		{"NaN", 0, true},
		{" 5", 0, true},
		{"5 ", 0, true},
		{"5 6", 0, true},
		{"-5", 0, true},
		{"-0.50", 0, true},
		{"5.123456", 0, true}, // more than 4 fractional digits
	}
	for _, c := range cases {
		got, err := parseMoney(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMoney(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMoney(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMoney(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
