package cmd

import (
	"net/url"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestBuildDatabaseURLMultiHostYugabyte(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"yuga-eu-1": {ExternalIP: "10.0.0.1"},
			"yuga-eu-2": {ExternalIP: "10.0.0.2"},
			"yuga-eu-3": {ExternalIP: "10.0.0.3"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Port:    5433,
				Nodes: []inventory.PostgresNode{
					{Host: "yuga-eu-1", ID: 1},
					{Host: "yuga-eu-2", ID: 2},
					{Host: "yuga-eu-3", ID: 3},
				},
			},
		},
	}

	got := buildDatabaseURL(manifest, "yuga-eu-1.internal", "5433", "foghorn", "secret", "foghorn")

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("multi-host DATABASE_URL must parse: %v (url=%q)", err, got)
	}
	wantHost := "yuga-eu-1.internal:5433,yuga-eu-2.internal:5433,yuga-eu-3.internal:5433"
	if parsed.Host != wantHost {
		t.Fatalf("host = %q, want %q", parsed.Host, wantHost)
	}
	q := parsed.Query()
	if q.Get("load_balance") != "true" {
		t.Errorf("load_balance = %q, want true (url=%q)", q.Get("load_balance"), got)
	}
	if q.Get("connect_timeout") != "5" {
		t.Errorf("connect_timeout = %q, want 5 (url=%q)", q.Get("connect_timeout"), got)
	}
	if q.Get("sslmode") != "disable" {
		t.Errorf("sslmode = %q, want disable", q.Get("sslmode"))
	}
	if pw, _ := parsed.User.Password(); pw != "secret" || parsed.User.Username() != "foghorn" {
		t.Errorf("userinfo not preserved: %q", parsed.User.String())
	}
}

func TestBuildDatabaseURLSingleNodeStaysVanilla(t *testing.T) {
	// Single-node Yugabyte and vanilla Postgres must keep the original
	// single-host form with no pgx-only params (so existing deploys/tests and
	// psql diagnostics are unaffected).
	cases := []struct {
		name string
		pg   *inventory.PostgresConfig
	}{
		{
			name: "single-node yugabyte",
			pg: &inventory.PostgresConfig{
				Enabled: true, Engine: "yugabyte", Port: 5433,
				Nodes: []inventory.PostgresNode{{Host: "yuga-eu-1", ID: 1}},
			},
		},
		{
			name: "vanilla postgres",
			pg:   &inventory.PostgresConfig{Enabled: true, Host: "pg-1", Port: 5432},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &inventory.Manifest{
				Hosts:          map[string]inventory.Host{"yuga-eu-1": {}, "pg-1": {}},
				Infrastructure: inventory.InfrastructureConfig{Postgres: tc.pg},
			}
			got := buildDatabaseURL(manifest, "host-1.internal", "5433", "svc", "", "svc")
			if want := "postgres://svc@host-1.internal:5433/svc?sslmode=disable"; got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}
