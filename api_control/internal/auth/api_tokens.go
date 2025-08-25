package auth

import (
	"database/sql"
	"errors"
	"time"
)

var (
	ErrInvalidAPIToken = errors.New("invalid API token")
	ErrExpiredAPIToken = errors.New("API token expired")
)

// APIToken represents a developer API token
type APIToken struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	UserID      string    `json:"user_id"`
	TokenValue  string    `json:"token_value"`
	TokenName   string    `json:"token_name"`
	Permissions []string  `json:"permissions"`
	IsActive    bool      `json:"is_active"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// ValidateAPIToken validates a developer API token
func ValidateAPIToken(db *sql.DB, tokenValue string) (*APIToken, error) {
	var token APIToken

	// Get token from database
	err := db.QueryRow(`
		SELECT id, tenant_id, user_id, token_value, token_name, 
		       permissions, is_active, expires_at, created_at
		FROM commodore.api_tokens 
		WHERE token_value = $1 AND is_active = true
	`, tokenValue).Scan(
		&token.ID, &token.TenantID, &token.UserID, &token.TokenValue,
		&token.TokenName, &token.Permissions, &token.IsActive,
		&token.ExpiresAt, &token.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrInvalidAPIToken
	}

	if err != nil {
		return nil, err
	}

	// Check expiry
	if time.Now().After(token.ExpiresAt) {
		return nil, ErrExpiredAPIToken
	}

	return &token, nil
}

// HasPermission checks if an API token has a specific permission
func (t *APIToken) HasPermission(permission string) bool {
	for _, p := range t.Permissions {
		if p == permission {
			return true
		}
	}
	return false
}
