package triggers

import (
	"errors"

	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
)

// ConfigureLiveViewerPlacementAdmission installs final viewer admission from the
// destination's public runtime, so USER_NEW checks the exact policy, ownership
// fence and router that prepared the viewer. Missing geo lookups leave the
// viewer location unknown; they never substitute a guessed coordinate.
func (p *Processor) ConfigureLiveViewerPlacementAdmission(runtime *federation.LivePublicPlacementRuntime, geo *geoip.Reader, geoCache *cache.Cache) error {
	if p == nil || runtime == nil || runtime.Gate == nil || runtime.Source == nil || p.viewerPlacementAdmission != nil || p.viewerPlacementRequired {
		return errors.New("viewer placement admission dependencies are unavailable or already configured")
	}
	sourceCell, sourceErr := runtime.Source.CellID()
	if runtime.Gate.CellID == "" || runtime.Gate.Authority == nil || runtime.Gate.Router.Observe == nil || sourceErr != nil || sourceCell != runtime.Gate.CellID {
		return errors.New("viewer placement admission requires a cell-bound policy gate and source paths")
	}
	authority, ok := runtime.Gate.Authority.(ViewerPlacementAuthorityReader)
	if !ok || authority == nil {
		return errors.New("viewer placement admission requires tenant-scoped authority lookup")
	}
	adapter := &ViewerPlacementAdapter{Authority: authority, Source: runtime.Source, Gate: runtime.Gate, GeoIP: geo, GeoCache: geoCache}
	p.SetViewerPlacementAdmission(adapter.AdmitViewer)
	return nil
}
