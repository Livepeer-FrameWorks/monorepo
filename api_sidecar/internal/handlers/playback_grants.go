package handlers

import (
	"context"

	"frameworks/api_sidecar/internal/appconfig"
	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/playbackgrant"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// playbackGrants holds Foghorn's playback grants and the viewer sessions it
// admitted on this edge. PLAY_REWRITE for an admitted session is answered
// from it; see internal/playbackgrant.
var playbackGrants *playbackgrant.Store

func initPlaybackGrants(log logging.Logger) {
	store := playbackgrant.NewStore(playbackgrant.Options{
		Logger: log,
		Mist:   mist.NewClient(log, appconfig.MistClient()),
		Fetch:  control.RequestPlaybackGrant,
	})
	installPlaybackGrants(store)
	go store.Run(context.Background())
}

func installPlaybackGrants(store *playbackgrant.Store) {
	playbackGrants = store
	control.SetPlaybackGrantSessions(store)
}
