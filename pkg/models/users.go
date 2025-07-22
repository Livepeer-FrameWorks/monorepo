package models

import (
	"time"
)

// User represents a user (tenant-scoped)
type User struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenant_id"`
	Email        string     `json:"email"`
	Password     string     `json:"-"` // Never serialize password
	PasswordHash string     `json:"-"` // Alias for Password for backward compatibility
	FirstName    string     `json:"first_name"`
	LastName     string     `json:"last_name"`
	Role         string     `json:"role"`
	Permissions  []string   `json:"permissions"` // User permissions
	IsActive     bool       `json:"is_active"`
	IsVerified   bool       `json:"is_verified"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// APIToken represents an API token for programmatic access
type APIToken struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	UserID      string     `json:"user_id"`
	TokenName   string     `json:"token_name"`
	TokenValue  string     `json:"token_value"`
	Permissions []string   `json:"permissions"`
	IsActive    bool       `json:"is_active"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// RegisterRequest represents the registration request
type RegisterRequest struct {
	Email     string `json:"email" binding:"required,email"`
	Password  string `json:"password" binding:"required,min=6"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	// Bot protection fields
	PhoneNumber string `json:"phone_number"` // Honeypot - must be empty
	HumanCheck  string `json:"human_check"`  // Must be "human"
	Behavior    string `json:"behavior"`     // JSON string with interaction data
}

// LoginRequest represents the login request
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// CreateAPITokenRequest represents the API token creation request
type CreateAPITokenRequest struct {
	TokenName   string     `json:"token_name" binding:"required"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}
