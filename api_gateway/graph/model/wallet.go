package model

import (
	"time"

	pb "frameworks/pkg/proto"
)

// WalletIdentity represents a linked cryptocurrency wallet.
// This is a GraphQL model type, not bound to proto because it's used in unions.
type WalletIdentity struct {
	ID         string     `json:"id"`
	Address    string     `json:"address"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastAuthAt *time.Time `json:"lastAuthAt"`
}

// IsLinkWalletResult implements the LinkWalletResult union interface.
func (WalletIdentity) IsLinkWalletResult() {}

// WalletLoginPayload represents a successful wallet login response.
type WalletLoginPayload struct {
	Token        string    `json:"token"`
	User         *pb.User  `json:"user"`
	ExpiresAt    time.Time `json:"expiresAt"`
	IsNewAccount bool      `json:"isNewAccount"`
}

// IsWalletLoginResult implements the WalletLoginResult union interface.
func (WalletLoginPayload) IsWalletLoginResult() {}
