package cmd

import (
	"context"
	"fmt"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func recordDiagnosticRead(ctx context.Context, kind, target, service string) error {
	c, cfg, cleanup, err := commodoreGRPCClientFromContext(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	defer c.Close()
	if err := requireOperatorSession(cfg.Auth.JWT); err != nil {
		return err
	}
	ctx, cancel := adminRPCContext(ctx, cfg.Auth.JWT)
	defer cancel()
	if err := c.AdminRecordDiagnosticRead(ctx, &commodorepb.AdminRecordDiagnosticReadRequest{Kind: kind, TargetId: target, Service: service}); err != nil {
		return fmt.Errorf("record diagnostic access: %w", err)
	}
	return nil
}
