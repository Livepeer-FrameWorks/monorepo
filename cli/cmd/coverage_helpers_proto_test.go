package cmd

import (
	"slices"
	"testing"

	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

func TestNodeComponentVersions(t *testing.T) {
	t.Parallel()
	if nodeComponentVersions(nil) != "-" {
		t.Fatal("nil → -")
	}
	versions := []*foghorncontrolpb.NodeComponentVersion{
		{Component: "helmsman", Version: "v1.0.0"},
		nil,             // skipped
		{Component: ""}, // skipped (empty component)
		{Component: "mist", Version: "v2"},
	}
	got := nodeComponentVersions(versions)
	if got != "helmsman=v1.0.0,mist=v2" {
		t.Fatalf("got %q", got)
	}
	// All-empty after filtering → "-".
	if nodeComponentVersions([]*foghorncontrolpb.NodeComponentVersion{{Component: ""}}) != "-" {
		t.Fatal("all-filtered → -")
	}
}

func TestNodeDisplayName(t *testing.T) {
	t.Parallel()
	if nodeDisplayName(nil) != "" {
		t.Fatal("nil → empty")
	}
	if got := nodeDisplayName(&quartermasterpb.InfrastructureNode{NodeId: "n1", NodeName: "Edge One"}); got != "Edge One (n1)" {
		t.Fatalf("name distinct from id → name (id), got %q", got)
	}
	// Name empty or equal to ID → just the id.
	if got := nodeDisplayName(&quartermasterpb.InfrastructureNode{NodeId: "n1"}); got != "n1" {
		t.Fatalf("empty name → id, got %q", got)
	}
	if got := nodeDisplayName(&quartermasterpb.InfrastructureNode{NodeId: "n1", NodeName: "n1"}); got != "n1" {
		t.Fatalf("name==id → id, got %q", got)
	}
}

func TestSlicesEqual(t *testing.T) {
	t.Parallel()
	if !slicesEqual([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("identical slices should be equal")
	}
	if slicesEqual([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different length → not equal")
	}
	if slicesEqual([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("order matters")
	}
}

func TestUniqueExternalIPs(t *testing.T) {
	t.Parallel()
	nodes := []*quartermasterpb.InfrastructureNode{
		{ExternalIp: proto.String("10.0.0.2")},
		{ExternalIp: proto.String("10.0.0.1")},
		{ExternalIp: proto.String("10.0.0.2")}, // dup
		{ExternalIp: proto.String("")},         // skipped
	}
	got := uniqueExternalIPs(nodes)
	if !slices.Equal(got, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("expected sorted unique non-empty IPs, got %v", got)
	}
}

func TestClusterExternalIPs(t *testing.T) {
	t.Parallel()
	nodes := []*quartermasterpb.InfrastructureNode{
		{ClusterId: "eu", ExternalIp: proto.String("10.0.0.2")},
		{ClusterId: "eu", ExternalIp: proto.String("10.0.0.1")},
		{ClusterId: "eu", ExternalIp: proto.String("10.0.0.1")}, // dup within cluster
		{ClusterId: "us", ExternalIp: proto.String("10.1.0.1")},
		{ClusterId: "", ExternalIp: proto.String("10.9.9.9")}, // skipped (no cluster)
		{ClusterId: "us", ExternalIp: proto.String("")},       // skipped (no ip)
	}
	got := clusterExternalIPs(nodes)
	if !slices.Equal(got["eu"], []string{"10.0.0.1", "10.0.0.2"}) {
		t.Fatalf("eu IPs wrong: %v", got["eu"])
	}
	if !slices.Equal(got["us"], []string{"10.1.0.1"}) {
		t.Fatalf("us IPs wrong: %v", got["us"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty cluster id must be skipped")
	}
}

func TestClusterDisplayName(t *testing.T) {
	t.Parallel()
	if clusterDisplayName(nil) != "" {
		t.Fatal("nil cluster → empty string")
	}
	// Name distinct from ID → "name (id)".
	c := &quartermasterpb.InfrastructureCluster{
		ClusterId:   "eu-1",
		ClusterName: "Europe",
		ClusterType: "edge",
	}
	got := clusterDisplayName(c)
	if got != "Europe (eu-1) [edge]" {
		t.Fatalf("got %q", got)
	}
	// Name == ID → just the id, no parens.
	same := &quartermasterpb.InfrastructureCluster{ClusterId: "x", ClusterName: "x"}
	if clusterDisplayName(same) != "x" {
		t.Fatalf("got %q, want x", clusterDisplayName(same))
	}
	// Platform-official adds a tag.
	plat := &quartermasterpb.InfrastructureCluster{
		ClusterId:          "p",
		ClusterName:        "Prod",
		IsPlatformOfficial: true,
	}
	if got := clusterDisplayName(plat); got != "Prod (p) [platform-official]" {
		t.Fatalf("got %q", got)
	}
}
