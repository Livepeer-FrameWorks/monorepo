#!/usr/bin/env bash
# Per-package coverage for api_gateway, measured CROSS-PACKAGE (-coverpkg=./...).
#
# Why this exists: `go test ./pkg -cover` reports only the coverage produced by
# tests living in that same package. The gateway's biggest coverage comes from
# en-masse sweeps that live in OTHER packages' test binaries — the GraphQL
# demo/real-path sweeps in `graph/`, the MCP sweep in `internal/mcp/` — which
# drive `internal/resolvers` and `internal/mcp/tools` heavily. Per-package
# `-cover` does not attribute that work, so resolvers/tools look like ~10% when
# they are actually ~50% / ~21%. This script measures the way `make coverage`
# does (cross-package) but breaks the result down PER PACKAGE so the real
# picture is visible.
#
# Usage: bash scripts/coverage-gateway.sh
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root/api_gateway"

profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

echo "Running api_gateway tests with -coverpkg=./... (this exercises every test binary)..."
go test ./... -coverpkg=./... -coverprofile="$profile" -covermode=atomic -count=1 >/dev/null

# Filter generated code, matching the Makefile `coverage` target exactly.
filtered="$(mktemp)"
trap 'rm -f "$profile" "$filtered"' EXIT
grep -v '\.pb\.go:' "$profile" \
	| grep -v '_grpc\.pb\.go:' \
	| grep -v 'graph/generated/' \
	| grep -v 'graph/model/models_gen\.go:' > "$filtered"

echo
echo "Per-package statement coverage (cross-package attribution, generated code excluded):"
echo

# Merge duplicate blocks (the same source block is emitted by every test binary
# under -coverpkg): a block is covered if ANY occurrence has count > 0. Then
# aggregate covered/total statements per package directory.
awk '
NR == 1 && /^mode:/ { next }
{
	key = $1
	n = split($0, f, " ")
	cnt = f[n]; stmts = f[n-1]
	if (!(key in total_stmts)) { total_stmts[key] = stmts; covered[key] = 0 }
	if (cnt + 0 > 0) covered[key] = 1
}
END {
	for (k in total_stmts) {
		file = k; sub(/:.*/, "", file)
		pkg = file; sub(/\/[^\/]*$/, "", pkg)
		tot[pkg] += total_stmts[k]
		if (covered[k]) cov[pkg] += total_stmts[k]
		gtot += total_stmts[k]
		if (covered[k]) gcov += total_stmts[k]
	}
	for (p in tot) {
		pct = tot[p] > 0 ? 100 * cov[p] / tot[p] : 0
		printf "%6.1f%%  %7d/%-7d  %s\n", pct, cov[p], tot[p], p
	}
	printf "------\n"
	gpct = gtot > 0 ? 100 * gcov / gtot : 0
	printf "%6.1f%%  %7d/%-7d  TOTAL\n", gpct, gcov, gtot
}
' "$filtered" | sed -E 's#frameworks/api_gateway/##' | sort -k4
