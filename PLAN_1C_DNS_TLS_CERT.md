# PLAN 1C: Per-Edge DNS + Wildcard TLS + Cert Distribution

## Progress
- [x] Added cluster-aware DNS reconciliation path with per-edge host records for edge nodes.
- [x] Added wildcard cluster certificate ensure path in Navigator DNS reconciler.
- [x] Added TLS certificate bundle to `ConfigSeed` proto contract.
- [x] Added Foghorn seed composition support to fetch and embed cluster wildcard cert bundles.
- [x] Added Helmsman ConfigSeed TLS bundle application to write cert/key files.
- [x] Updated edge template rendering to default Caddy to file-based TLS cert paths.
- [x] Regenerate protobufs and validate with focused tests.
- [x] Commit and open PR.

## Sign-off
- Completed implementation, validation, commit, and PR draft creation.
