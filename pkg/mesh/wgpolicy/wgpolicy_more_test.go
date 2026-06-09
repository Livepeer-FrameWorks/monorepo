package wgpolicy

import (
	"testing"
)

func TestValidateForApply_KeepAliveBoundary(t *testing.T) {
	cases := []struct {
		name      string
		keepAlive int
		wantErr   bool
	}{
		{"zero is accepted", 0, false},
		{"positive is accepted", 25, false},
		{"negative is rejected", -1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := validBase(t)
			cfg.Peers[0].KeepAlive = c.keepAlive
			err := ValidateForApply(cfg)
			if c.wantErr && err == nil {
				t.Fatalf("KeepAlive=%d should be rejected", c.keepAlive)
			}
			if !c.wantErr && err != nil {
				t.Fatalf("KeepAlive=%d should be accepted: %v", c.keepAlive, err)
			}
		})
	}
}
