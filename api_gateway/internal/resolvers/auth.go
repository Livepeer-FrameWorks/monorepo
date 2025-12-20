package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/middleware"
	pb "frameworks/pkg/proto"
)

// DoLogin handles user authentication business logic
// Note: Auth is typically handled via REST (/auth/login), not GraphQL
func (r *Resolver) DoLogin(ctx context.Context, req *pb.LoginRequest) (*pb.AuthResponse, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("login").Observe(time.Since(start).Seconds())
		}
	}()

	// Call Commodore login endpoint
	authResp, err := r.Clients.Commodore.Login(ctx, req)
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
func (r *Resolver) DoRegister(ctx context.Context, email, password, firstName, lastName string) (*pb.RegisterResponse, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("register").Observe(time.Since(start).Seconds())
		}
	}()

	// Call Commodore register endpoint
	authResp, err := r.Clients.Commodore.Register(ctx, &pb.RegisterRequest{
		Email:     email,
		Password:  password,
		FirstName: firstName,
		LastName:  lastName,
	})
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
func (r *Resolver) DoGetMe(ctx context.Context) (*pb.User, error) {
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
		return &pb.User{
			Id:        "demo_user_developer",
			Email:     "developer@frameworks.demo",
			FirstName: "Demo",
			LastName:  "Developer",
		}, nil
	}

	// JWT token is in context, validated by middleware
	// gRPC uses metadata from context, not explicit token param
	var ok bool
	if v := ctx.Value("jwt_token"); v != nil {
		_, ok = v.(string)
	}
	if !ok {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("getMe", "auth_error").Inc()
		}
		return nil, fmt.Errorf("user not authenticated")
	}

	// Get user info from Commodore
	user, err := r.Clients.Commodore.GetMe(ctx)
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
