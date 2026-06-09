package stripe

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stripe/stripe-go/v85"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// subBackend is a stripe.Backend that returns canned Subscription /
// BillingPortalSession resources (or an error) so the thin SDK wrappers can be
// exercised without a network round-trip. It records call count for ordering
// assertions.
type subBackend struct {
	sub    *stripe.Subscription
	portal *stripe.BillingPortalSession
	err    error
	calls  int
}

func (b *subBackend) Call(method, path, key string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	b.calls++
	if b.err != nil {
		return b.err
	}
	switch out := v.(type) {
	case *stripe.Subscription:
		if b.sub != nil {
			*out = *b.sub
		}
	case *stripe.BillingPortalSession:
		if b.portal != nil {
			*out = *b.portal
		}
	}
	return nil
}

func (b *subBackend) CallStreaming(method, path, key string, params stripe.ParamsContainer, v stripe.StreamingLastResponseSetter) error {
	return nil
}
func (b *subBackend) CallRaw(method, path, key string, body []byte, params *stripe.Params, v stripe.LastResponseSetter) error {
	return nil
}
func (b *subBackend) CallMultipart(method, path, key, boundary string, body *bytes.Buffer, params *stripe.Params, v stripe.LastResponseSetter) error {
	return nil
}
func (b *subBackend) SetMaxNetworkRetries(maxNetworkRetries int64) {}

func newStripeClient() *Client { return &Client{logger: logging.NewLogger()} }

func TestGetSubscription(t *testing.T) {
	be := &subBackend{sub: &stripe.Subscription{ID: "sub_1"}}
	installBackend(t, be)

	got, err := newStripeClient().GetSubscription(context.Background(), "sub_1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.ID != "sub_1" {
		t.Fatalf("got %q, want sub_1", got.ID)
	}
}

func TestGetSubscriptionWrapsError(t *testing.T) {
	installBackend(t, &subBackend{err: errors.New("boom")})
	_, err := newStripeClient().GetSubscription(context.Background(), "sub_1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCancelSubscription(t *testing.T) {
	installBackend(t, &subBackend{sub: &stripe.Subscription{ID: "sub_1"}})
	got, err := newStripeClient().CancelSubscription(context.Background(), "sub_1")
	if err != nil || got.ID != "sub_1" {
		t.Fatalf("CancelSubscription got (%+v, %v)", got, err)
	}
}

func TestCancelSubscriptionImmediately(t *testing.T) {
	installBackend(t, &subBackend{sub: &stripe.Subscription{ID: "sub_1"}})
	got, err := newStripeClient().CancelSubscriptionImmediately(context.Background(), "sub_1")
	if err != nil || got.ID != "sub_1" {
		t.Fatalf("CancelSubscriptionImmediately got (%+v, %v)", got, err)
	}
}

func TestUpdateSubscription(t *testing.T) {
	// Get then Update both hit the backend; the subscription needs at least one
	// item for the price swap to target.
	be := &subBackend{sub: &stripe.Subscription{
		ID: "sub_1",
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{{ID: "si_1"}},
		},
	}}
	installBackend(t, be)

	got, err := newStripeClient().UpdateSubscription(context.Background(), "sub_1", "price_new")
	if err != nil {
		t.Fatalf("UpdateSubscription: %v", err)
	}
	if got.ID != "sub_1" {
		t.Fatalf("got %q, want sub_1", got.ID)
	}
	if be.calls < 2 {
		t.Fatalf("expected a Get + Update (>=2 calls), got %d", be.calls)
	}
}

func TestUpdateSubscriptionNoItems(t *testing.T) {
	// Non-nil but empty item list exercises the intended "no items" guard.
	// (A nil sub.Items would nil-deref at client.go:404 — real Stripe always
	// returns a populated Items list, so that path is unguarded by design.)
	installBackend(t, &subBackend{sub: &stripe.Subscription{
		ID:    "sub_1",
		Items: &stripe.SubscriptionItemList{Data: []*stripe.SubscriptionItem{}},
	}})
	_, err := newStripeClient().UpdateSubscription(context.Background(), "sub_1", "price_new")
	if err == nil {
		t.Fatal("subscription with no items should error")
	}
}

func TestCreateBillingPortalSession(t *testing.T) {
	installBackend(t, &subBackend{portal: &stripe.BillingPortalSession{URL: "https://portal.example"}})
	sess, err := newStripeClient().CreateBillingPortalSession(context.Background(), "cus_1", "https://return")
	if err != nil {
		t.Fatalf("CreateBillingPortalSession: %v", err)
	}
	if sess.URL != "https://portal.example" {
		t.Fatalf("portal url = %q", sess.URL)
	}
}
