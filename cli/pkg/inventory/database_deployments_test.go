package inventory

import "testing"

func TestDatabaseDeploymentManifestParses(t *testing.T) {
	_, err := ParseManifest([]byte(`
infrastructure:
  database_deployments:
    us-east:
      enabled: true
      engine: yugabyte
      replication_factor: 3
      placement_cloud: hetzner
      placement_region: us-east
      nodes:
        - {host: us-1, id: 1, placement_zone: ash-dc1}
        - {host: us-2, id: 2, placement_zone: ash-dc1}
        - {host: us-3, id: 3, placement_zone: ash-dc1}
      databases:
        - {name: foghorn, owner: foghorn}
services:
  foghorn-us:
    enabled: true
    deploy: foghorn
    database_deployment: us-east
`))
	if err != nil {
		t.Fatalf("independent US database deployment must parse: %v", err)
	}
}

func TestDatabaseDeploymentPortsCannotCollide(t *testing.T) {
	m := &Manifest{
		Infrastructure: InfrastructureConfig{DatabaseDeployments: map[string]*PostgresConfig{
			"us": {Enabled: true, Engine: "yugabyte", Nodes: []PostgresNode{{Host: "us", ID: 1}}},
		}},
		Services: map[string]ServiceConfig{"bridge": {Enabled: true, Host: "us", Port: 5433}},
	}
	if err := m.validatePortCollisions(); err == nil {
		t.Fatal("US Yugabyte YSQL port collision was ignored")
	}
}

func TestDatabaseDeploymentsRejectUnsafeTopology(t *testing.T) {
	newManifest := func() *Manifest {
		return &Manifest{
			Hosts: map[string]Host{"eu": {}, "us-1": {}, "us-2": {}, "us-3": {}},
			Infrastructure: InfrastructureConfig{
				Postgres: &PostgresConfig{Enabled: true, Host: "eu"},
				DatabaseDeployments: map[string]*PostgresConfig{"us-east": {
					Enabled: true, Engine: "yugabyte", ReplicationFactor: 3,
					PlacementCloud: "hetzner", PlacementRegion: "us-east",
					Nodes: []PostgresNode{{Host: "us-1", ID: 1, PlacementZone: "ash-dc1"}, {Host: "us-2", ID: 2, PlacementZone: "ash-dc1"}, {Host: "us-3", ID: 3, PlacementZone: "ash-dc1"}},
				}},
			},
			Services: map[string]ServiceConfig{"foghorn-us": {Enabled: true, DatabaseDeployment: "us-east"}},
		}
	}
	if err := newManifest().validateDatabaseDeployments(); err != nil {
		t.Fatalf("valid regional RF3 deployment: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
	}{
		{"two voters", func(m *Manifest) {
			m.Infrastructure.DatabaseDeployments["us-east"].Nodes = m.Infrastructure.DatabaseDeployments["us-east"].Nodes[:2]
		}},
		{"under-replicated region", func(m *Manifest) { m.Infrastructure.DatabaseDeployments["us-east"].ReplicationFactor = 1 }},
		{"same native host in two universes", func(m *Manifest) { m.Infrastructure.DatabaseDeployments["us-east"].Nodes[0].Host = "eu" }},
		{"unknown service binding", func(m *Manifest) { m.Services["foghorn-us"] = ServiceConfig{Enabled: true, DatabaseDeployment: "typo"} }},
		{"missing actual zone", func(m *Manifest) { m.Infrastructure.DatabaseDeployments["us-east"].Nodes[0].PlacementZone = "" }},
		{"postgres pretending to have managed HA", func(m *Manifest) { m.Infrastructure.DatabaseDeployments["us-east"].Engine = "postgres" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newManifest()
			tc.change(m)
			if err := m.validateDatabaseDeployments(); err == nil {
				t.Fatal("unsafe topology was accepted")
			}
		})
	}
}
