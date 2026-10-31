package subscriptions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// ProviderRequest is the original commercial request, retained before external I/O.
// The credential digest prevents replay after a key rotation changed idempotency scope.
type ProviderRequest struct {
	Kind         string          `json:"kind"`
	Params       json.RawMessage `json:"params"`
	Credential   string          `json:"credential"`
	ReplayBefore time.Time       `json:"replay_before"`
}

// ErrCheckoutRequestChanged refuses replay under different provider identity or purpose.
var ErrCheckoutRequestChanged = errors.New("checkout request credential or purpose changed; recover the original checkout")

// SaveProviderRequest retains exact parameters under the same lock as recovery.
func SaveProviderRequest(ctx context.Context, db *sql.DB, tenantID, intentID, provider, kind, credential string, params any) (ProviderRequest, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return ProviderRequest{}, err
	}
	var window time.Duration
	switch provider {
	case "stripe":
		window = 23 * time.Hour
	case "mollie":
		window = 45 * time.Minute
	default:
		return ProviderRequest{}, errors.New("unsupported checkout provider")
	}
	request := ProviderRequest{Kind: kind, Params: raw, Credential: credential, ReplayBefore: time.Now().Add(window)}
	encoded, err := json.Marshal(request)
	if err != nil {
		return ProviderRequest{}, err
	}
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		if _, lockErr := LockChange(ctx, tx, tenantID, ChangePricing); lockErr != nil {
			return lockErr
		}
		saved, saveErr := purserdb.New(tx).SaveCheckoutProviderRequest(ctx, purserdb.SaveCheckoutProviderRequestParams{TenantID: tenantID, IntentID: intentID, Request: encoded})
		if errors.Is(saveErr, sql.ErrNoRows) {
			return ErrCheckoutExpired
		}
		if saveErr != nil {
			return saveErr
		}
		if len(saved.ProviderRequest) == 0 {
			request.ReplayBefore = time.Time{}
			return nil
		}
		return json.Unmarshal(saved.ProviderRequest, &request)
	})
	if err != nil {
		return ProviderRequest{}, err
	}
	if request.Kind != kind || request.Credential != credential {
		return ProviderRequest{}, ErrCheckoutRequestChanged
	}
	if err = json.Unmarshal(request.Params, params); err != nil {
		return ProviderRequest{}, err
	}
	return request, nil
}
