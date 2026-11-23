package prober

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/google/uuid"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type responseError struct {
	RetryAfter time.Duration
	Status     int
	Code       string
}

func (e responseError) Error() string { return fmt.Sprintf("observer returned HTTP %d", e.Status) }

type Prober struct {
	URL, Secret, ID, Region string
	Client                  *http.Client
	Queue                   *telemetryspool.Queue
	Probe                   interface {
		Check(context.Context, obs.Assignment) obs.ProbeResult
	}
	Concurrency          int
	Timeout              time.Duration
	LastWork             atomic.Int64
	LastDiscoverySuccess atomic.Int64
	SuccessfulChecks     atomic.Uint64
	AdmissionRejections  atomic.Uint64
	Assigned             atomic.Uint64
	Completed            atomic.Uint64
	clockSkew            atomic.Int64
	bulkRecords          atomic.Int64
	bulkBytes            atomic.Int64
}

func (p *Prober) request(ctx context.Context, method, path string, raw []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.URL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := p.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 8<<20 {
		return nil, fmt.Errorf("observer response exceeds bound")
	}
	if response.StatusCode != 200 {
		var rejected struct {
			Code string `json:"code"`
		}
		if parseErr := json.Unmarshal(body, &rejected); parseErr != nil {
			rejected.Code = ""
		}
		retryAfter := time.Second
		if seconds, parseErr := strconv.Atoi(response.Header.Get("Retry-After")); parseErr == nil && seconds > 0 {
			retryAfter = time.Duration(min(seconds, 30)) * time.Second
		} else if at, parseErr := http.ParseTime(response.Header.Get("Retry-After")); parseErr == nil {
			retryAfter = max(time.Second, min(time.Until(at), 30*time.Second))
		}
		return nil, responseError{Status: response.StatusCode, Code: rejected.Code, RetryAfter: retryAfter}
	}
	return body, nil
}
func (p *Prober) Health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		count, size, dropped := p.Queue.Stats()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"pending_records": count, "pending_bytes": size, "dropped_records": dropped, "last_work": p.LastWork.Load(), "last_discovery_success": p.LastDiscoverySuccess.Load(), "successful_discovery_checks": p.SuccessfulChecks.Load(), "admission_rejections": p.AdmissionRejections.Load()}); err != nil {
			return
		}
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		seen := p.LastWork.Load()
		if seen == 0 || time.Since(time.Unix(seen, 0)) > 2*time.Minute {
			http.Error(w, "coordinator heartbeat unavailable", http.StatusServiceUnavailable)
			return
		}
		if !p.deliveryHealth().Healthy(time.Now()) {
			http.Error(w, "observation delivery is degraded", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
func (p *Prober) Run(ctx context.Context, report func(error)) error {
	if p.Concurrency < 1 || p.Concurrency > 64 || p.Timeout <= 0 {
		return fmt.Errorf("invalid prober concurrency or timeout")
	}
	if report == nil {
		report = func(error) {}
	}
	go p.Queue.RunBatches(ctx, func() (int, int) { return int(max(1, p.bulkRecords.Load())), int(max(1, p.bulkBytes.Load())) }, p.sendRecords, report)

	commit := func(batch obs.Batch, reservation *telemetryspool.Reservation) bool {
		defer reservation.Cancel()
		batch.ID, batch.ObserverID = uuid.NewString(), p.ID
		raw, err := json.Marshal(batch)
		if err != nil {
			report(err)
			if lossErr := p.Queue.NoteDropped(); lossErr != nil {
				report(lossErr)
			}
			return false
		}
		if int64(len(raw)) > reservation.Capacity() {
			report(errors.New("observation exceeds reserved record size"))
			if lossErr := p.Queue.NoteDropped(); lossErr != nil {
				report(lossErr)
			}
			return false
		}
		for ctx.Err() == nil {
			err = reservation.Commit(batch.ID, raw)
			if err == nil {
				return true
			}
			report(err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false
			case <-timer.C:
			}
		}
		return false
	}
	enqueue := func(batch obs.Batch) bool {
		reservation, err := p.Queue.Reserve(8192)
		if err != nil {
			report(err)
			return false
		}
		return commit(batch, reservation)
	}

	var workers sync.WaitGroup
	defer workers.Wait()
	var dnsRunning atomic.Bool
	nextDNS := time.Time{}
	nextReport := time.Time{}
	controlVersion := 0
	seen := map[string]time.Time{}
	var seenMu sync.Mutex
	rpcSem := make(chan struct{}, p.Concurrency)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		sequence, sequenceErr := p.Queue.NextSequence()
		health := p.deliveryHealth()
		health.Sequence = sequence
		if sequenceErr != nil {
			health.CapacityAvailable = false
		}
		body, encodeErr := json.Marshal(obs.WorkRequest{ObserverID: p.ID, Region: p.Region, Delivery: health})
		if encodeErr != nil {
			cancel()
			return encodeErr
		}
		method, path := http.MethodGet, "/v1/work?observer_id="+url.QueryEscape(p.ID)+"&region="+url.QueryEscape(p.Region)
		if controlVersion > 0 {
			method, path = http.MethodPost, "/v1/work"
		} else {
			body = nil
		}
		sent := time.Now()
		raw, err := p.request(requestCtx, method, path, body)
		var response responseError
		if errors.As(err, &response) && (response.Status == 404 || response.Status == 405) && method == http.MethodPost {
			controlVersion = 0
			raw, err = p.request(requestCtx, http.MethodGet, "/v1/work?observer_id="+url.QueryEscape(p.ID)+"&region="+url.QueryEscape(p.Region), nil)
		}
		cancel()
		if sequenceErr != nil {
			err = sequenceErr
		}

		var work obs.Work
		if err == nil {
			err = json.Unmarshal(raw, &work)
		}
		if err != nil {
			report(err)
		} else {
			now := time.Now().UTC()
			p.LastWork.Store(now.Unix())
			controlVersion = work.ControlVersion
			if work.IngestBatchRecords > 1 && work.IngestBatchBytes > 0 {
				p.bulkBytes.Store(int64(min(work.IngestBatchBytes, 380<<10)))
				p.bulkRecords.Store(int64(min(work.IngestBatchRecords, 64)))
			}
			if !work.ServerTime.IsZero() {
				p.clockSkew.Store(minimumClockOffset(sent, now, work.ServerTime).Microseconds())
			}
			clockTrusted := work.ServerTime.IsZero() || math.Abs(float64(p.clockSkew.Load())/1e6) <= 1
			if !now.Before(nextReport) {
				count, _, dropped := p.Queue.Stats()
				enqueue(obs.Batch{Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: now, CadenceSeconds: 60, QueueRecords: uint32(count), DroppedRecords: dropped, Healthy: health.Healthy(now)}})
				nextReport = now.Add(time.Minute)
			}
			seenMu.Lock()
			for id, deadline := range seen {
				if now.After(deadline.Add(time.Hour)) {
					delete(seen, id)
				}
			}
			seenMu.Unlock()
			// A full DNS scan has its own bounded workers; work polling never waits for it.
			if clockTrusted && !now.Before(nextDNS) && dnsRunning.CompareAndSwap(false, true) {
				seconds := work.DNSIntervalSeconds
				if seconds < 60 {
					seconds = 300
				}
				nextDNS = now.Add(time.Duration(seconds) * time.Second)
				workers.Add(1)
				go func(catalog []obs.Resource) {
					defer workers.Done()
					defer dnsRunning.Store(false)
					resources := make(chan obs.Resource)
					var completed, failed atomic.Uint32
					var scan sync.WaitGroup
					for i := 0; i < p.Concurrency; i++ {
						scan.Add(1)
						go func() {
							defer scan.Done()
							for resource := range resources {
								reservation, reserveErr := p.Queue.Reserve(65536)
								if reserveErr != nil {
									report(reserveErr)
									failed.Add(1)
									continue
								}

								if ctx.Err() != nil {
									reservation.Cancel()
									return
								}
								result := obs.DNSResult{Resource: resource, ObservedAt: time.Now().UTC()}
								uri, e := url.Parse(resource.URI)
								if e != nil || uri.Hostname() == "" {
									result.ErrorKind = "invalid_uri"
								} else {
									checkCtx, stop := context.WithTimeout(ctx, p.Timeout)
									ips, lookupErr := net.DefaultResolver.LookupIPAddr(checkCtx, uri.Hostname())
									stop()
									if lookupErr != nil {
										result.ErrorKind = "dns"
									} else {
										for _, ip := range ips {
											result.IPs = append(result.IPs, ip.IP.String())
										}
									}
								}
								committed := commit(obs.Batch{DNS: []obs.DNSResult{result}}, reservation)
								if committed {
									completed.Add(1)
								}
								if !committed || result.ErrorKind != "" {
									failed.Add(1)
								}
							}
						}()
					}
					for _, resource := range catalog {
						select {
						case resources <- resource:
						case <-ctx.Done():
							close(resources)
							scan.Wait()
							return
						}
					}
					close(resources)
					scan.Wait()
					enqueue(obs.Batch{Report: &obs.ObserverReport{Kind: "dns_cycle", ObservedAt: time.Now().UTC(), Expected: uint32(len(catalog)), Completed: completed.Load(), Failed: failed.Load(), CadenceSeconds: 300, Healthy: ctx.Err() == nil && completed.Load() == uint32(len(catalog))}})
				}(work.Catalog)
			}
			for _, a := range work.Assignments {
				if !clockTrusted || now.After(a.Deadline) {
					continue
				}
				seenMu.Lock()
				_, exists := seen[a.ID]
				if exists {
					seenMu.Unlock()
					continue
				}
				reservation, reserveErr := p.Queue.Reserve(8 << 20)
				if reserveErr != nil {
					seenMu.Unlock()
					report(reserveErr)
					continue
				}
				select {
				case rpcSem <- struct{}{}:
					seen[a.ID] = a.Deadline
				default:
					reservation.Cancel()
					seenMu.Unlock()
					continue
				}
				seenMu.Unlock()
				workers.Add(1)
				go func(a obs.Assignment, reservation *telemetryspool.Reservation) {
					defer reservation.Cancel()
					p.Assigned.Add(1)
					defer workers.Done()
					defer func() { <-rpcSem }()
					if time.Now().After(a.Deadline) {
						return
					}
					result := p.Probe.Check(ctx, a)
					if result.Check.Success {
						p.LastDiscoverySuccess.Store(time.Now().Unix())
						p.SuccessfulChecks.Add(1)
					}
					if result.Check.FailureKind == "authentication" || result.Check.FailureKind == "observer_payment" {
						p.AdmissionRejections.Add(1)
					}
					if commit(obs.Batch{Results: []obs.ProbeResult{result}}, reservation) {
						p.Completed.Add(1)
					}
				}(a, reservation)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}

func (p *Prober) deliveryHealth() *obs.DeliveryHealth {
	count, _, dropped := p.Queue.Stats()
	last, oldest, quarantined, readErr := p.Queue.DeliveryState()
	return &obs.DeliveryHealth{QuarantinedRecords: quarantined, CapacityAvailable: readErr == nil && p.Queue.CanReserve(65536), LastUpload: last, OldestPending: oldest, PendingRecords: count, DroppedRecords: dropped, Assigned: p.Assigned.Load(), Completed: p.Completed.Load(), ClockSkewSeconds: float64(p.clockSkew.Load()) / 1e6}
}

// The server stamps its response before transmission. Only offset outside the
// request interval proves clock skew; time spent in transit is uncertainty.
func minimumClockOffset(sent, received, server time.Time) time.Duration {
	lower, upper := sent.Sub(server), received.Sub(server)
	if lower > 0 {
		return lower
	}
	if upper < 0 {
		return upper
	}
	return 0
}
