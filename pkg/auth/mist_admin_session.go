package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/golang-jwt/jwt/v5"
)

// MistAdminSessionPurpose is the fixed `purpose` claim that identifies a
// JWT minted for the /_mist admin proxy. Carried so the same JWT secret
// cannot be reused as a normal login session if the token leaks.
const MistAdminSessionPurpose = "edge_mist_admin"

// DefaultMistAdminSessionTTL is the lifetime of a freshly-minted session
// token. Short by design — the operator exchanges the token at
// /_mist/_session immediately and the resulting cookie tracks the same
// expiry, so a leaked URL/form value is useless within minutes.
const DefaultMistAdminSessionTTL = 5 * time.Minute

// MistAdminSessionClaims is the JWT claim shape for an operator session
// authorizing access to a specific edge node's Mist admin UI. The node
// binding (NodeID) is the security perimeter: validation rejects the
// token if the caller's expected node ID does not match.
type MistAdminSessionClaims struct {
	Purpose   string `json:"purpose"` // always MistAdminSessionPurpose
	NodeID    string `json:"node_id"`
	ClusterID string `json:"cluster_id"`
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	Role      string `json:"role"`
	jwt.RegisteredClaims
}

var (
	ErrInvalidMistAdminSession      = errors.New("invalid mist admin session token")
	ErrExpiredMistAdminSession      = errors.New("mist admin session token expired")
	ErrWrongMistAdminSessionNode    = errors.New("mist admin session token is for a different node")
	ErrWrongMistAdminSessionPurpose = errors.New("token has wrong purpose for mist admin session")
)

// CanAdminMistNode is the owner check for Mist LSP access, delegated to the
// authz PDP: the caller must be an owner/admin of the tenant that owns the
// infrastructure cluster, or carry the platform operator grant (break-glass).
func CanAdminMistNode(ctx context.Context, ownerTenantID, callerTenantID, callerRole string, callerIsPlatformOperator bool) bool {
	return authz.Default.Can(ctx, authz.Identity{
		TenantID:         callerTenantID,
		Role:             callerRole,
		PlatformOperator: callerIsPlatformOperator,
	}, authz.ActionAdminMistNode, authz.Resource{OwnerTenantID: ownerTenantID}).Allow
}

// GenerateMistAdminSessionJWT mints a session token bound to one edge
// node. ttl=0 falls back to DefaultMistAdminSessionTTL.
func GenerateMistAdminSessionJWT(userID, tenantID, role, nodeID, clusterID string, ttl time.Duration, secret []byte) (string, time.Time, error) {
	if nodeID == "" {
		return "", time.Time{}, fmt.Errorf("node_id is required")
	}
	if len(secret) == 0 {
		return "", time.Time{}, fmt.Errorf("jwt secret is required")
	}
	if ttl <= 0 {
		ttl = DefaultMistAdminSessionTTL
	}
	jti, err := randomJTI()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("jti: %w", err)
	}
	exp := time.Now().Add(ttl)
	claims := &MistAdminSessionClaims{
		Purpose:   MistAdminSessionPurpose,
		NodeID:    nodeID,
		ClusterID: clusterID,
		UserID:    userID,
		TenantID:  tenantID,
		Role:      role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, exp, nil
}

// ValidateMistAdminSessionJWT verifies signature + expiry + purpose, then
// checks the node binding. expectedNodeID MUST be the node the caller is
// authorized to admin — for the Foghorn relay that is the connected
// Helmsman's nodeID. Empty expectedNodeID is rejected; that's a caller
// bug, not a valid match-any case.
func ValidateMistAdminSessionJWT(tokenString string, secret []byte, expectedNodeID string) (*MistAdminSessionClaims, error) {
	if expectedNodeID == "" {
		return nil, ErrWrongMistAdminSessionNode
	}
	parsed, err := jwt.ParseWithClaims(tokenString, &MistAdminSessionClaims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredMistAdminSession
		}
		return nil, ErrInvalidMistAdminSession
	}
	claims, ok := parsed.Claims.(*MistAdminSessionClaims)
	if !ok || !parsed.Valid {
		return nil, ErrInvalidMistAdminSession
	}
	if claims.Purpose != MistAdminSessionPurpose {
		return nil, ErrWrongMistAdminSessionPurpose
	}
	if claims.NodeID != expectedNodeID {
		return nil, ErrWrongMistAdminSessionNode
	}
	return claims, nil
}

func randomJTI() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
