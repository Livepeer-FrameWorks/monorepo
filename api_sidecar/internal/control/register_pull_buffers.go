package control

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func pullBuffersFromRegistrationSnapshot(response map[string]any) ([]*ipcpb.LiveSourceBuffer, error) {
	raw, present := response["active_streams"]
	if !present {
		return nil, errors.New("active_streams inventory missing")
	}
	if raw == nil {
		return nil, nil
	}
	streams, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("active_streams inventory malformed")
	}
	var buffers []*ipcpb.LiveSourceBuffer
	for name, raw := range streams {
		if !strings.HasPrefix(name, "pull+") {
			continue
		}
		data, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("pull buffer inventory malformed")
		}
		pid, ok := data["pid"].(float64)
		if !ok || math.IsNaN(pid) || math.IsInf(pid, 0) || pid <= 0 || pid > 1<<53-1 || math.Trunc(pid) != pid {
			return nil, errors.New("pull buffer inventory has no exact positive PID")
		}
		buffers = append(buffers, &ipcpb.LiveSourceBuffer{RuntimeName: name, BufferPid: int64(pid)})
	}
	sort.Slice(buffers, func(i, j int) bool { return buffers[i].RuntimeName < buffers[j].RuntimeName })
	return buffers, nil
}

func liveSourceBuffersForRegister(logger logging.Logger, cfg *sidecarcfg.HelmsmanConfig) ([]*ipcpb.LiveSourceBuffer, bool, int64) {
	client := mist.NewClient(logger, appconfig.MistClient())
	if cfg.MistServerURL != "" {
		client.BaseURL = cfg.MistServerURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sampled := time.Now().UnixMilli()
	response, err := client.GetActiveStreamsContext(ctx)
	if err != nil {
		logger.WithError(err).Warn("Cannot read direct-source buffer inventory for registration")
		return nil, false, 0
	}
	buffers, err := pullBuffersFromRegistrationSnapshot(response)
	if err != nil {
		logger.WithError(err).Warn("Cannot trust direct-source buffer inventory for registration")
		return nil, false, 0
	}
	return buffers, true, sampled
}
