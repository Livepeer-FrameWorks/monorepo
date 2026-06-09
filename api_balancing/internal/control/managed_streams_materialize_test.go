package control

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// TestMaterializeManagedStream pins the three-way admission outcome the
// reconciler branches on: a nil row or a failed ResolveStreamContext is
// TRANSIENT (preserve prior state, retry) — never a retract; an explicit
// not-admitted is DENIED (retract if previously applied); an admitted stream is
// OK. Collapsing transient into denied would retract live streams on a blip.
func TestMaterializeManagedStream(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("nil row is transient", func(t *testing.T) {
		_, st := materializeManagedStream(ctx, log, "c1", "n1", nil)
		if st != materializeTransient {
			t.Fatalf("nil row status = %v, want transient", st)
		}
	})

	t.Run("ResolveStreamContext error is transient", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			streamContext: func(_ context.Context, _ *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return nil, errors.New("commodore down")
			},
		})
		_, st := materializeManagedStream(ctx, log, "c1", "n1", &commodorepb.ManagedStreamRow{StreamId: "s1"})
		if st != materializeTransient {
			t.Fatalf("resolve error status = %v, want transient", st)
		}
	})

	t.Run("not admitted is denied", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			streamContext: func(_ context.Context, _ *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return &commodorepb.ResolveStreamContextResponse{Admitted: false, AdmissionReason: "suspended"}, nil
			},
		})
		_, st := materializeManagedStream(ctx, log, "c1", "n1", &commodorepb.ManagedStreamRow{StreamId: "s1"})
		if st != materializeDenied {
			t.Fatalf("not-admitted status = %v, want denied", st)
		}
	})

	t.Run("admitted is ok", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			streamContext: func(_ context.Context, _ *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return &commodorepb.ResolveStreamContextResponse{Admitted: true, InternalName: "live+s1"}, nil
			},
		})
		streamCtx, st := materializeManagedStream(ctx, log, "c1", "n1", &commodorepb.ManagedStreamRow{StreamId: "s1"})
		if st != materializeOK {
			t.Fatalf("admitted status = %v, want ok", st)
		}
		if streamCtx.GetInternalName() != "live+s1" {
			t.Fatalf("admitted ctx internal name = %q, want live+s1", streamCtx.GetInternalName())
		}
	})
}
