// Package netstat measures rates on default-route interfaces using operating-system counters.
package netstat

import (
	"math"
	"sort"
	"strings"
	"time"
)

type Counters struct {
	Name   string
	Index  uint32
	Tx, Rx uint64
}

type Snapshot struct {
	At         time.Time
	Interfaces []Counters
}

type Reader interface {
	Read() (Snapshot, error)
}

type Rate struct {
	Up, Down               uint64
	Valid                  bool
	WindowStart, WindowEnd time.Time
}

// Sampler is owned by the poller; its caller serializes samples with the poller's metrics lock.
type Sampler struct {
	interval time.Duration
	previous Snapshot
}

func NewSampler(interval time.Duration) *Sampler {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &Sampler{interval: interval}
}

func (s *Sampler) Invalidate() { s.previous = Snapshot{} }

func (s *Sampler) Sample(current Snapshot) Rate {
	current.Interfaces = append([]Counters(nil), current.Interfaces...)
	sort.Slice(current.Interfaces, func(i, j int) bool { return current.Interfaces[i].Name < current.Interfaces[j].Name })
	previous := s.previous
	s.previous = current
	result := Rate{WindowStart: previous.At, WindowEnd: current.At}
	elapsed := current.At.Sub(previous.At)
	if previous.At.IsZero() || elapsed < time.Second || elapsed > 3*s.interval || len(current.Interfaces) == 0 || len(current.Interfaces) != len(previous.Interfaces) {
		return result
	}
	var tx, rx uint64
	for i, counter := range current.Interfaces {
		old := previous.Interfaces[i]
		if counter.Name != old.Name || counter.Index != old.Index || counter.Tx < old.Tx || counter.Rx < old.Rx {
			return result
		}
		dtx, drx := counter.Tx-old.Tx, counter.Rx-old.Rx
		if dtx > math.MaxUint64-tx || drx > math.MaxUint64-rx {
			return result
		}
		tx += dtx
		rx += drx
	}
	rate := func(delta uint64) uint64 {
		value := float64(delta) / elapsed.Seconds()
		if value >= float64(math.MaxUint64) {
			return math.MaxUint64
		}
		return uint64(value)
	}
	result.Up, result.Down, result.Valid = rate(tx), rate(rx), true
	return result
}

func excludedInterface(name string) bool {
	return name == "lo" || name == "lo0" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "veth")
}
