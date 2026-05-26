package resolvers

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

type publicTopologyContext struct {
	context.Context
}

func (c publicTopologyContext) Value(key any) any {
	switch key {
	case ctxkeys.KeyUserID,
		ctxkeys.KeyTenantID,
		ctxkeys.KeyEmail,
		ctxkeys.KeyRole,
		ctxkeys.KeyJWTToken,
		ctxkeys.KeyAPIToken,
		ctxkeys.KeyAPITokenHash,
		ctxkeys.KeyAuthType,
		ctxkeys.KeySessionToken,
		ctxkeys.KeyWalletAddr,
		ctxkeys.KeyPermissions:
		return nil
	default:
		return c.Context.Value(key)
	}
}

func publicTopologyReadContext(ctx context.Context) context.Context {
	return publicTopologyContext{Context: ctx}
}
