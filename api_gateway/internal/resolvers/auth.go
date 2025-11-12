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
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("login").Observe(time.Since(start).Seconds())
		}
	}()

	// Call Commodore login endpoint
	authResp, err := r.Clients.Commodore.Login(ctx, email, password)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to authenticate user")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("login", "error").Inc()
		}
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("login", "success").Inc()
	}
	return authResp, nil
}

// DoRegister handles user registration business logic
func (r *Resolver) DoRegister(ctx context.Context, email, password, firstName, lastName string) (*commodore.AuthResponse, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("register").Observe(time.Since(start).Seconds())
		}
	}()

	// Call Commodore register endpoint
	authResp, err := r.Clients.Commodore.Register(ctx, email, password, firstName, lastName)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to register user")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("register", "error").Inc()
		}
		return nil, fmt.Errorf("registration failed: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("register", "success").Inc()
	}
	return authResp, nil
}

// DoGetMe retrieves current user information
func (r *Resolver) DoGetMe(ctx context.Context) (*models.User, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("getMe").Observe(time.Since(start).Seconds())
		}
	}()

	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning synthetic user profile")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("getMe", "demo").Inc()
		}
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
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("getMe", "auth_error").Inc()
		}
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get user info from Commodore
	user, err := r.Clients.Commodore.GetMe(ctx, userToken)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get user info")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("getMe", "error").Inc()
		}
		return nil, fmt.Errorf("failed to get user info: %w", err)
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("getMe", "success").Inc()
	}
	return user, nil
}
