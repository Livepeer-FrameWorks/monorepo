package orchestrator

// DefaultStrategyFor returns the baked-in UpdateStrategy for a service
// identifier (the manifest key — "foghorn", "kafka", "redis", "bridge",
// etc.). Operators get safe defaults without having to write
// `update_strategy:` for every service.
//
// Tiers:
//
//   - Raft/KRaft quorum tiers (yugabyte, kafka-controller):
//     max_unavailable=1, no parallelism, no region stagger (a single quorum
//     spans the cluster). Loses majority if two nodes roll together.
//
//   - Kafka brokers: max_unavailable=1. Brokers carry partition leaders;
//     parallel restarts churn leader elections and risk under-replicated
//     partitions.
//
//   - Redis (primary+replicas): primary_last + max_unavailable=1. Replicas
//     roll first, sentinel failover, then the old primary.
//
//   - Stateless multi-host paired regional (foghorn, livepeer-gateway,
//     chandler, bridge, signalman, decklog, periscope-ingest):
//     max_unavailable=ceil(N/3), region_stagger=true. Always keep one
//     region fully healthy.
//
//   - Interface / per-host (nginx, caddy, vmagent, vmauth, chartroom,
//     foredeck, logbook): max_unavailable=1, no stagger. Plenty of
//     replicas, but reload-preferred means low-blast even at one host.
//
//   - Singletons (commodore, quartermaster, purser, navigator, skipper,
//     periscope-query, deckhand, helmsman, livepeer-signer, mirrormaker
//     instances, edges): max_unavailable=1. Nothing to parallelize.
//
//   - Unknown services: max_unavailable=1, no stagger. Safe default —
//     better to take the slow path than the unsafe one.
func DefaultStrategyFor(serviceID string) UpdateStrategy {
	if strat, ok := serviceStrategyDefaults[serviceID]; ok {
		return strat
	}
	// Unknown service: maximally cautious.
	return UpdateStrategy{MaxUnavailable: 1}
}

// serviceStrategyDefaults is the per-service registry. New services land
// here when added to the manifest schema; missing entries fall through to
// DefaultStrategyFor's safe default.
//
// MaxUnavailable values for the stateless paired-region tier are tuned
// for the common 3-per-region layout: ceil(3/3) = 1 keeps two healthy
// hosts in each region during a roll.
var serviceStrategyDefaults = map[string]UpdateStrategy{
	// Raft/KRaft quorum tiers.
	"yugabyte":         {MaxUnavailable: 1},
	"kafka":            {MaxUnavailable: 1}, // brokers
	"kafka-controller": {MaxUnavailable: 1},

	// Kafka MirrorMaker — 3 hosts but checkpoint state is per-task; one at
	// a time keeps mirror lag bounded.
	"kafka-mirrormaker": {MaxUnavailable: 1},

	// Redis: replicas roll first, then primary.
	"redis": {MaxUnavailable: 1, PrimaryLast: true},

	// ClickHouse — singleton today; Replicated cluster nodes roll one at a time.
	"clickhouse": {MaxUnavailable: 1},

	// Stateless multi-host paired regional. region_stagger keeps EU and
	// US distinct waves; canary=1 soaks one host before the rest.
	"foghorn":          {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"livepeer-gateway": {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"chandler":         {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"bridge":           {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"signalman":        {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"decklog":          {MaxUnavailable: 1, Canary: 1, RegionStagger: true},
	"periscope-ingest": {MaxUnavailable: 1, Canary: 1, RegionStagger: true},

	// Interface / per-host. Per-host waves keep reloads and restarts easy to
	// reason about even when N is large.
	"nginx":     {MaxUnavailable: 1},
	"caddy":     {MaxUnavailable: 1},
	"vmagent":   {MaxUnavailable: 1},
	"vmauth":    {MaxUnavailable: 1},
	"chartroom": {MaxUnavailable: 1},
	"foredeck":  {MaxUnavailable: 1},
	"logbook":   {MaxUnavailable: 1},

	// Singletons.
	"commodore":       {MaxUnavailable: 1},
	"quartermaster":   {MaxUnavailable: 1},
	"purser":          {MaxUnavailable: 1},
	"navigator":       {MaxUnavailable: 1},
	"skipper":         {MaxUnavailable: 1},
	"periscope-query": {MaxUnavailable: 1},
	"deckhand":        {MaxUnavailable: 1},
	"helmsman":        {MaxUnavailable: 1},
	"livepeer-signer": {MaxUnavailable: 1},
	"steward":         {MaxUnavailable: 1},
	"grafana":         {MaxUnavailable: 1},
	"metabase":        {MaxUnavailable: 1},
	"prometheus":      {MaxUnavailable: 1},
	"victoriametrics": {MaxUnavailable: 1},
	"listmonk":        {MaxUnavailable: 1},
	"chatwoot":        {MaxUnavailable: 1},

	// Mesh.
	"privateer": {MaxUnavailable: 1},

	// Edges (stateful singletons per cluster).
	"mistserver": {MaxUnavailable: 1},
}
