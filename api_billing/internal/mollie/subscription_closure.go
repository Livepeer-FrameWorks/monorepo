package mollie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
)

// HasLiveSubscription distinguishes recoverable provider states from terminal subscriptions.
func (c *Client) HasLiveSubscription(ctx context.Context, customerID, id string) (bool, error) {
	u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/subscriptions/" + url.PathEscape(id))
	if err != nil {
		return false, err
	}
	body, err := c.getJSON(ctx, u.String())
	if err != nil {
		var providerErr *APIError
		if errors.As(err, &providerErr) && providerErr.StatusCode == 404 {
			return false, nil
		}
		return false, err
	}
	var sub struct {
		Status string `json:"status"`
	}
	if decodeErr := json.Unmarshal(body, &sub); decodeErr != nil {
		return false, decodeErr
	}
	if sub.Status == "" {
		return false, fmt.Errorf("mollie subscription %s has no status", id)
	}
	return sub.Status != "canceled" && sub.Status != "completed", nil
}

// CancelEstablishedSubscription stops recurring payments and retains settled charges.
func (c *Client) CancelEstablishedSubscription(ctx context.Context, customerID, id string) error {
	live, err := c.HasLiveSubscription(ctx, customerID, id)
	if err != nil || !live {
		return err
	}
	return c.CancelSubscription(ctx, customerID, id)
}
