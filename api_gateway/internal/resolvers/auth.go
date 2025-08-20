package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/middleware"
	commodore "frameworks/pkg/api/commodore"
	"frameworks/pkg/models"
)

// DoLogin handles user authentication business logic
func (r *Resolver) DoLogin(ctx context.Context, email, password string) (*commodore.AuthResponse, error) {
	// Call Commodore login endpoint
	authResp, err := r.Clients.Commodore.Login(ctx, email, password)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to authenticate user")
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	return authResp, nil
}

// DoRegister handles user registration business logic
func (r *Resolver) DoRegister(ctx context.Context, email, password, firstName, lastName string) (*commodore.AuthResponse, error) {
	// Call Commodore register endpoint
	authResp, err := r.Clients.Commodore.Register(ctx, email, password, firstName, lastName)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to register user")
		return nil, fmt.Errorf("registration failed: %w", err)
	}

	return authResp, nil
}

// DoGetMe retrieves current user information
func (r *Resolver) DoGetMe(ctx context.Context) (*models.User, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo user data")
		// Return demo user data without calling Commodore
		return &models.User{
			ID:        "demo_user_developer",
			Email:     "developer@frameworks.demo",
			FirstName: "Demo",
			LastName:  "Developer",
			CreatedAt: time.Now().Add(-90 * 24 * time.Hour),
		}, nil
	}

	// Extract JWT token from context (set by auth middleware)
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get user info from Commodore
	user, err := r.Clients.Commodore.GetMe(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get user info")
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	return user, nil
}
