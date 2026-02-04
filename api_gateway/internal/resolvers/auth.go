package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	gatewayerrors "frameworks/api_gateway/internal/errors"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/ctxkeys"
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
		return nil, fmt.Errorf("authentication failed")
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
		return nil, fmt.Errorf("registration failed")
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
		demoEmail := "developer@frameworks.demo"
		return &pb.User{
			Id:        "demo_user_developer",
			Email:     &demoEmail,
			FirstName: "Demo",
			LastName:  "Developer",
		}, nil
	}

	// JWT token is in context, validated by middleware
	// gRPC uses metadata from context, not explicit token param
	if ctxkeys.GetJWTToken(ctx) == "" {
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
		return nil, fmt.Errorf("failed to get user info")
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("getMe", "success").Inc()
	}
	return user, nil
}

// DoWalletLogin authenticates a user via wallet signature
func (r *Resolver) DoWalletLogin(ctx context.Context, input model.WalletLoginInput) (model.WalletLoginResult, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("walletLogin").Observe(time.Since(start).Seconds())
		}
	}()

	// Call Commodore wallet login
	authResp, err := r.Clients.Commodore.WalletLogin(ctx, input.Address, input.Message, input.Signature)
	if err != nil {
		r.Logger.WithError(err).Error("Wallet login failed")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("walletLogin", "error").Inc()
		}
		message := gatewayerrors.SanitizeGRPCError(err, "wallet authentication failed", []string{"signature", "expired"})
		return &model.ValidationError{
			Message: message,
			Code:    ptrString("WALLET_AUTH_FAILED"),
		}, nil
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("walletLogin", "success").Inc()
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	if authResp.ExpiresAt != nil {
		expiresAt = authResp.ExpiresAt.AsTime()
	}

	return &model.WalletLoginPayload{
		Token:        authResp.Token,
		User:         authResp.User,
		ExpiresAt:    expiresAt,
		IsNewAccount: authResp.IsNewUser,
	}, nil
}

// DoLinkWallet links a wallet to the current user's account
func (r *Resolver) DoLinkWallet(ctx context.Context, input model.WalletLoginInput) (model.LinkWalletResult, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("linkWallet").Observe(time.Since(start).Seconds())
		}
	}()

	// Requires authenticated user
	if ctxkeys.GetUserID(ctx) == "" {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("linkWallet", "auth_error").Inc()
		}
		return &model.AuthError{
			Message: "Authentication required",
			Code:    ptrString("UNAUTHENTICATED"),
		}, nil
	}

	// Call Commodore link wallet
	walletPb, err := r.Clients.Commodore.LinkWallet(ctx, input.Address, input.Message, input.Signature)
	if err != nil {
		r.Logger.WithError(err).Error("Link wallet failed")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("linkWallet", "error").Inc()
		}
		message := gatewayerrors.SanitizeGRPCError(err, "link wallet failed", []string{"already linked", "signature"})
		return &model.ValidationError{
			Message: message,
			Code:    ptrString("LINK_WALLET_FAILED"),
		}, nil
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("linkWallet", "success").Inc()
	}

	// Convert proto to model
	wallet := protoToWalletIdentity(walletPb)
	return &wallet, nil
}

// DoUnlinkWallet removes a wallet from the current user's account
func (r *Resolver) DoUnlinkWallet(ctx context.Context, walletID string) (model.UnlinkWalletResult, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("unlinkWallet").Observe(time.Since(start).Seconds())
		}
	}()

	// Requires authenticated user
	if ctxkeys.GetUserID(ctx) == "" {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("unlinkWallet", "auth_error").Inc()
		}
		return &model.AuthError{
			Message: "Authentication required",
			Code:    ptrString("UNAUTHENTICATED"),
		}, nil
	}

	// Call Commodore unlink wallet
	resp, err := r.Clients.Commodore.UnlinkWallet(ctx, walletID)
	if err != nil {
		r.Logger.WithError(err).Error("Unlink wallet failed")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("unlinkWallet", "error").Inc()
		}
		message := gatewayerrors.SanitizeGRPCError(err, "wallet not found", []string{"not found"})
		return &model.NotFoundError{
			Message:      message,
			Code:         ptrString("WALLET_NOT_FOUND"),
			ResourceType: "WalletIdentity",
		}, nil
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("unlinkWallet", "success").Inc()
	}

	return &model.DeleteSuccess{
		DeletedID: walletID,
		Success:   resp.Success,
	}, nil
}

// DoLinkEmail adds an email to a wallet-only account (for postpaid upgrade path)
func (r *Resolver) DoLinkEmail(ctx context.Context, input model.LinkEmailInput) (model.LinkEmailResult, error) {
	start := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.Duration.WithLabelValues("linkEmail").Observe(time.Since(start).Seconds())
		}
	}()

	// Requires authenticated user
	if ctxkeys.GetUserID(ctx) == "" {
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("linkEmail", "auth_error").Inc()
		}
		return &model.AuthError{
			Message: "Authentication required",
			Code:    ptrString("UNAUTHENTICATED"),
		}, nil
	}

	// Call Commodore to link email
	resp, err := r.Clients.Commodore.LinkEmail(ctx, input.Email, input.Password)
	if err != nil {
		r.Logger.WithError(err).Error("Link email failed")
		if r.Metrics != nil {
			r.Metrics.Operations.WithLabelValues("linkEmail", "error").Inc()
		}
		message := gatewayerrors.SanitizeGRPCError(err, "email link failed", []string{"already linked", "invalid"})
		return &model.ValidationError{
			Message: message,
			Code:    ptrString("EMAIL_LINK_FAILED"),
			Field:   ptrString("email"),
		}, nil
	}

	if r.Metrics != nil {
		r.Metrics.Operations.WithLabelValues("linkEmail", "success").Inc()
	}

	return &model.LinkEmailPayload{
		Success:          resp.Success,
		Message:          resp.Message,
		VerificationSent: resp.VerificationSent,
	}, nil
}

// protoToWalletIdentity converts proto WalletIdentity to model WalletIdentity
func protoToWalletIdentity(w *pb.WalletIdentity) model.WalletIdentity {
	result := model.WalletIdentity{
		ID:        w.Id,
		Address:   w.WalletAddress,
		CreatedAt: w.CreatedAt.AsTime(),
	}
	if w.LastAuthAt != nil {
		t := w.LastAuthAt.AsTime()
		result.LastAuthAt = &t
	}
	return result
}

// ptrString returns a pointer to the given string
func ptrString(s string) *string {
	return &s
}
