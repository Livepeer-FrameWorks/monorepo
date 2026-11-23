package prober

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/google/uuid"
)

func (p *Prober) sendRecords(ctx context.Context, records [][]byte) []telemetryspool.DeliveryOutcome {
	results := make([]telemetryspool.DeliveryOutcome, len(records))
	var raws [][]byte
	var ids []string
	var indexes []int
	size := 0
	for i, raw := range records {
		var b obs.Batch
		if json.Unmarshal(raw, &b) != nil {
			results[i].QuarantineCode = "LOCAL_RECORD_INVALID"
			continue
		}
		if _, err := uuid.Parse(b.ID); err != nil || b.ObserverID != p.ID {
			results[i].QuarantineCode = "LOCAL_IDENTITY_INVALID"
			continue
		}
		raws = append(raws, raw)
		ids = append(ids, b.ID)
		indexes = append(indexes, i)
		size += len(raw)
	}
	if len(raws) == 0 {
		return results
	}
	if len(raws) == 1 {
		body, err := p.request(ctx, http.MethodPost, "/v1/observations", raws[0])
		if err == nil {
			var receipt struct {
				Accepted bool   `json:"accepted"`
				ID       string `json:"event_id"`
			}
			if json.Unmarshal(body, &receipt) != nil || !receipt.Accepted || receipt.ID != ids[0] {
				err = errors.New("missing matching committed observation receipt")
			}
		}
		var rejected responseError
		if errors.As(err, &rejected) && rejected.Status == 409 && rejected.Code == "EVENT_ID_CONFLICT" {
			results[indexes[0]].QuarantineCode = rejected.Code
		} else {
			results[indexes[0]] = telemetryspool.DeliveryOutcome{Delivered: err == nil, Err: err}
			if err != nil {
				results[indexes[0]].RetryAfter = receiverRetryAfter(err)
			}
		}
		return results
	}
	fail := func(err error) []telemetryspool.DeliveryOutcome {
		for _, i := range indexes {
			results[i].Err = err
			results[i].RetryAfter = receiverRetryAfter(err)
		}
		return results
	}
	if p.bulkRecords.Load() < 2 || size > int(p.bulkBytes.Load()) {
		return fail(errors.New("bulk capability changed; retry original records"))
	}
	body, err := json.Marshal(map[string]any{"records": raws})
	if err != nil {
		return fail(err)
	}
	raw, err := p.request(ctx, http.MethodPost, "/v1/observation-batches", body)
	var rejected responseError
	if errors.As(err, &rejected) && (rejected.Status == 404 || rejected.Status == 405) {
		p.bulkRecords.Store(1)
	}
	if err != nil {
		return fail(err)
	}
	var response struct {
		Results []struct {
			ID       string `json:"id"`
			Accepted bool   `json:"accepted"`
			Code     string `json:"code"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Results) != len(raws) {
		return fail(errors.New("invalid bulk receipt count"))
	}
	for i, result := range response.Results {
		if result.ID != ids[i] {
			return fail(errors.New("bulk receipt identity mismatch"))
		}
	}
	for i, result := range response.Results {
		index := indexes[i]
		switch {
		case result.Accepted && result.Code == "":
			results[index].Delivered = true
		case !result.Accepted && result.Code == "EVENT_ID_CONFLICT":
			results[index].QuarantineCode = result.Code
		default:
			results[index].Err = errors.New("record has no committed receipt")
		}
	}
	return results
}

func receiverRetryAfter(err error) time.Duration {
	var response responseError
	if errors.As(err, &response) && response.RetryAfter > 0 {
		return response.RetryAfter
	}
	return time.Second
}
