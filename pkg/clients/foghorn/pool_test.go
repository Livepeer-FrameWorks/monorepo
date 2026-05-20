package foghorn

import "testing"

func TestFoghornPoolServerNameForManagedTLS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  PoolConfig
		addr string
		want string
	}{
		{
			name: "ca bundle",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.internal:18019",
			want: defaultInternalServerName,
		},
		{
			name: "explicit tls",
			cfg:  PoolConfig{UseTLS: true},
			addr: "10.88.1.10:18019",
			want: defaultInternalServerName,
		},
		{
			name: "cluster fqdn uses target hostname",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.media-us-1.frameworks.network:18019",
			want: "",
		},
		{
			name: "explicit server name wins",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", ServerName: "custom.internal"},
			addr: "foghorn.media-us-1.frameworks.network:18019",
			want: "custom.internal",
		},
		{
			name: "insecure does not default",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", AllowInsecure: true},
			addr: "foghorn.internal:18019",
			want: "",
		},
		{
			name: "legacy plaintext default",
			cfg:  PoolConfig{},
			addr: "foghorn.internal:18019",
			want: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool := NewPool(tc.cfg)
			defer pool.Close()
			if got := pool.serverName(tc.addr); got != tc.want {
				t.Fatalf("serverName(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}
