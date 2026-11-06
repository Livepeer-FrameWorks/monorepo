package cmd

import (
	"context"
	"errors"
	"fmt"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errDiagnosticAuditUnsupported = errors.New("commodore does not support diagnostic access records")

func diagnosticAccessError(kind string, err error) error {
	if kind == "dvr" && status.Code(err) == codes.Unimplemented {
		return errDiagnosticAuditUnsupported
	}
	return fmt.Errorf("record diagnostic access: %w", err)
}

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
		return diagnosticAccessError(kind, err)
	}
	return nil
}
