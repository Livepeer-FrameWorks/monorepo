package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/pkg/geoip"
)

type fakeGeoLookup struct {
	data *geoip.GeoData
}

func (f fakeGeoLookup) Lookup(string) *geoip.GeoData {
	return f.data
}

func TestReconcileNodesInsertsGeoCoordinates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	node := Node{
		ID:         "central-eu-1",
		ClusterID:  "core-central-primary",
		Type:       "core",
		ExternalIP: "203.0.113.10",
		WireGuard: NodeWireGuard{
			IP:        "10.88.0.10",
			PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			Port:      51820,
		},
	}

	mock.ExpectQuery(regexp.QuoteMeta("FROM quartermaster.infrastructure_nodes")).
		WithArgs(node.ID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO quartermaster.infrastructure_nodes")).
		WithArgs(
			node.ID, node.ClusterID, node.ID, node.Type,
			node.ExternalIP, node.WireGuard.IP, node.WireGuard.PublicKey, node.WireGuard.Port,
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileNodesWithOptions(context.Background(), db, []Node{node}, NodeOptions{
		GeoIPReader: fakeGeoLookup{data: &geoip.GeoData{Latitude: 52.37, Longitude: 4.89}},
	})
	if err != nil {
		t.Fatalf("ReconcileNodesWithOptions: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != node.ID {
		t.Fatalf("created = %+v, want [%s]", res.Created, node.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestReconcileNodesBackfillsMissingGeoCoordinates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	node := Node{
		ID:         "central-eu-1",
		ClusterID:  "core-central-primary",
		Type:       "core",
		ExternalIP: "203.0.113.10",
		WireGuard: NodeWireGuard{
			IP:        "10.88.0.10",
			PublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			Port:      51820,
		},
	}

	mock.ExpectQuery(regexp.QuoteMeta("FROM quartermaster.infrastructure_nodes")).
		WithArgs(node.ID).
		WillReturnRows(sqlmock.NewRows([]string{
			"node_name", "node_type", "cluster_id", "external_ip", "wireguard_ip",
			"wireguard_public_key", "wireguard_listen_port", "latitude", "longitude",
		}).AddRow(node.ID, node.Type, node.ClusterID, node.ExternalIP, node.WireGuard.IP, node.WireGuard.PublicKey, node.WireGuard.Port, nil, nil))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE quartermaster.infrastructure_nodes")).
		WithArgs(
			node.ID, node.ID, node.Type, node.WireGuard.Port,
			sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileNodesWithOptions(context.Background(), db, []Node{node}, NodeOptions{
		GeoIPReader: fakeGeoLookup{data: &geoip.GeoData{Latitude: 52.37, Longitude: 4.89}},
	})
	if err != nil {
		t.Fatalf("ReconcileNodesWithOptions: %v", err)
	}
	if len(res.Updated) != 1 || res.Updated[0] != node.ID {
		t.Fatalf("updated = %+v, want [%s]", res.Updated, node.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestSameHostIPTreatsHostPrefixAsSameAddress(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"91.99.236.223/32", "91.99.236.223", true},
		{"2a01:7c8:aaca:2ec::1/128", "2a01:7c8:aaca:2ec::1", true},
		{"10.88.156.88/32", "10.88.156.88/32", true},
		{"91.99.236.223/31", "91.99.236.223", false},
		{"91.99.236.223", "91.99.236.224", false},
		{"not-an-ip", "not-an-ip", true},
		{"not-an-ip", "91.99.236.223", false},
	}
	for _, tc := range cases {
		if got := sameHostIP(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameHostIP(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
