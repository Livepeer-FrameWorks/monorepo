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
			name: "internal dns name",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "regional-eu-1.internal:18019",
			want: defaultInternalServerName,
		},
		{
			name: "cluster fqdn uses target hostname",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.media-us-1.frameworks.network:18029",
			want: "foghorn.media-us-1.frameworks.network",
		},
		{
			name: "explicit server name wins for internal address",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", ServerName: "custom.internal"},
			addr: "10.88.1.10:18019",
			want: "custom.internal",
		},
		{
			name: "cluster fqdn ignores leaked internal override",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", ServerName: "foghorn.internal"},
			addr: "foghorn.media-us-1.frameworks.network:18029",
			want: "foghorn.media-us-1.frameworks.network",
		},
		{
			name: "insecure does not default",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", AllowInsecure: true},
			addr: "foghorn.internal:18019",
			want: "",
		},
		{
			name: "development plaintext default",
			cfg:  PoolConfig{},
			addr: "foghorn.internal:18019",
			want: "",
		},
	}

	for _, tc := range cases {
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

func TestFoghornPoolCAFileForManagedTLS(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  PoolConfig
		addr string
		want string
	}{
		{
			name: "internal foghorn uses internal CA",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.internal:18019",
			want: "/etc/frameworks/pki/ca.crt",
		},
		{
			name: "internal address uses internal CA",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "10.88.1.10:18019",
			want: "/etc/frameworks/pki/ca.crt",
		},
		{
			name: "internal dns name uses internal CA",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "regional-eu-1.internal:18019",
			want: "/etc/frameworks/pki/ca.crt",
		},
		{
			name: "cluster fqdn uses system roots",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.media-us-1.frameworks.network:18029",
			want: "",
		},
		{
			name: "public root fqdn uses system roots",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt"},
			addr: "foghorn.frameworks.network:18029",
			want: "",
		},
		{
			name: "insecure never carries CA",
			cfg:  PoolConfig{CACertFile: "/etc/frameworks/pki/ca.crt", AllowInsecure: true},
			addr: "foghorn.internal:18019",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pool := NewPool(tc.cfg)
			defer pool.Close()
			if got := pool.caCertFile(tc.addr); got != tc.want {
				t.Fatalf("caCertFile(%q) = %q, want %q", tc.addr, got, tc.want)
			}
		})
	}
}
