#!/usr/bin/env bash
# stack-scenario: default
# staging: unsafe, changes the QA tenant's own placement policy and clears it again
# tenant: qa
# A tenant-scope placement change rolls out to streams the cells already hold
# without refusing a resolution. Commodore delivers the tenant authority first
# and each stream's recompiled authority after it, so a cell holds the new
# tenant revision before every stream that names it; the cell must keep
# resolving those streams under the revision they were compiled for. From the
# moment the change is applied until it reports EFFECTIVE, and once more after,
# every viewer resolve of a live stream through every Foghorn replica of both
# cells and every ingest resolve of the tenant's idle streams through the
# cell-A replicas succeeds. EFFECTIVE is reported only once every stream
# authority recompiled for the change is acknowledged by every cell it
# targets. The change is applied (SET) and reverted (CLEAR), and both roll out
# this way.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl psql || finish
run="rollout52-$(date +%s)-$RANDOM"
idle=${STACK_ROLLOUT_IDLE_STREAMS:-8}
ids=() ids_uuid=() keys=() set_applied=0 publisher="" pb=""
cleanup() {
  [ "${cleaned:-0}" = 0 ] || return 0
  cleaned=1
  [ -z "$publisher" ] || kill "$publisher" 2>/dev/null
  # A policy this run set is cleared again so later scenarios see the tenant's own default.
  if [ "$set_applied" = 1 ]; then apply_ingest CLEAR >/dev/null || echo "cleanup: clearing the tenant INGEST rules failed"; fi
  for id in "${ids[@]}"; do delete_stream "$id"; done
  return 0
}
on_exit() { local rc=$?; cleanup || rc=1; exit "$rc"; }
trap on_exit EXIT

policy_query='query($s:MediaPlacementScopeInput!){mediaPlacementPolicy(scope:$s){__typename ... on MediaPlacementPolicyState{revision parentRevision activeRevision verbs{verb ownRules{schemaVersion}} rollout{status requiredRecipients appliedRecipients}} ... on MediaPlacementError{message}}}'
tenant_scope='{"s":{"kind":"TENANT"}}'
policy_state() { gql "$policy_query" "$tenant_scope"; }

# apply_ingest SET|CLEAR: reviews and applies the change through the user-facing
# contract; prints the applied revision.
apply_ingest() {
  local state revision parent update input review token warnings applied
  state=$(policy_state)
  revision=$(echo "$state" | jq -r '.data.mediaPlacementPolicy.revision // empty')
  parent=$(echo "$state" | jq -r '.data.mediaPlacementPolicy.parentRevision // empty')
  [ -n "$revision" ] && [ -n "$parent" ] || { echo "$state" >&2; return 1; }
  if [ "$1" = SET ]; then
    update='[{"verb":"INGEST","kind":"SET","rules":{"schemaVersion":1,"constraints":{"deny":[{"regions":["stack-rollout-nowhere"]}]}}}]'
  else
    update='[{"verb":"INGEST","kind":"CLEAR"}]'
  fi
  input=$(jq -cn --arg r "$revision" --arg p "$parent" --argjson u "$update" \
    '{scope:{kind:"TENANT"},expectedRevision:$r,expectedParentRevision:$p,updates:$u}')
  review=$(gql 'query($i:ReviewMediaPlacementChangeInput!){reviewMediaPlacementChange(input:$i){__typename ... on MediaPlacementReview{reviewToken warnings{id acknowledgementRequired}} ... on MediaPlacementError{message code}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  token=$(echo "$review" | jq -r '.data.reviewMediaPlacementChange.reviewToken // empty')
  [ -n "$token" ] || { echo "$review" >&2; return 1; }
  warnings=$(echo "$review" | jq -c '[.data.reviewMediaPlacementChange.warnings[]?|select(.acknowledgementRequired)|.id]')
  input=$(echo "$input" | jq -c --arg t "$token" --arg k "$run-$1" --argjson w "$warnings" '.+{reviewToken:$t,idempotencyKey:$k,acknowledgedWarningIds:$w}')
  applied=$(gql 'mutation($i:ApplyMediaPlacementChangeInput!){applyMediaPlacementChange(input:$i){__typename ... on MediaPlacementChange{revision} ... on MediaPlacementError{message}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  json_has "$applied" '.data.applyMediaPlacementChange.__typename == "MediaPlacementChange"' || { echo "$applied" >&2; return 1; }
  echo "$applied" | jq -r '.data.applyMediaPlacementChange.revision'
}

ingest_status() { curl -s -m 10 -o /dev/null -w '%{http_code}' "$1/ingest/$2?protocol=rtmp"; }

# The ingest resolver allows one request per second per client and replica
# (burst 10), so idle streams are resolved one per replica at a time, at most
# once a second; viewer resolves have no such limit.
next_idle=0 last_ingest=0 round_file="$STACK_STATE_DIR/$run-round"
# probe_round prints "<what> <base> <code>" for every resolve that was refused.
# It keeps its pacing in this shell, so callers redirect it instead of
# capturing it in a subshell.
probe_round() {
  local base code now
  for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
    (code=$(play_status "$base" "$pb"); [ "$code" = 307 ] || [ "$code" = 302 ] || echo "viewer $base $code") &
  done
  wait
  now=$(monotonic_ms)
  if [ "$idle" -gt 0 ] && [ $((now - last_ingest)) -ge 1100 ]; then
    last_ingest=$now
    for base in $FOGHORN_A_URLS; do
      code=$(ingest_status "$base" "${keys[$next_idle]}")
      [ "$code" = 200 ] || echo "ingest $base $code ${keys[$next_idle]}"
    done
    next_idle=$(((next_idle + 1) % idle))
  fi
}

# roll_out <SET|CLEAR>: applies the change and resolves until the change is
# effective, then once more; fails on any refusal on the way.
roll_out() {
  local what=$1 revision deadline rounds=0 refusals="" round status
  revision=$(apply_ingest "$what") || { fail "the tenant applies an INGEST $what"; return 1; }
  pass "the tenant applies an INGEST $what at revision $revision"
  set_applied=0
  [ "$what" = SET ] && set_applied=1
  deadline=$(($(date +%s) + 420))
  while :; do
    probe_round >"$round_file"; round=$(cat "$round_file")
    rounds=$((rounds + 1))
    [ -z "$round" ] || refusals+="$round"$'\n'
    status=$(policy_state | jq -r '.data.mediaPlacementPolicy | select(.revision == "'"$revision"'") | .rollout.status')
    [ "$status" = EFFECTIVE ] && break
    [ "$(date +%s)" -lt "$deadline" ] || { fail "INGEST $what revision $revision became effective (last status ${status:-unknown})"; return 1; }
  done
  pass "INGEST $what revision $revision became effective after $rounds resolve rounds"
  # Effective means every stream authority recompiled for the change reached
  # every cell: each stream's current version was published after the change
  # and none of its deliveries is outstanding.
  check "INGEST $what: every stream authority was recompiled for revision $revision and acknowledged by every cell when it became effective" \
    stream_authorities_converged "$revision"
  for _ in 1 2 3; do
    probe_round >"$round_file"; round=$(cat "$round_file")
    [ -z "$round" ] || refusals+="$round"$'\n'
  done
  if [ -n "$refusals" ]; then
    printf '  refused resolves during the rollout:\n%s' "$refusals" | head -20
    fail "every resolve across the INGEST $what rollout succeeded ($(printf '%s' "$refusals" | grep -c .) refused)"
  else
    pass "every resolve across the INGEST $what rollout succeeded"
  fi
}

stream_authorities_converged() {
  local list result
  list=$(printf "'live_stream:%s'," "${ids_uuid[@]}")
  result=$(pg commodore "
    SELECT count(*) FILTER (WHERE v.created_at < ch.created_at),
           count(*) FILTER (WHERE EXISTS (
             SELECT 1 FROM commodore.media_authority_deliveries d
             WHERE d.authority_kind = c.authority_kind AND d.authority_id = c.authority_id
               AND d.authority_version = c.authority_version AND d.status <> 'acknowledged'))
    FROM commodore.media_authority_current c
    JOIN commodore.media_authority_versions v
      ON v.authority_kind = c.authority_kind AND v.authority_id = c.authority_id AND v.authority_version = c.authority_version
    CROSS JOIN (SELECT created_at FROM commodore.media_placement_changes
                WHERE tenant_id = '$STACK_TENANT_ID' AND scope_kind = 'tenant' AND revision = $1) ch
    WHERE c.authority_kind = 'media_object' AND c.authority_id IN (${list%,})")
  [ "$result" = "0|0" ] || { echo "  streams not recompiled|with outstanding deliveries: $result"; return 1; }
}

[ -n "${STACK_TENANT_ID:-}" ] || { fail 'the run names no tenant'; finish; }
state=$(policy_state)
json_has "$state" '.data.mediaPlacementPolicy.__typename == "MediaPlacementPolicyState"' || { fail "read the tenant placement policy: $state"; finish; }
if ! json_has "$state" '[.data.mediaPlacementPolicy.verbs[] | select(.verb == "INGEST" and .ownRules != null)] | length == 0'; then
  blocked 'the tenant has its own INGEST rules; this scenario only sets and clears rules on a tenant without them'
  finish
fi

log "one live stream and $idle idle streams the cells hold before the change"
for i in $(seq 0 "$idle"); do
  s=$(create_stream "stack-rollout-$run-$i" false)
  id=$(echo "$s" | jq -r '.id // empty')
  [ -n "$id" ] || { fail "createStream: $s"; finish; }
  ids+=("$id")
  ids_uuid+=("$(echo "$s" | jq -r '.streamId')")
  if [ "$i" = 0 ]; then
    pb=$(echo "$s" | jq -r '.playbackId')
    live_key=$(echo "$s" | jq -r '.streamKey')
  else
    keys+=("$(echo "$s" | jq -r '.streamKey')")
  fi
done
publisher=$(publish_until_admitted "$EDGE_A_RTMP" "$live_key" 640x360 15 1500) || { fail 'the live stream publisher was admitted'; finish; }
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 90 "the live stream resolves for viewers at $base" media_at "$base" "$pb" || finish
done
# Resolving pulls each idle stream's authority into cell A, so the change
# below fans out to streams the cell holds.
for key in "${keys[@]}"; do
  for base in $FOGHORN_A_URLS; do
    warmed=0
    for _ in $(seq 20); do
      [ "$(ingest_status "$base" "$key")" = 200 ] && { warmed=1; break; }
      sleep 2
    done
    [ "$warmed" = 1 ] || { fail "idle stream $key resolves for ingest at $base"; finish; }
    sleep 1.1
  done
done
pass "all $idle idle streams resolve for ingest through every cell-A replica"

log 'a tenant INGEST rule is set and rolls out'
roll_out SET
if [ "$set_applied" = 1 ]; then
  log 'the rule is cleared and rolls out'
  roll_out CLEAR
fi
finish
