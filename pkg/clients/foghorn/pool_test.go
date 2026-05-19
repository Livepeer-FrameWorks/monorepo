package foghorn

import "testing"

func TestFoghornPoolDefaultsInternalServerNameForManagedTLS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  PoolConfig
		want string
	}{
		{
			name: "ca bundle",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			want: defaultInternalServerName,
		},
		{
			name: "explicit tls",
			cfg:  PoolConfig{UseTLS: true},
			want: defaultInternalServerName,
		},
		{
			name: "explicit server name wins",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", ServerName: "custom.internal"},
			want: "custom.internal",
		},
		{
			name: "insecure does not default",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", AllowInsecure: true},
			want: "",
		},
		{
			name: "legacy plaintext default",
			cfg:  PoolConfig{},
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool := NewPool(tc.cfg)
			defer pool.Close()
			if got := pool.serverName(); got != tc.want {
				t.Fatalf("serverName() = %q, want %q", got, tc.want)
			}
		})
	}
}
