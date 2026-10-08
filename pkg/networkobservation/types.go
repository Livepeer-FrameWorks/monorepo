// Package networkobservation defines provider-neutral inventory and observer facts.
package networkobservation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Known     bool    `json:"known"`
	Source    string  `json:"source"`
	Country   string  `json:"country,omitempty"`
	City      string  `json:"city,omitempty"`
}

func (l Location) Valid() bool {
	return l.Known && !math.IsNaN(l.Latitude) && !math.IsNaN(l.Longitude) && !math.IsInf(l.Latitude, 0) && !math.IsInf(l.Longitude, 0) && math.Abs(l.Latitude) <= 90 && math.Abs(l.Longitude) <= 180
}

type Observer struct {
	Kind     string    `json:"kind,omitempty"`
	ID       string    `json:"id"`
	Region   string    `json:"region"`
	Location Location  `json:"location"`
	LastSeen time.Time `json:"last_seen"`
}
type Resource struct {
	CatalogSource string    `json:"catalog_source,omitempty"`
	Provider      string    `json:"provider"`
	Network       string    `json:"network"`
	Environment   string    `json:"environment"`
	ID            string    `json:"id"`
	URI           string    `json:"uri"`
	Active        bool      `json:"active"`
	CatalogSeen   time.Time `json:"catalog_seen"`
}

func (r Resource) Key() string { return Key(r.Provider, r.Network, r.Environment, r.ID, r.URI) }

type Endpoint struct {
	AssignedObservers []string  `json:"assigned_observers,omitempty"`
	ID                string    `json:"id"`
	Resource          Resource  `json:"resource"`
	IP                string    `json:"ip,omitempty"`
	Authority         string    `json:"authority"`
	Location          Location  `json:"location"`
	FirstSeen         time.Time `json:"first_seen"`
	DNSSeen           time.Time `json:"dns_seen"`
	LastCheck         time.Time `json:"last_check"`
	LastSuccess       time.Time `json:"last_success"`
	NextCheck         time.Time `json:"next_check"`
	FailureStreak     uint32    `json:"failure_streak"`
	CadenceSeconds    int64     `json:"cadence_seconds"`
	Status            string    `json:"status"`
	Referenced        bool      `json:"referenced"`
	Checks            []Check   `json:"checks,omitempty"`
	Price             *Quote    `json:"price,omitempty"`
	Capabilities      []string  `json:"capabilities,omitempty"`
	Hardware          string    `json:"hardware,omitempty"`
}
type Quote struct {
	Numerator   string    `json:"numerator"`
	Denominator string    `json:"denominator"`
	Currency    string    `json:"currency"`
	Unit        string    `json:"unit"`
	Capability  string    `json:"capability,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}
type Check struct {
	ObserverID  string    `json:"observer_id"`
	ObservedAt  time.Time `json:"observed_at"`
	Success     bool      `json:"success"`
	Qualifying  bool      `json:"qualifying"`
	ActualIP    string    `json:"actual_ip,omitempty"`
	LatencyMS   uint32    `json:"latency_ms"`
	FailureKind string    `json:"failure_kind,omitempty"`
}
type Assignment struct {
	Signature  string    `json:"signature,omitempty"`
	Completed  bool      `json:"completed"`
	Settled    bool      `json:"settled"`
	ID         string    `json:"id"`
	Endpoint   Endpoint  `json:"endpoint"`
	ObserverID string    `json:"observer_id"`
	DueAt      time.Time `json:"due_at"`
	Deadline   time.Time `json:"deadline"`
}
type DNSResult struct {
	Resource   Resource  `json:"resource"`
	IPs        []string  `json:"ips"`
	ObservedAt time.Time `json:"observed_at"`
	ErrorKind  string    `json:"error_kind,omitempty"`
}
type ProbeResult struct {
	Assignment     *Assignment `json:"assignment,omitempty"`
	ID             string      `json:"id"`
	AssignmentID   string      `json:"assignment_id"`
	EndpointID     string      `json:"endpoint_id"`
	Check          Check       `json:"check"`
	Quote          *Quote      `json:"quote,omitempty"`
	Capabilities   []string    `json:"capabilities,omitempty"`
	Hardware       string      `json:"hardware,omitempty"`
	AdvertisedURIs []string    `json:"advertised_uris,omitempty"`
}
type Batch struct {
	Report     *ObserverReport `json:"report,omitempty"`
	ID         string          `json:"id"`
	ObserverID string          `json:"observer_id"`
	DNS        []DNSResult     `json:"dns,omitempty"`
	Results    []ProbeResult   `json:"results,omitempty"`
}
type ObserverReport struct {
	Kind           string    `json:"kind"`
	ObservedAt     time.Time `json:"observed_at"`
	Expected       uint32    `json:"expected"`
	Completed      uint32    `json:"completed"`
	Failed         uint32    `json:"failed"`
	CadenceSeconds uint32    `json:"cadence_seconds"`
	QueueRecords   uint32    `json:"queue_records"`
	DroppedRecords uint64    `json:"dropped_records"`
	Healthy        bool      `json:"healthy"`
}
type Work struct {
	Catalog            []Resource   `json:"catalog"`
	Assignments        []Assignment `json:"assignments"`
	DNSIntervalSeconds int64        `json:"dns_interval_seconds"`
}
type CandidateFeed struct {
	Version     int        `json:"version"`
	GeneratedAt time.Time  `json:"generated_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	Provider    string     `json:"provider"`
	Network     string     `json:"network"`
	Environment string     `json:"environment"`
	Candidates  []Endpoint `json:"candidates"`
}

func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func NewEndpoint(r Resource, ip string, now time.Time) (Endpoint, error) {
	u, err := url.Parse(r.URI)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return Endpoint{}, fmt.Errorf("invalid resource URI")
	}
	if ip != "" && net.ParseIP(ip) == nil {
		return Endpoint{}, fmt.Errorf("invalid endpoint IP")
	}
	if ip != "" {
		ip = net.ParseIP(ip).String()
	}
	return Endpoint{ID: Key(r.Key(), u.Scheme, u.Host, ip), Resource: r, IP: ip, Authority: u.Host, FirstSeen: now, DNSSeen: now, Referenced: true, CadenceSeconds: 60, Status: "unknown"}, nil
}
func Backoff(streak uint32) time.Duration {
	if streak == 0 {
		return time.Minute
	}
	if streak >= 4 {
		return 15 * time.Minute
	}
	return time.Minute * time.Duration(uint64(1)<<streak)
}
func (e Endpoint) Retirable(now time.Time) bool {
	if e.Referenced {
		return false
	}
	anchor := e.LastSuccess
	if anchor.IsZero() {
		anchor = e.FirstSeen
	}
	return !anchor.IsZero() && now.Sub(anchor) >= 30*24*time.Hour
}
func (e Endpoint) Hidden(now time.Time) bool {
	a := e.LastSuccess
	if a.IsZero() {
		a = e.FirstSeen
	}
	return !a.IsZero() && now.Sub(a) >= 7*24*time.Hour
}
func DistanceKM(a, b Location) float64 {
	rad := math.Pi / 180
	dlat := (b.Latitude - a.Latitude) * rad
	dlon := (b.Longitude - a.Longitude) * rad
	h := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(a.Latitude*rad)*math.Cos(b.Latitude*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 6371 * 2 * math.Asin(math.Sqrt(math.Min(1, math.Max(0, h))))
}

// Nearest applies relative distance, including best-available distant coverage.
// Unknown geography uses one deterministic healthy fallback rather than a false coordinate.
func Nearest(location Location, observers []Observer, now time.Time, healthAge time.Duration, factor float64) []Observer {
	if factor < 1 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		factor = 2
	}
	healthy := func(o Observer) bool {
		return !o.LastSeen.IsZero() && !o.LastSeen.After(now.Add(time.Minute)) && now.Sub(o.LastSeen) <= healthAge
	}
	var fallback *Observer
	var first, second *Observer
	d1, d2 := math.Inf(1), math.Inf(1)
	for _, o := range observers {
		if !healthy(o) {
			continue
		}
		if fallback == nil || o.ID < fallback.ID {
			copy := o
			fallback = &copy
		}
		if !location.Valid() || !o.Location.Valid() {
			continue
		}
		d := DistanceKM(location, o.Location)
		if d < d1 || (d == d1 && (first == nil || o.ID < first.ID)) {
			second, d2 = first, d1
			copy := o
			first = &copy
			d1 = d
		} else if d < d2 || (d == d2 && (second == nil || o.ID < second.ID)) {
			copy := o
			second = &copy
			d2 = d
		}
	}
	if first == nil {
		if fallback == nil {
			return nil
		}
		return []Observer{*fallback}
	}
	out := []Observer{*first}
	if second != nil && d2 <= factor*d1 {
		out = append(out, *second)
	}
	return out
}
func Availability(now time.Time, cadence time.Duration, assigned []string, checks []Check, healthy map[string]bool) string {
	if cadence < time.Minute {
		cadence = time.Minute
	}
	latest := map[string]Check{}
	for _, c := range checks {
		if !healthy[c.ObserverID] || c.ObservedAt.IsZero() || c.ObservedAt.After(now.Add(time.Minute)) || now.Sub(c.ObservedAt) > 2*cadence {
			continue
		}
		if old, ok := latest[c.ObserverID]; !ok || c.ObservedAt.After(old.ObservedAt) {
			latest[c.ObserverID] = c
		}
	}
	for _, c := range latest {
		if c.Success {
			return "up"
		}
	}
	if len(assigned) == 0 {
		return "unknown"
	}
	for _, id := range assigned {
		c, ok := latest[id]
		if !ok || !healthy[id] || !c.Qualifying || c.Success {
			return "unknown"
		}
	}
	return "unavailable"
}
func NormalizeIPs(ips []string) []string {
	set := map[string]bool{}
	for _, ip := range ips {
		if p := net.ParseIP(strings.TrimSpace(ip)); p != nil {
			set[p.String()] = true
		}
	}
	out := make([]string, 0, len(set))
	for ip := range set {
		out = append(out, ip)
	}
	sort.Strings(out)
	return out
}
