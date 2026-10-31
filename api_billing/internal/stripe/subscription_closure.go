package stripe

import (
	"context"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v85"
)

// HasLiveSubscription distinguishes recoverable provider states from terminal subscriptions.
func (c *Client) HasLiveSubscription(ctx context.Context, id string) (bool, error) {
	sub, err := c.GetSubscription(ctx, id)
	if err != nil {
		var providerErr *stripe.Error
		if errors.As(err, &providerErr) && providerErr.HTTPStatusCode == 404 {
			return false, nil
		}
		return false, err
	}
	if sub.Status == "" {
		return false, fmt.Errorf("stripe subscription %s has no status", id)
	}
	return sub.Status != stripe.SubscriptionStatusCanceled && sub.Status != stripe.SubscriptionStatusIncompleteExpired, nil
}

// CancelEstablishedSubscription ends future billing without refunding completed service.
func (c *Client) CancelEstablishedSubscription(ctx context.Context, id string) error {
	live, err := c.HasLiveSubscription(ctx, id)
	if err != nil || !live {
		return err
	}
	_, err = c.CancelSubscriptionImmediately(ctx, id)
	return err
}
