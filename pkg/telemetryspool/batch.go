package telemetryspool

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// RunBatches preserves each record's identity and acknowledges only its own committed result.
func (q *Queue) RunBatches(ctx context.Context, limits func() (int, int), send func(context.Context, [][]byte) []DeliveryOutcome, onError func(error)) {
	for ctx.Err() == nil {
		rows, err := q.Pending(100)
		if err != nil {
			if onError != nil {
				onError(err)
			}
		} else {
			for i := 0; i < len(rows) && ctx.Err() == nil; {
				maxRecords, maxBytes := limits()
				maxRecords = max(1, min(64, maxRecords))
				end := i
				bytes := 0
				for end < len(rows) && end-i < maxRecords {
					size := len(rows[end].Payload)
					if end > i && bytes+size > maxBytes {
						break
					}
					bytes += size
					end++
				}
				payloads := make([][]byte, end-i)
				for j := range payloads {
					payloads[j] = rows[i+j].Payload
				}
				results := send(ctx, payloads)
				if len(results) != len(payloads) {
					results = make([]DeliveryOutcome, len(payloads))
					for j := range results {
						results[j].Err = errors.New("delivery returned no matching per-record receipts")
					}
				}
				var pause time.Duration
				for j, result := range results {
					q.finish(rows[i+j], result, onError)
					pause = max(pause, result.RetryAfter)
				}
				if pause > 0 {
					timer := time.NewTimer(min(pause, 30*time.Second))
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				i = end
			}
		}
		if len(rows) == 100 {
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-q.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (q *Queue) finish(row Record, result DeliveryOutcome, onError func(error)) {
	var err error
	switch {
	case result.QuarantineCode != "":
		err = q.quarantine(row, result.QuarantineCode)
	case result.Delivered:
		err = q.Ack(row)
	default:
		err = result.Err
		if err == nil {
			err = errors.New("delivery was not acknowledged")
		}
	}
	if err != nil {
		if retryErr := q.deferRecord(row); retryErr != nil {
			err = errors.Join(err, retryErr)
		}
		if onError != nil {
			onError(fmt.Errorf("telemetry delivery: %w", err))
		}
	}
}
