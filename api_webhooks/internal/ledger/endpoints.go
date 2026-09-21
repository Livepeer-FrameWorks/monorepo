package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/webhooksig"

	"github.com/google/uuid"
)

const endpointColumns = `e.id, e.tenant_id, e.url, e.description, array_to_json(e.event_types)::text, e.api_version, e.status,
	COALESCE(e.disabled_reason, ''), e.disabled_at, e.consecutive_failures, e.failing_since, e.last_success_at,
	e.last_failure_at,
	(SELECT max(s.expires_at) FROM bosun.webhook_endpoint_secrets s
	  WHERE s.tenant_id = e.tenant_id AND s.endpoint_id = e.id AND s.state = 'previous' AND s.expires_at > now()),
	e.created_at, e.updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanEndpoint(row rowScanner) (Endpoint, error) {
	var (
		ep                                                          Endpoint
		eventTypes                                                  string
		disabledAt, failingSince, lastSuccess, lastFailure, prevExp sql.NullTime
	)
	if err := row.Scan(&ep.ID, &ep.TenantID, &ep.URL, &ep.Description, &eventTypes, &ep.APIVersion, &ep.Status,
		&ep.DisabledReason, &disabledAt, &ep.ConsecutiveFailures, &failingSince, &lastSuccess, &lastFailure,
		&prevExp, &ep.CreatedAt, &ep.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Endpoint{}, ErrNotFound
		}
		return Endpoint{}, err
	}
	types, err := jsonStrings(eventTypes)
	if err != nil {
		return Endpoint{}, err
	}
	ep.EventTypes = types
	ep.DisabledAt = timePtr(disabledAt)
	ep.FailingSince = timePtr(failingSince)
	ep.LastSuccessAt = timePtr(lastSuccess)
	ep.LastFailureAt = timePtr(lastFailure)
	ep.PreviousSecretExpiresAt = timePtr(prevExp)
	ep.CreatedAt = ep.CreatedAt.UTC()
	ep.UpdatedAt = ep.UpdatedAt.UTC()
	return ep, nil
}

// NewEndpoint is a validated endpoint to create.
type NewEndpoint struct {
	URL         string
	Description string
	EventTypes  []string
	APIVersion  string
}

// CreateEndpoint creates an endpoint with a new signing secret and returns
// both. It fails with ErrEndpointLimit when the tenant already has
// MaxEndpointsPerTenant endpoints.
func (s *Store) CreateEndpoint(ctx context.Context, tenantID string, in NewEndpoint) (Endpoint, webhooksig.Key, error) {
	key, ciphertext, secretErr := s.newSecret()
	if secretErr != nil {
		return Endpoint{}, nil, secretErr
	}
	endpointID := uuid.Must(uuid.NewV7()).String()
	var ep Endpoint
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		// The tenant row lock serializes concurrent creates; the check
		// constraint rejects the eleventh endpoint.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_tenants (tenant_id, endpoint_count) VALUES ($1, 1)
			ON CONFLICT (tenant_id) DO UPDATE
			SET endpoint_count = bosun.webhook_tenants.endpoint_count + 1, updated_at = now()`, tenantID); err != nil {
			if database.SQLState(err) == "23514" {
				return ErrEndpointLimit
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_endpoints (id, tenant_id, url, description, event_types, api_version)
			VALUES ($1, $2, $3, $4, (($5)::text)::text[], $6)`,
			endpointID, tenantID, in.URL, in.Description, arrayLiteral(in.EventTypes), in.APIVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_endpoint_secrets (id, tenant_id, endpoint_id, secret_ciphertext, state)
			VALUES ($1, $2, $3, $4, 'active')`,
			uuid.Must(uuid.NewV7()).String(), tenantID, endpointID, ciphertext); err != nil {
			return err
		}
		var scanErr error
		ep, scanErr = scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointColumns+` FROM bosun.webhook_endpoints e WHERE e.tenant_id = $1 AND e.id = $2`, tenantID, endpointID))
		return scanErr
	})
	if txErr != nil {
		return Endpoint{}, nil, txErr
	}
	return ep, key, nil
}

// newSecret generates a signing key and its field-encrypted text form.
func (s *Store) newSecret() (webhooksig.Key, string, error) {
	key, err := webhooksig.GenerateKey()
	if err != nil {
		return nil, "", err
	}
	ciphertext, err := s.Cipher.Encrypt(key.String())
	if err != nil {
		return nil, "", fmt.Errorf("encrypt signing secret: %w", err)
	}
	return key, ciphertext, nil
}

// GetEndpoint returns one endpoint of the tenant.
func (s *Store) GetEndpoint(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, ErrNotFound
	}
	return scanEndpoint(s.DB.QueryRowContext(ctx, `SELECT `+endpointColumns+` FROM bosun.webhook_endpoints e WHERE e.tenant_id = $1 AND e.id = $2`, tenantID, endpointID))
}

// ListEndpoints returns every endpoint of the tenant, oldest first.
func (s *Store) ListEndpoints(ctx context.Context, tenantID string) ([]Endpoint, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+endpointColumns+` FROM bosun.webhook_endpoints e WHERE e.tenant_id = $1 ORDER BY e.created_at, e.id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Endpoint
	for rows.Next() {
		ep, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// EndpointUpdate holds the fields to change; nil leaves a field unchanged.
type EndpointUpdate struct {
	URL         *string
	Description *string
	EventTypes  []string
}

// UpdateEndpoint changes an endpoint's URL, description, or event types.
func (s *Store) UpdateEndpoint(ctx context.Context, tenantID, endpointID string, in EndpointUpdate) (Endpoint, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, ErrNotFound
	}
	var eventTypes any
	if in.EventTypes != nil {
		eventTypes = arrayLiteral(in.EventTypes)
	}
	return scanEndpoint(s.DB.QueryRowContext(ctx, `
		UPDATE bosun.webhook_endpoints AS e
		SET url = COALESCE($3, e.url),
		    description = COALESCE($4, e.description),
		    event_types = COALESCE((($5)::text)::text[], e.event_types),
		    updated_at = now()
		WHERE e.tenant_id = $1 AND e.id = $2
		RETURNING `+endpointColumns, tenantID, endpointID, in.URL, in.Description, eventTypes))
}

// DeleteEndpoint deletes an endpoint with its secrets, deliveries, and
// attempts.
func (s *Store) DeleteEndpoint(ctx context.Context, tenantID, endpointID string) error {
	if _, err := uuid.Parse(endpointID); err != nil {
		return ErrNotFound
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2`, tenantID, endpointID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE bosun.webhook_tenants SET endpoint_count = endpoint_count - 1, updated_at = now()
			WHERE tenant_id = $1`, tenantID)
		return err
	})
}

// EnableEndpoint re-enables a disabled endpoint and resets its failure
// streak. Deliveries skipped while it was disabled stay skipped. Enabling an
// enabled endpoint changes nothing.
func (s *Store) EnableEndpoint(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, ErrNotFound
	}
	ep, err := scanEndpoint(s.DB.QueryRowContext(ctx, `
		UPDATE bosun.webhook_endpoints AS e
		SET status = 'enabled', disabled_reason = NULL, disabled_at = NULL,
		    consecutive_failures = 0, failing_since = NULL, updated_at = now()
		WHERE e.tenant_id = $1 AND e.id = $2 AND e.status = 'disabled'
		RETURNING `+endpointColumns, tenantID, endpointID))
	if errors.Is(err, ErrNotFound) {
		return s.GetEndpoint(ctx, tenantID, endpointID)
	}
	return ep, err
}

// DisableEndpoint disables an endpoint on the tenant's request and skips its
// pending deliveries. Disabling a disabled endpoint changes nothing.
func (s *Store) DisableEndpoint(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, ErrNotFound
	}
	var ep Endpoint
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, endpointID).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status == StatusEnabled {
			if _, err := disableTx(ctx, tx, tenantID, endpointID, DisabledByUser); err != nil {
				return err
			}
		}
		var scanErr error
		ep, scanErr = scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointColumns+` FROM bosun.webhook_endpoints e WHERE e.tenant_id = $1 AND e.id = $2`, tenantID, endpointID))
		return scanErr
	})
	return ep, txErr
}

// disableTx disables the endpoint and skips its pending deliveries, returning
// how many were skipped. A skipped delivery's lease is cleared, so a worker
// still sending it cannot settle it.
func disableTx(ctx context.Context, tx *sql.Tx, tenantID, endpointID, reason string) (int, error) {
	if _, err := tx.ExecContext(ctx, `
		UPDATE bosun.webhook_endpoints
		SET status = 'disabled', disabled_reason = $3, disabled_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2`, tenantID, endpointID, reason); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE bosun.webhook_deliveries
		SET status = 'skipped', finished_at = now(), lease_token = NULL, leased_until = NULL, updated_at = now()
		WHERE tenant_id = $1 AND endpoint_id = $2 AND status = 'pending'`, tenantID, endpointID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RotateSecret replaces the endpoint's signing secret. The current secret
// keeps signing for PreviousSecretValidity unless revokePrevious is set; an
// older previous secret is dropped either way.
func (s *Store) RotateSecret(ctx context.Context, tenantID, endpointID string, revokePrevious bool) (Endpoint, webhooksig.Key, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, nil, ErrNotFound
	}
	key, ciphertext, secretErr := s.newSecret()
	if secretErr != nil {
		return Endpoint{}, nil, secretErr
	}
	var ep Endpoint
	txErr := s.inTx(ctx, func(tx *sql.Tx) error {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenantID, endpointID).Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bosun.webhook_endpoint_secrets WHERE tenant_id = $1 AND endpoint_id = $2 AND state = 'previous'`, tenantID, endpointID); err != nil {
			return err
		}
		if revokePrevious {
			if _, err := tx.ExecContext(ctx, `DELETE FROM bosun.webhook_endpoint_secrets WHERE tenant_id = $1 AND endpoint_id = $2`, tenantID, endpointID); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE bosun.webhook_endpoint_secrets
			SET state = 'previous', expires_at = now() + make_interval(secs => $3)
			WHERE tenant_id = $1 AND endpoint_id = $2 AND state = 'active'`,
			tenantID, endpointID, PreviousSecretValidity.Seconds()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO bosun.webhook_endpoint_secrets (id, tenant_id, endpoint_id, secret_ciphertext, state)
			VALUES ($1, $2, $3, $4, 'active')`,
			uuid.Must(uuid.NewV7()).String(), tenantID, endpointID, ciphertext); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bosun.webhook_endpoints SET updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, endpointID); err != nil {
			return err
		}
		var scanErr error
		ep, scanErr = scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointColumns+` FROM bosun.webhook_endpoints e WHERE e.tenant_id = $1 AND e.id = $2`, tenantID, endpointID))
		return scanErr
	})
	if txErr != nil {
		return Endpoint{}, nil, txErr
	}
	return ep, key, nil
}

// SigningKeys returns the keys that sign the endpoint's deliveries now: the
// active secret first, then an unexpired previous secret.
func (s *Store) SigningKeys(ctx context.Context, tenantID, endpointID string) ([]webhooksig.Key, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT secret_ciphertext FROM bosun.webhook_endpoint_secrets
		WHERE tenant_id = $1 AND endpoint_id = $2 AND (state = 'active' OR expires_at > now())
		ORDER BY (state = 'active') DESC, created_at DESC`, tenantID, endpointID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var keys []webhooksig.Key
	for rows.Next() {
		var ciphertext string
		if err := rows.Scan(&ciphertext); err != nil {
			return nil, err
		}
		plaintext, err := s.Cipher.Decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("%w: decrypt: %w", ErrSigningKeyUnusable, err)
		}
		key, err := webhooksig.ParseSecret(plaintext)
		if err != nil {
			return nil, fmt.Errorf("%w: parse: %w", ErrSigningKeyUnusable, err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: endpoint %s has no signing secret", ErrSigningKeyUnusable, endpointID)
	}
	return keys, nil
}

// ClaimTestSlot records a test of the endpoint now, or fails with
// ErrTestRateLimited when one ran within TestInterval.
func (s *Store) ClaimTestSlot(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	if _, err := uuid.Parse(endpointID); err != nil {
		return Endpoint{}, ErrNotFound
	}
	ep, err := scanEndpoint(s.DB.QueryRowContext(ctx, `
		UPDATE bosun.webhook_endpoints AS e SET last_test_at = now()
		WHERE e.tenant_id = $1 AND e.id = $2
		  AND (e.last_test_at IS NULL OR e.last_test_at <= now() - make_interval(secs => $3))
		RETURNING `+endpointColumns, tenantID, endpointID, TestInterval.Seconds()))
	if errors.Is(err, ErrNotFound) {
		if _, getErr := s.GetEndpoint(ctx, tenantID, endpointID); getErr != nil {
			return Endpoint{}, getErr
		}
		return Endpoint{}, ErrTestRateLimited
	}
	return ep, err
}
