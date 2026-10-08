package prober

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetryspool"
	"github.com/google/uuid"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

type Prober struct {
	URL, Secret, ID, Region string
	Client                  *http.Client
	Queue                   *telemetryspool.Queue
	Probe                   interface {
		Check(context.Context, obs.Assignment) obs.ProbeResult
	}
	Concurrency int
	Timeout     time.Duration
	LastWork    atomic.Int64
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
		return nil, fmt.Errorf("observer returned HTTP %d", response.StatusCode)
	}
	return body, nil
}
func (p *Prober) Health() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		count, size, dropped := p.Queue.Stats()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"pending_records": count, "pending_bytes": size, "dropped_records": dropped, "last_work": p.LastWork.Load()}); err != nil {
			return
		}
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		seen := p.LastWork.Load()
		if seen == 0 || time.Since(time.Unix(seen, 0)) > 2*time.Minute {
			http.Error(w, "coordinator heartbeat unavailable", http.StatusServiceUnavailable)
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
	go p.Queue.Run(ctx, func(ctx context.Context, raw []byte) error {
		_, err := p.request(ctx, http.MethodPost, "/v1/observations", raw)
		return err
	}, report)
	enqueue := func(batch obs.Batch) bool {
		batch.ID, batch.ObserverID = uuid.NewString(), p.ID
		raw, err := json.Marshal(batch)
		if err == nil {
			err = p.Queue.Enqueue(batch.ID, raw)
		}
		if err != nil {
			report(err)
			return false
		}
		return true
	}
	var workers sync.WaitGroup
	defer workers.Wait()
	var dnsRunning atomic.Bool
	nextDNS := time.Time{}
	nextReport := time.Time{}
	seen := map[string]time.Time{}
	var seenMu sync.Mutex
	rpcSem := make(chan struct{}, p.Concurrency)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		raw, err := p.request(requestCtx, http.MethodGet, "/v1/work?observer_id="+url.QueryEscape(p.ID)+"&region="+url.QueryEscape(p.Region), nil)
		cancel()
		var work obs.Work
		if err == nil {
			err = json.Unmarshal(raw, &work)
		}
		if err != nil {
			report(err)
		} else {
			now := time.Now().UTC()
			p.LastWork.Store(now.Unix())
			if !now.Before(nextReport) {
				count, _, dropped := p.Queue.Stats()
				enqueue(obs.Batch{Report: &obs.ObserverReport{Kind: "heartbeat", ObservedAt: now, CadenceSeconds: 60, QueueRecords: uint32(count), DroppedRecords: dropped, Healthy: true}})
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
			if !now.Before(nextDNS) && dnsRunning.CompareAndSwap(false, true) {
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
								if ctx.Err() != nil {
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
								enqueue(obs.Batch{DNS: []obs.DNSResult{result}})
								completed.Add(1)
								if result.ErrorKind != "" {
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
					enqueue(obs.Batch{Report: &obs.ObserverReport{Kind: "dns_cycle", ObservedAt: time.Now().UTC(), Expected: uint32(len(catalog)), Completed: completed.Load(), Failed: failed.Load(), CadenceSeconds: 300, Healthy: ctx.Err() == nil}})
				}(work.Catalog)
			}
			for _, a := range work.Assignments {
				if now.After(a.Deadline) {
					continue
				}
				seenMu.Lock()
				_, exists := seen[a.ID]
				if exists {
					seenMu.Unlock()
					continue
				}
				select {
				case rpcSem <- struct{}{}:
					seen[a.ID] = a.Deadline
				default:
					seenMu.Unlock()
					continue
				}
				seenMu.Unlock()
				workers.Add(1)
				go func(a obs.Assignment) {
					defer workers.Done()
					defer func() { <-rpcSem }()
					if time.Now().After(a.Deadline) {
						return
					}
					result := p.Probe.Check(ctx, a)
					if !enqueue(obs.Batch{Results: []obs.ProbeResult{result}}) {
						seenMu.Lock()
						delete(seen, a.ID)
						seenMu.Unlock()
					}
				}(a)
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
