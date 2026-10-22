package stripe

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/shopspring/decimal"
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

// idempotentBackend models Stripe for two replicas syncing one tier at once:
// search and list do not show objects created moments ago, and a create
// carrying an idempotency key it has seen returns the object that key created.
type idempotentBackend struct {
	mu       sync.Mutex
	created  map[string]int
	byKey    map[string]string
	keysUsed []string
}

func newIdempotentBackend() *idempotentBackend {
	return &idempotentBackend{created: map[string]int{}, byKey: map[string]string{}}
}

func (b *idempotentBackend) create(kind string, params stripe.ParamsContainer) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := ""
	if params != nil && params.GetParams().IdempotencyKey != nil {
		key = *params.GetParams().IdempotencyKey
	}
	if id, ok := b.byKey[kind+" "+key]; ok && key != "" {
		return id
	}
	b.created[kind]++
	id := fmt.Sprintf("%s_%d", kind, b.created[kind])
	if key != "" {
		b.byKey[kind+" "+key] = id
		b.keysUsed = append(b.keysUsed, key)
	}
	return id
}

func (b *idempotentBackend) Call(method, path, _ string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	if method != http.MethodPost {
		return nil
	}
	switch out := v.(type) {
	case *stripe.Product:
		*out = stripe.Product{ID: b.create("prod", params)}
	case *stripe.Price:
		*out = stripe.Price{ID: b.create("price", params)}
	default:
		return fmt.Errorf("unexpected Stripe call %s %s", method, path)
	}
	return nil
}

func (b *idempotentBackend) CallRaw(string, string, string, []byte, *stripe.Params, stripe.LastResponseSetter) error {
	return nil
}

func (b *idempotentBackend) CallStreaming(string, string, string, stripe.ParamsContainer, stripe.StreamingLastResponseSetter) error {
	return nil
}

func (b *idempotentBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *stripe.Params, stripe.LastResponseSetter) error {
	return nil
}

func (b *idempotentBackend) SetMaxNetworkRetries(int64) {}

// Two replicas sync a new tier at once and neither sees the other's product
// or price in Stripe's search and list: they must still create one product
// and one price, not one each.
func TestConcurrentTierSyncsCreateOneProductAndPrice(t *testing.T) {
	backend := newIdempotentBackend()
	installBackend(t, backend)
	c := &Client{logger: logging.NewLogger()}

	var wg sync.WaitGroup
	results := make([][2]string, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			productID, priceID, _, err := c.SyncTier(context.Background(), "", "", "pro", "Pro", "For teams", decimal.RequireFromString("29.95"), "EUR")
			if err != nil {
				t.Errorf("sync %d: %v", i, err)
			}
			results[i] = [2]string{productID, priceID}
		}(i)
	}
	wg.Wait()
	if backend.created["prod"] != 1 || backend.created["price"] != 1 {
		t.Fatalf("Stripe created %d products and %d prices for one tier, want 1 and 1", backend.created["prod"], backend.created["price"])
	}
	if results[0] != results[1] {
		t.Fatalf("replicas recorded different Stripe IDs: %v and %v", results[0], results[1])
	}
}

// A changed price never replays an earlier create: a new amount is a new
// price, and so is a return to an earlier amount, whose earlier price the
// intervening change deactivated.
func TestTierPriceChangesCreateNewPrices(t *testing.T) {
	backend := newIdempotentBackend()
	installBackend(t, backend)
	c := &Client{logger: logging.NewLogger()}
	recordedPrice := ""
	for i, amount := range []string{"29.95", "39.95", "29.95"} {
		_, priceID, created, err := c.SyncTier(context.Background(), "prod_known", recordedPrice, "pro", "Pro", "", decimal.RequireFromString(amount), "EUR")
		if err != nil {
			t.Fatal(err)
		}
		if !created {
			t.Fatalf("sync %d at %s did not report the price it created", i, amount)
		}
		if want := fmt.Sprintf("price_%d", i+1); priceID != want {
			t.Fatalf("sync %d at %s recorded %s, want the new %s", i, amount, priceID, want)
		}
		recordedPrice = priceID
	}
	for _, key := range backend.keysUsed {
		if len(key) > 255 {
			t.Fatalf("idempotency key %q is longer than Stripe's 255 characters", key)
		}
	}
}
