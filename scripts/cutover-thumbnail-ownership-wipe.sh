#!/bin/sh
#
# One-time thumbnail-ownership cutover: enumerate the OWNER-LESS legacy live streams and wipe ONLY their
# live-thumbnail prefixes, fail-closed, then verify every enumerated prefix is gone in BOTH cells before any
# service or traffic starts. This is the destructive core of the legacy ownership cutover documented in
# docs/architecture/thumbnails.md; the operator runs it AFTER stopping old minters and provisioning infrastructure
# (so the v0.2.97 EXPAND migrations have created thumbnail_serving_cluster_ids), and provisions applications AFTER it.
#
# It NEVER touches the whole thumbnails/ namespace: artifact thumbnails (thumbnails/{artifact_hash}/) route via
# origin_cluster_id and are preserved. It only removes thumbnails/{stream_id}/ for streams whose serving-cell set is
# empty ('{}').
#
# Fail-closed on its dangerous inputs:
#   - DRAIN is ENFORCED, not asserted: STOP_EPOCH is the unix time the last minter was stopped, and the script refuses
#     until now - STOP_EPOCH >= DeterministicCopyWindow (1200s = 15m presigned-PUT TTL + 5m provider-ambiguity tail).
#   - TARGETS are DERIVED from the committed GitOps cluster manifest via the CLI: the script asks
#     `frameworks cluster storage descriptor <cluster>` for a JSON descriptor, so parsing goes through
#     inventory.ParseManifest (strict, structural — quoted values, inline comments, anchors handled; NO host inventory /
#     SOPS), NOT a text/indentation parser. This is the pre-bootstrap DESIRED descriptor, complete and available regardless of
#     whether Quartermaster has bootstrapped (its s3_prefix is written only during apps provision, so it is NOT a valid
#     source at wipe time). bucket/prefix are never operator-typed, and the operator-supplied mc alias must resolve to
#     the endpoint the manifest records, compared after trimming a trailing slash (stricter than a host-only check,
#     though not byte-identical to runtime backend-identity). So a mistyped
#     prefix cannot delete/verify the wrong empty path, and an alias pointed at the wrong endpoint is refused.
#
# Required environment:
#   STOP_EPOCH                       unix seconds the last live-thumbnail minter was stopped
#   COMMODORE_DSN                    psql connection string for the Commodore database (stream enumeration)
#   GITOPS_CLUSTER_FILE              path to the committed GitOps cluster manifest (clusters/<env>/cluster.yaml)
#   EU_MC_ALIAS, EU_CLUSTER_KEY      EU cell: local mc alias + the cluster key in the manifest (e.g. media-eu-1)
#   US_MC_ALIAS, US_CLUSTER_KEY      US cell: local mc alias + cluster key
#   FRAMEWORKS_BIN                   optional: the frameworks CLI to invoke (default: frameworks on PATH)
#
# Any drain, input, descriptor, enumeration, deletion, or verification failure aborts the whole run non-zero.

set -eu

DETERMINISTIC_COPY_WINDOW=1200 # 20m = thumbnailUploadTTL(15m) + projectionProviderAmbiguityWindow(5m)

fail() {
	echo "cutover: FAIL: $*" >&2
	exit 1
}

# --- drain enforcement (a bare "yes" flag would not prove the window elapsed) ---
[ -n "${STOP_EPOCH:-}" ] || fail "STOP_EPOCH (unix seconds when the last minter was stopped) is required"
case "$STOP_EPOCH" in
'' | *[!0-9]*) fail "STOP_EPOCH must be unix seconds (a non-negative integer)" ;;
esac
now="$(date +%s)"
elapsed=$((now - STOP_EPOCH))
[ "$elapsed" -ge 0 ] || fail "STOP_EPOCH is in the future (${elapsed}s); check the clock"
[ "$elapsed" -ge "$DETERMINISTIC_COPY_WINDOW" ] ||
	fail "only ${elapsed}s since STOP_EPOCH; must wait >= ${DETERMINISTIC_COPY_WINDOW}s (DeterministicCopyWindow) so a late PUT cannot land after the wipe"

# --- required inputs ---
for v in COMMODORE_DSN GITOPS_CLUSTER_FILE EU_MC_ALIAS EU_CLUSTER_KEY US_MC_ALIAS US_CLUSTER_KEY; do
	eval "val=\${$v:-}"
	[ -n "$val" ] || fail "$v is required"
done
[ -f "$GITOPS_CLUSTER_FILE" ] || fail "GITOPS_CLUSTER_FILE '$GITOPS_CLUSTER_FILE' is not a file"

FW_BIN="${FRAMEWORKS_BIN:-frameworks}"
command -v psql >/dev/null 2>&1 || fail "psql not found on PATH"
command -v mc >/dev/null 2>&1 || fail "mc not found on PATH"
command -v jq >/dev/null 2>&1 || fail "jq not found on PATH (needed to parse the descriptor JSON safely)"
command -v "$FW_BIN" >/dev/null 2>&1 || fail "frameworks CLI '$FW_BIN' not found on PATH (set FRAMEWORKS_BIN)"

# --- derive each cell's wipe base from the VALIDATED CLI descriptor + verify the alias endpoint ---
# Sets <CELL>_BASE. `cluster storage descriptor` strict-parses the manifest (inventory.ParseManifest, no host inventory)
# and prints a JSON descriptor; jq extracts each field, control chars are refused, and printable specials are handled
# losslessly, so a crafted prefix can neither shift a field nor wipe a wrong path. A nonzero CLI exit (missing cluster /
# no backend / incomplete descriptor) aborts the cutover.
derive_base() {
	cell="$1"
	alias="$2"
	cluster_key="$3"
	desc="$("$FW_BIN" cluster storage descriptor "$cluster_key" --manifest "$GITOPS_CLUSTER_FILE")" ||
		fail "$cell: 'frameworks cluster storage descriptor $cluster_key' failed (wrong key, or no S3 backend for it)"
	# Validate the descriptor once (defense in depth; the CLI validates too): bucket/prefix/endpoint must all be JSON
	# strings with NO control characters, and bucket+endpoint non-empty. A null/non-string prefix is REFUSED (never
	# coerced to the literal "null"); a control character (newline, etc.) is REFUSED rather than addressed on a
	# destructive path. Printable specials (e.g. '|') are allowed and handled losslessly below.
	printf '%s' "$desc" | jq -e '
		(.bucket|type=="string") and ((.bucket|length)>0) and ((.bucket|test("[[:cntrl:]]"))|not)
		and (.prefix|type=="string") and ((.prefix|test("[[:cntrl:]]"))|not)
		and (.endpoint|type=="string") and ((.endpoint|length)>0) and ((.endpoint|test("[[:cntrl:]]"))|not)' >/dev/null 2>&1 ||
		fail "$cell: descriptor JSON invalid — bucket/prefix/endpoint must be control-char-free strings (bucket+endpoint non-empty); got: $desc"
	# Build the FULL wipe base INSIDE jq (-j: no added newline). Command substitution strips only a trailing newline, so
	# putting the '/thumbnails' suffix after the prefix keeps any newline/delimiter bytes in the prefix INTERNAL and
	# lossless rather than trailing. bucket/endpoint are plain identifiers/URLs, extracted the same lossless way.
	base="$(printf '%s' "$desc" | jq -rj --arg a "$alias" '$a + "/" + .bucket + (if .prefix=="" then "" else "/" + .prefix end) + "/thumbnails"')"
	bucket="$(printf '%s' "$desc" | jq -rj '.bucket')"
	endpoint="$(printf '%s' "$desc" | jq -rj '.endpoint')"

	# Verify the operator's mc alias resolves to the endpoint the manifest records: string equality after trimming a
	# trailing slash (mc may store one). Stricter than a host-only match; catches an alias pointed at the wrong cell.
	alias_url="$(mc alias ls "$alias" --json 2>/dev/null | jq -rj '.URL // .url // empty')"
	[ -n "$alias_url" ] || fail "$cell: cannot read endpoint URL for mc alias '$alias' (is it configured?)"
	if [ "${alias_url%/}" != "${endpoint%/}" ]; then
		fail "$cell: mc alias '$alias' endpoint '${alias_url%/}' != GitOps s3_endpoint '${endpoint%/}'; refusing (alias points at the wrong endpoint)"
	fi

	# bucket must be reachable before any delete.
	mc ls "$alias/$bucket/" >/dev/null 2>&1 || fail "$cell: bucket $alias/$bucket is not reachable; refusing"
	eval "${cell}_BASE=\$base"
	echo "cutover: $cell target derived from GitOps: $base"
}

derive_base EU "$EU_MC_ALIAS" "$EU_CLUSTER_KEY"
derive_base US "$US_MC_ALIAS" "$US_CLUSTER_KEY"

# --- enumerate (fail-closed: a query error aborts, never reads as an empty set) ---
streams_file="$(mktemp)"
trap 'rm -f "$streams_file"' EXIT
psql "$COMMODORE_DSN" -v ON_ERROR_STOP=1 -tAc \
	"SELECT id FROM commodore.streams WHERE thumbnail_serving_cluster_ids = '{}'" >"$streams_file" ||
	fail "enumeration query failed (has the v0.2.97 EXPAND migration run?)"
count="$(grep -c . "$streams_file" || true)"
echo "cutover: drain ${elapsed}s OK; enumerated $count owner-less legacy live stream(s)"

# --- wipe on BOTH cells; any rm error aborts the whole cutover ---
while IFS= read -r sid; do
	[ -n "$sid" ] || continue
	mc rm --recursive --force "$EU_BASE/$sid/" || fail "delete failed for $EU_BASE/$sid/"
	mc rm --recursive --force "$US_BASE/$sid/" || fail "delete failed for $US_BASE/$sid/"
done <"$streams_file"

# --- verify EVERY enumerated prefix is empty in BOTH stores (a list error is a failure, not "empty") ---
while IFS= read -r sid; do
	[ -n "$sid" ] || continue
	for target in "$EU_BASE/$sid/" "$US_BASE/$sid/"; do
		out="$(mc ls "$target")" || fail "cannot list $target"
		[ -z "$out" ] || fail "$target still has objects after wipe"
	done
done <"$streams_file"

echo "cutover: OK — $count prefix(es) wiped and verified empty in both cells; safe to provision applications"
