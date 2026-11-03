#!/usr/bin/env bash
# Customer streaming lifecycle, sourced by scenario 47 (the scenario's tenant on
# platform capacity) and scenario 51 (a fresh Free tenant on its own private
# cluster). The caller sets tenant, run, since and streams, and cookies when the
# session is a browser login whose access token needs refreshing.
# stream_flow sets stream, uuid, internal, class, cell, node and cluster for the
# helpers below while a flow runs.
# shellcheck disable=SC2154

setup_tenant_webhooks() {
  local response
  response=$(gql 'mutation($i:CreateWebhookEndpointInput!){createWebhookEndpoint(input:$i){... on WebhookEndpointSecret{secret endpoint{id}} ... on ValidationError{message}}}' \
    "$(jq -cn --arg u "$RECEIVER_HOOK_URL" '{i:{url:$u,description:"stack scenarios",eventTypes:["stream.live","stream.idle","recording.started","recording.stopped","recording.ready","clip.requested","clip.ready"]}}')")
  endpoint=$(echo "$response" | jq -r '.data.createWebhookEndpoint.endpoint.id // empty')
  [ -n "$endpoint" ] || { echo "$response"; return 1; }
  echo "$response" | jq -r '.data.createWebhookEndpoint | "\(.endpoint.id) \(.secret)"' >"$STACK_STATE_DIR/webhook-endpoint"
}
# webhook_events uses the helper below rather than the demo user's session.
webhook_endpoint() { cat "$STACK_STATE_DIR/webhook-endpoint"; }

# refresh_tenant_session: a browser login's access token expires within the
# flows, so it is rotated from the cookie jar; an API token needs nothing.
refresh_tenant_session() {
  [ -n "${cookies:-}" ] || return 0
  STACK_API_TOKEN=$(refresh_session_token "$cookies") || return 1
  export STACK_API_TOKEN
  next_session_refresh=$(($(date +%s) + 300))
}

# Constrain both publishing and viewing through the user-facing review/apply
# contract. A successful test must not silently use another capacity class.
set_placement() {
  local class=$1 policy revision parent updates review input applied warnings token
  scope=$(jq -cn --arg id "$2" '{kind:"STREAM",streamId:$id}')
  policy=$(gql 'query($s:MediaPlacementScopeInput!){mediaPlacementPolicy(scope:$s){__typename ... on MediaPlacementPolicyState{revision parentRevision}}}' "$(jq -cn --argjson s "$scope" '{s:$s}')")
  revision=$(echo "$policy" | jq -r '.data.mediaPlacementPolicy.revision // empty')
  parent=$(echo "$policy" | jq -r '.data.mediaPlacementPolicy.parentRevision // empty')
  [ -n "$revision" ] && [ -n "$parent" ] || { echo "$policy"; return 1; }
  updates=$(jq -cn --arg c "$class" '["INGEST","SERVE"]|map({verb:.,kind:"SET",rules:{schemaVersion:1,constraints:{allow:{any:[{classes:[$c]}]}}}})')
  input=$(jq -cn --argjson s "$scope" --arg r "$revision" --arg p "$parent" --argjson u "$updates" \
    '{scope:$s,expectedRevision:$r,expectedParentRevision:$p,updates:$u}')
  review=$(gql 'query($i:ReviewMediaPlacementChangeInput!){reviewMediaPlacementChange(input:$i){__typename ... on MediaPlacementReview{reviewToken warnings{id acknowledgementRequired}} ... on MediaPlacementError{message}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  token=$(echo "$review" | jq -r '.data.reviewMediaPlacementChange.reviewToken // empty')
  [ -n "$token" ] || { echo "$review"; return 1; }
  warnings=$(echo "$review" | jq -c '[.data.reviewMediaPlacementChange.warnings[]?|select(.acknowledgementRequired)|.id]')
  input=$(echo "$input" | jq -c --arg t "$token" --arg k "$run-$class-$(date +%s)" --argjson w "$warnings" '.+{reviewToken:$t,idempotencyKey:$k,acknowledgedWarningIds:$w}')
  applied=$(gql 'mutation($i:ApplyMediaPlacementChangeInput!){applyMediaPlacementChange(input:$i){__typename ... on MediaPlacementChange{revision} ... on MediaPlacementError{message}}}' "$(jq -cn --argjson i "$input" '{i:$i}')")
  json_has "$applied" '.data.applyMediaPlacementChange.__typename == "MediaPlacementChange"' || { echo "$applied"; return 1; }
  policy_revision=$(echo "$applied" | jq -r '.data.applyMediaPlacementChange.revision')
  policy_parent=$parent
}
stream_ended() { [ "$(pg "$1" "SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id='$tenant' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL")" = 0 ]; }
# stream_active <foghorn db> <node or empty> <cluster>: one active ingest
# session in the cluster, on that node when one is named. Sets admitted_node.
stream_active() {
  admitted_node=$(pg "$1" "SELECT node_id FROM foghorn.ingest_sessions WHERE tenant_id='$tenant' AND stream_internal_name IN ('$internal','live+$internal') AND ended_at IS NULL AND projection_state='active' AND ingest_cluster_id='$3'")
  [ -n "$admitted_node" ] && [ "$(printf '%s\n' "$admitted_node" | wc -l | tr -d ' ')" = 1 ] && { [ -z "$2" ] || [ "$admitted_node" = "$2" ]; }
}
policy_effective() {
  local policy
  policy=$(gql 'query($s:MediaPlacementScopeInput!){mediaPlacementPolicy(scope:$s){... on MediaPlacementPolicyState{revision activeRevision parentRevision activeParentRevision rollout{status requiredRecipients appliedRecipients}}}}' "$(jq -cn --argjson s "$scope" '{s:$s}')")
  json_has "$policy" '.data.mediaPlacementPolicy | .rollout.status == "EFFECTIVE" and .revision == .activeRevision and .parentRevision == .activeParentRevision and .rollout.requiredRecipients == .rollout.appliedRecipients'
}
preview_ready() {
  local preview
  preview=$(gql 'query($i:PreviewMediaPlacementInput!){previewMediaPlacement(input:$i){__typename ... on MediaPlacementPreview{complete selected{nodeId clusterId} reason} ... on MediaPlacementError{message}}}' \
    "$(jq -cn --argjson s "$scope" --arg p "$1" --arg r "$policy_revision" --arg a "$policy_parent" '{i:{scope:$s,verb:"INGEST",protocol:$p,expectedRevision:$r,expectedParentRevision:$a}}')")
  printf '%s\n' "$preview" >"$STACK_STATE_DIR/$run-preview.json"
  json_has "$preview" '.data.previewMediaPlacement | .__typename == "MediaPlacementPreview" and .complete and .selected.clusterId != null' || return 1
  # Platform node identities are intentionally hidden from ordinary tenants.
  if [ "$2" = TENANT_PRIVATE ]; then
    [ "$(echo "$preview" | jq -r '.data.previewMediaPlacement.selected.nodeId')" = "$3" ]
  fi
}
private_media_at() {
  local location
  location=$(location_at "$1" "$2")
  [ -n "$location" ] || return 1
  python3 - "$location" "$3:8082" <<'PY'
import sys, urllib.parse, urllib.request

url, expected_host = sys.argv[1:]
try:
    for _ in range(4):
        if urllib.parse.urlsplit(url).netloc != expected_host:
            sys.exit(1)
        with urllib.request.urlopen(url, timeout=10) as response:
            if response.status != 200 or urllib.parse.urlsplit(response.geturl()).netloc != expected_host:
                sys.exit(1)
            body = response.read()
        if urllib.parse.urlsplit(url).path.endswith('.m3u8'):
            entries = [line.strip() for line in body.decode().splitlines() if line.strip() and not line.startswith('#')]
            if not entries:
                sys.exit(1)
            url = urllib.parse.urljoin(url, entries[0])
            continue
        valid = len(body) >= 3 * 188 and all(body[offset] == 0x47 for offset in (0, 188, 376))
        sys.exit(0 if valid else 1)
except Exception:
    sys.exit(1)
sys.exit(1)
PY
}

# stream_flow <class> <ingest host> <foghorn db> <node or empty> <cluster>
# <protocol> <foghorn base> [admission]: one stream published on the class.
# A full flow also records RTMP (DVR, a viewer, a clip, replay), checks its
# signed lifecycle webhooks and final analytics, and is listed for the run's
# analytics settlement. "admission" stops after admission, media and cleanup.
stream_flow() {
  local class=$1 host=$2 cell=$3 node=$4 cluster=$5 protocol=$6 base=$7 depth=${8:-full} s pb key internal uuid viewer record=false
  refresh_tenant_session || { fail 'refresh the tenant session'; return 1; }
  [ "$protocol" != rtmp ] || [ "$depth" != full ] || record=true
  s=$(create_stream "stack-$run-$class-$protocol" "$record")
  stream=$(echo "$s" | jq -r '.id // empty')
  pb=$(echo "$s" | jq -r '.playbackId // empty')
  key=$(echo "$s" | jq -r '.streamKey // empty')
  uuid=$(echo "$s" | jq -r '.streamId // empty')
  [ -n "$stream" ] || { fail "create stream as the tenant: $s"; return 1; }
  streams+=("$stream")
  internal=$(internal_name_of "$uuid")
  if ! set_placement "$class" "$uuid"; then fail "$class $protocol placement policy applies"; return 1; fi
  pass "$class $protocol placement policy applies"
  eventually 90 "$class $protocol placement revision is effective" policy_effective || return 1
  eventually 60 "$class $protocol preview has fresh placement evidence" preview_ready "$protocol" "$class" "$node" || return 1
  if [ "$protocol" = rtmp ]; then
    publisher=$(publish "rtmp://$host:1935/live" "$key" 320x240 10 600)
  else
    ffmpeg -hide_banner -loglevel error -re -f lavfi -i 'testsrc2=size=320x240:rate=10' \
      -f lavfi -i 'sine=frequency=440:sample_rate=48000' -t 600 -c:v libx264 -preset veryfast -g 20 \
      -pix_fmt yuv420p -c:a aac -b:a 96k -f mpegts "srt://$host:8889?mode=caller&streamid=$key" \
      >"$STACK_STATE_DIR/$run-srt.log" 2>&1 &
    publisher=$!
  fi
  eventually 90 "$class $protocol first publisher admits to the expected node and cluster" stream_active "$cell" "$node" "$cluster" || return 1
  if [ "$class" = TENANT_PRIVATE ]; then
    for viewer in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
      eventually 60 "$class $protocol HLS manifests and media via $viewer stay on the private edge" private_media_at "$viewer" "$pb" "$host" || return 1
    done
  else
    eventually 60 "$class $protocol serves real HLS media" media_at "$base" "$pb" || return 1
  fi
  check "$class $protocol publisher stays connected" kill -0 "$publisher"
  if [ "$depth" = full ]; then
    eventually 90 "$class $protocol signed stream.live webhook belongs to this stream" saw_event "$since" stream.live ".streamId == \"$uuid\"" || return 1
  fi
  if [ "$record" = true ]; then
    tenant_live_artifacts "$base" || return 1
  fi
  kill "$publisher" 2>/dev/null
  publisher=""
  eventually 90 "$class $protocol session ends on disconnect" stream_ended "$cell" || return 1
  if [ "$depth" = full ]; then
    eventually 90 "$class $protocol signed stream.idle webhook belongs to this stream" saw_event "$since" stream.idle ".streamId == \"$uuid\"" || return 1
    eventually 180 "$class $protocol final analytics match the node, cluster and media lifecycle with ingest bytes" tenant_final_analytics "$cell" "$admitted_node" "$cluster" || return 1
  fi
  if [ "$record" = true ]; then
    tenant_replay_artifacts || return 1
  fi
  [ "$depth" != full ] || printf '%s\t%s\t%s\t%s\n' "$class" "$protocol" "$stream" "$uuid" >>"$STACK_STATE_DIR/analytics-flows.tsv"
  stream=""
}

tenant_dvr_started() {
  local row
  row=$(pg commodore "SELECT id,dvr_hash FROM commodore.dvr_recordings WHERE tenant_id='$tenant' AND stream_id='$uuid' ORDER BY created_at DESC LIMIT 1")
  IFS='|' read -r dvr_id dvr_hash <<<"$row"
  [ -n "$dvr_id" ] && [ -n "$dvr_hash" ]
}
tenant_chapters() {
  gql 'query($d:ID!){dvrChapters(dvrId:$d){chapters{state playbackId segmentCount}}}' "$(jq -cn --arg d "$dvr_id" '{d:$d}')" |
    jq -c '.data.dvrChapters.chapters // []'
}
tenant_clip_ready() {
  local response
  response=$(gql 'query($id:ID!){node(id:$id){... on Clip{status}}}' "$(jq -cn --arg id "$clip" '{id:$id}')")
  case "$(echo "$response" | jq -r '.data.node.status // empty')" in
    ready|done|READY|DONE) return 0 ;; failed|FAILED) echo "$response"; return 2 ;;
  esac
  return 1
}
tenant_request_clip() {
  local response now
  now=$(date +%s)
  response=$(gql 'mutation($i:CreateClipInput!){createClip(input:$i){__typename ... on Clip{id clipHash playbackId} ... on ValidationError{message}}}' \
    "$(jq -cn --arg s "$stream" --argjson start "$((now - 15))" '{i:{streamId:$s,title:"stack tenant lifecycle clip",mode:"DURATION",startUnix:$start,duration:6}}')")
  clip=$(echo "$response" | jq -r '.data.createClip.id // empty')
  clip_hash=$(echo "$response" | jq -r '.data.createClip.clipHash // empty')
  clip_pb=$(echo "$response" | jq -r '.data.createClip.playbackId // empty')
  [ -n "$clip" ] && return 0
  echo "$response" | grep -q 'no source with at least one whole second' && return 1
  echo "$response"; return 2
}
tenant_live_artifacts() {
  local base=$1 location origin
  eventually 90 "$class DVR starts automatically" tenant_dvr_started || return 1
  eventually 90 "$class signed recording.started names this stream" saw_event "$since" recording.started ".artifact.streamId == \"$uuid\"" || return 1
  # Consume enough HLS to produce an actual viewer session and buffer history.
  location=$(location_at "$base" "$pb")
  if timeout 90 ffmpeg -hide_banner -loglevel error -i "$location" -t 20 -f null - >"$STACK_STATE_DIR/$class-viewer.log" 2>&1; then
    pass "$class viewer decodes twenty seconds of HLS"
  else
    fail "$class viewer cannot decode HLS"; return 1
  fi
  eventually 90 "$class tenant requests a clip from its live stream" tenant_request_clip || return 1
  origin=$(pg "$cell" "SELECT origin_cluster_id FROM foghorn.artifacts WHERE tenant_id='$tenant' AND artifact_hash='$clip_hash'")
  if [ "$origin" != "$cluster" ]; then
    fail "$class clip origin $origin does not match its admitted cluster $cluster"
    return 1
  fi
  pass "$class clip origin matches its admitted cluster"
  eventually 240 "$class clip finishes" tenant_clip_ready || return 1
  eventually 90 "$class signed clip.ready names this clip" saw_event "$since" clip.ready ".artifact.artifactId == \"$clip_hash\"" || return 1
  every_replica "$class clip serves real media" "$clip_pb" media_at
}
tenant_dvr_finalized() {
  local chapters
  chapters=$(tenant_chapters)
  echo "$chapters" | jq -e 'length > 0 and all((.state == "FINALIZED" or .state == "FROZEN") and .segmentCount > 0 and (.playbackId | length > 0))' >/dev/null
}
tenant_replay_artifacts() {
  local playback
  eventually 240 "$class DVR chapters finalize after disconnect" tenant_dvr_finalized || return 1
  playback=$(tenant_chapters | jq -r '.[0].playbackId')
  eventually 90 "$class signed recording.ready names a replayable chapter" saw_event "$since" recording.ready ".artifact.streamId == \"$uuid\" and .artifact.playbackId == \"$playback\"" || return 1
  every_replica "$class DVR chapter replays real media" "$playback" media_at
  gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' "$(jq -cn --arg id "$clip" '{id:$id}')" >/dev/null
  clip=""
  gql 'mutation($id:ID!){deleteDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$dvr_hash" '{id:$id}')" >/dev/null
  dvr_hash=""
}
# tenant_all_analytics: every full flow of the run settled with exact runtime,
# no session left live, and viewer analytics for each recorded RTMP stream.
tenant_all_analytics() {
  local class protocol stream uuid
  if [ -n "${cookies:-}" ] && [ "$(date +%s)" -ge "$next_session_refresh" ]; then
    refresh_tenant_session || return 1
  fi
  while IFS=$'\t' read -r class protocol stream uuid; do
    [ "$protocol" != rtmp ] || tenant_viewer_analytics || return 1
  done <"$STACK_STATE_DIR/analytics-flows.tsv"
  python3 - "$STACK_LIB_DIR" "$STACK_STATE_DIR/analytics-flows.tsv" "$since" <<'PY'
import os, sys

sys.path.insert(0, sys.argv[1])
from sweep import Sources, collect_runtime, runtime_violations

with open(sys.argv[2]) as flows:
    streams = {line.rstrip().split('\t')[3] for line in flows if line.strip()}
runtime = collect_runtime(Sources(os.environ), int(sys.argv[3]), streams)
if runtime is None or not streams:
    sys.exit(1)
bad, judged = runtime_violations(**runtime)
final_streams = {row['stream'] for row in runtime['finals']}
sys.exit(0 if not bad and judged == len(streams) and final_streams == streams and not runtime['live'] else 1)
PY
}
tenant_viewer_analytics() {
  local response
  response=$(gql 'query($s:ID!){analytics{usage{streaming{streamAnalyticsSummary(streamId:$s){rangeTotalSessions rangeViewerHours rangeEgressGb}}}}}' "$(jq -cn --arg s "$stream" '{s:$s}')")
  echo "$response" >"$STACK_STATE_DIR/$class-analytics.json"
  json_has "$response" '.errors == null and (.data.analytics.usage.streaming.streamAnalyticsSummary | .rangeTotalSessions > 0 and .rangeViewerHours > 0 and .rangeEgressGb > 0)'
}
tenant_final_analytics() {
  local db=$1 node=$2 cluster=$3 start end rows
  IFS='|' read -r start end <<<"$(pg "$db" "SELECT floor(EXTRACT(EPOCH FROM started_at)*1000)::bigint,floor(EXTRACT(EPOCH FROM ended_at)*1000)::bigint FROM foghorn.ingest_sessions WHERE tenant_id='$tenant' AND stream_internal_name IN ('$internal','live+$internal') AND projection_state <> 'pending'")"
  [ -n "$start" ] && [ -n "$end" ] || return 1
  # Publisher admission closes on disconnect; STREAM_END follows the live
  # buffer draining. Final accounting must match that media lifecycle.
  rows=$(ch "SELECT f.node_id,f.cluster_id,f.source_started_at_ms,f.source_ended_at_ms,f.downloaded_bytes,e.event_started_ms,e.event_ended_ms
    FROM periscope.stream_sessions_final_v f CROSS JOIN (
      SELECT minIf(toInt64(toUnixTimestamp(timestamp))*1000,event_type='stream_start') AS event_started_ms,
        maxIf(toInt64(toUnixTimestamp(timestamp))*1000,event_type='stream_end') AS event_ended_ms
      FROM periscope.stream_event_log WHERE tenant_id=toUUID('$tenant') AND stream_id=toUUID('$uuid')
        AND internal_name IN ('$internal','live+$internal')) e
    WHERE f.tenant_id=toUUID('$tenant') AND f.stream_id=toUUID('$uuid') AND f.closed_reason='final' FORMAT JSONEachRow")
  printf '%s\n' "$rows" >"$STACK_STATE_DIR/$class-$protocol-final.jsonl"
  echo "$rows" | jq -se --arg n "$node" --arg c "$cluster" --argjson s "$start" --argjson e "$end" \
    'length == 1 and all((.source_started_at_ms | tonumber) as $started | (.source_ended_at_ms | tonumber) as $ended |
      .node_id == $n and .cluster_id == $c and ($started - $s | fabs) < 5000 and $ended >= ($e - 5000) and
      (.event_started_ms | tonumber) > 0 and (.event_ended_ms | tonumber) > 0 and
      ($started - (.event_started_ms | tonumber) | fabs) < 5000 and ($ended - (.event_ended_ms | tonumber) | fabs) < 5000 and
      $ended > $started and (.downloaded_bytes | tonumber) > 0)' >/dev/null
}
