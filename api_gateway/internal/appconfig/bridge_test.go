package appconfig

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func TestGatewayGraphQLURL(t *testing.T) {
	for _, tc := range []struct {
		name, port, want string
	}{
		{"configured listener", "18000", "http://localhost:18000/graphql/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Bridge{HTTPListen: config.HTTPListen{Port: tc.port}}
			if got := cfg.GatewayGraphQLURL(); got != tc.want {
				t.Fatalf("MCP GraphQL endpoint = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestGatewayGraphQLURLUsesBoundAddress(t *testing.T) {
	for _, tc := range []struct {
		addresses []string
		want      string
	}{
		{[]string{"10.0.0.1", "127.0.0.1"}, "http://127.0.0.1:18000/graphql/"},
		{[]string{"::"}, "http://[::1]:18000/graphql/"},
		{[]string{"10.0.0.1"}, "http://10.0.0.1:18000/graphql/"},
	} {
		cfg := Bridge{HTTPListen: config.HTTPListen{Port: "18000"}, ListenAddrs: config.ListenAddrs{BindAddrs: tc.addresses}}
		if got := cfg.GatewayGraphQLURL(); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.addresses, got, tc.want)
		}
	}
}
