package stripe

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/stripe/stripe-go/v85"
)

// productBackend answers product retrieval from a fixed catalog and records
// every request, so a test can see whether a sync searched or created.
type productBackend struct {
	products map[string]stripe.Product
	calls    []string
}

func (b *productBackend) Call(method, path, _ string, _ stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	b.calls = append(b.calls, method+" "+path)
	if method == http.MethodGet {
		id := path[len("/v1/products/"):]
		prod, ok := b.products[id]
		if !ok {
			return &stripe.Error{HTTPStatusCode: http.StatusNotFound, Code: stripe.ErrorCodeResourceMissing}
		}
		*v.(*stripe.Product) = prod
		return nil
	}
	if out, ok := v.(*stripe.Product); ok {
		*out = stripe.Product{ID: "prod_created"}
	}
	return nil
}

func (b *productBackend) CallRaw(method, path, _ string, _ []byte, _ *stripe.Params, _ stripe.LastResponseSetter) error {
	b.calls = append(b.calls, method+" "+path)
	return nil
}

func (b *productBackend) CallStreaming(string, string, string, stripe.ParamsContainer, stripe.StreamingLastResponseSetter) error {
	return nil
}

func (b *productBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *stripe.Params, stripe.LastResponseSetter) error {
	return nil
}

func (b *productBackend) SetMaxNetworkRetries(int64) {}

// A replica that syncs right after another must use the product the tier row
// records: Stripe's product search may not list it yet, and creating another
// would leave two products for one tier.
func TestFindOrCreateProductUsesTheRecordedProduct(t *testing.T) {
	backend := &productBackend{products: map[string]stripe.Product{
		"prod_known": {ID: "prod_known", Active: true, Metadata: map[string]string{"tier_name": "pro"}},
	}}
	installBackend(t, backend)
	c := &Client{logger: logging.NewLogger()}

	prod, err := c.FindOrCreateProduct(context.Background(), "prod_known", "pro", "Pro", "")
	if err != nil {
		t.Fatal(err)
	}
	if prod.ID != "prod_known" {
		t.Fatalf("product = %s, want the recorded prod_known", prod.ID)
	}
	if len(backend.calls) != 1 || backend.calls[0] != "GET /v1/products/prod_known" {
		t.Fatalf("Stripe calls = %v, want only the recorded product's retrieval", backend.calls)
	}
}

func TestFindOrCreateProductSearchesWhenTheRecordedProductIsGone(t *testing.T) {
	backend := &productBackend{products: map[string]stripe.Product{
		"prod_other_tier": {ID: "prod_other_tier", Active: true, Metadata: map[string]string{"tier_name": "starter"}},
	}}
	installBackend(t, backend)
	c := &Client{logger: logging.NewLogger()}

	for _, known := range []string{"prod_deleted", "prod_other_tier"} {
		backend.calls = nil
		prod, err := c.FindOrCreateProduct(context.Background(), known, "pro", "Pro", "")
		if err != nil {
			t.Fatalf("recorded %s: %v", known, err)
		}
		want := []string{"GET /v1/products/" + known, "GET /v1/products/search", "POST /v1/products"}
		if len(backend.calls) != len(want) {
			t.Fatalf("recorded %s: Stripe calls = %v, want %v", known, backend.calls, want)
		}
		for i := range want {
			if backend.calls[i] != want[i] {
				t.Fatalf("recorded %s: Stripe calls = %v, want %v", known, backend.calls, want)
			}
		}
		if prod.ID != "prod_created" {
			t.Fatalf("recorded %s: product = %s, want a new product", known, prod.ID)
		}
	}
}
