package mollie

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// BalanceTransaction is one movement on a Mollie balance. InitialAmount is the
// gross amount in the balance currency, Deductions the fees (negative), and
// ResultAmount the net.
type BalanceTransaction struct {
	ID              string       `json:"id"`
	TransactionType string       `json:"type"`
	LegacyType      string       `json:"transactionType"`
	ResultAmount    *Money       `json:"resultAmount"`
	InitialAmount   *Money       `json:"initialAmount"`
	Deductions      *Money       `json:"deductions"`
	CreatedAt       time.Time    `json:"createdAt"`
	Context         contextField `json:"context"`
}

// Money is a Mollie money value.
type Money struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// Type returns the transaction type from either API field name.
func (t BalanceTransaction) Type() string {
	if t.TransactionType != "" {
		return t.TransactionType
	}
	return t.LegacyType
}

// PaymentID returns the payment a payment transaction belongs to.
func (t BalanceTransaction) PaymentID() string {
	if t.Context.PaymentID != "" {
		return t.Context.PaymentID
	}
	return t.Context.Payment.PaymentID
}

type contextField struct {
	PaymentID string `json:"paymentId"`
	Payment   struct {
		PaymentID string `json:"paymentId"`
	} `json:"payment"`
}

// BalanceTransactionPage is one page of balance transactions, newest first.
// NextFrom is the transaction ID the next, older page starts from, or empty on
// the last page.
type BalanceTransactionPage struct {
	Transactions []BalanceTransaction
	NextFrom     string
}

// PrimaryBalanceID returns the ID of the organization's primary balance.
func (c *Client) PrimaryBalanceID(ctx context.Context) (string, error) {
	u, err := c.mollieURL("v2/balances/primary")
	if err != nil {
		return "", err
	}
	body, err := c.getJSON(ctx, u.String())
	if err != nil {
		return "", fmt.Errorf("get Mollie primary balance: %w", err)
	}
	var balance struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &balance); err != nil {
		return "", fmt.Errorf("decode Mollie primary balance: %w", err)
	}
	if balance.ID == "" {
		return "", fmt.Errorf("mollie primary balance has no id")
	}
	return balance.ID, nil
}

// ListBalanceTransactions returns one page of a balance's transactions, newest
// first, starting at from when it is set.
func (c *Client) ListBalanceTransactions(ctx context.Context, balanceID, from string, limit int) (*BalanceTransactionPage, error) {
	u, err := c.mollieURL(fmt.Sprintf("v2/balances/%s/transactions", url.PathEscape(balanceID)))
	if err != nil {
		return nil, err
	}
	query := u.Query()
	if from != "" {
		query.Set("from", from)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	u.RawQuery = query.Encode()
	body, err := c.getJSON(ctx, u.String())
	if err != nil {
		return nil, fmt.Errorf("list Mollie balance transactions: %w", err)
	}
	var list struct {
		Embedded struct {
			BalanceTransactions []BalanceTransaction `json:"balance_transactions"`
		} `json:"_embedded"`
		Links struct {
			Next *struct {
				Href string `json:"href"`
			} `json:"next"`
		} `json:"_links"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("decode Mollie balance transactions: %w", err)
	}
	page := &BalanceTransactionPage{Transactions: list.Embedded.BalanceTransactions}
	if list.Links.Next != nil && list.Links.Next.Href != "" {
		next, parseErr := url.Parse(list.Links.Next.Href)
		if parseErr != nil {
			return nil, fmt.Errorf("parse Mollie next page link: %w", parseErr)
		}
		page.NextFrom = next.Query().Get("from")
	}
	return page, nil
}

func (c *Client) getJSON(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build Mollie request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read Mollie response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("mollie status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}
