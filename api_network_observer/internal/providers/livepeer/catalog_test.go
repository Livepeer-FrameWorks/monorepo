package livepeer

import (
	"context"
	"testing"
)

func TestDialCatalogKeepsNetworkAndEnvironmentDistinct(t *testing.T) {
	catalog, closeClient, err := DialCatalog(context.Background(), "http://127.0.0.1:1", "0x1111111111111111111111111111111111111111", "42161", "staging")
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient()
	if catalog.ExpectedNetwork != "42161" || catalog.Environment != "staging" {
		t.Fatalf("swapped catalog namespace: network=%s environment=%s", catalog.ExpectedNetwork, catalog.Environment)
	}
}
