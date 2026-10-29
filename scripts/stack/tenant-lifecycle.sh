#!/usr/bin/env bash
# Sourced by scenario 47 inside the fresh tenant session. The caller supplies
# stream, uuid, internal, class, cell, node and cluster for the current flow.
# shellcheck disable=SC2154

setup_tenant_webhooks() {
  local response
  response=$(gql 'mutation($i:CreateWebhookEndpointInput!){createWebhookEndpoint(input:$i){... on WebhookEndpointSecret{secret endpoint{id}} ... on ValidationError{message}}}' \
    "$(jq -cn --arg u "$RECEIVER_HOOK_URL" '{i:{url:$u,description:"Free streaming lifecycle",eventTypes:["stream.live","stream.idle","recording.started","recording.stopped","recording.ready","clip.requested","clip.ready"]}}')")
  endpoint=$(echo "$response" | jq -r '.data.createWebhookEndpoint.endpoint.id // empty')
  [ -n "$endpoint" ] || { echo "$response"; return 1; }
  echo "$response" | jq -r '.data.createWebhookEndpoint | "\(.endpoint.id) \(.secret)"' >"$STACK_STATE_DIR/webhook-endpoint"
}
# webhook_events uses the helper below rather than the demo user's session.
webhook_endpoint() { cat "$STACK_STATE_DIR/webhook-endpoint"; }

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
    "$(jq -cn --arg s "$stream" --argjson start "$((now - 15))" '{i:{streamId:$s,title:"stack Free lifecycle clip",mode:"DURATION",startUnix:$start,duration:6}}')")
  clip=$(echo "$response" | jq -r '.data.createClip.id // empty')
  clip_hash=$(echo "$response" | jq -r '.data.createClip.clipHash // empty')
  clip_pb=$(echo "$response" | jq -r '.data.createClip.playbackId // empty')
  [ -n "$clip" ] && return 0
  echo "$response" | grep -q 'no source with at least one whole second' && return 1
  echo "$response"; return 2
}
tenant_live_artifacts() {
  local base=$1 location
  eventually 90 "$class Free DVR starts automatically" tenant_dvr_started || return 1
  eventually 90 "$class signed recording.started names this stream" saw_event "$since" recording.started ".artifact.streamId == \"$uuid\"" || return 1
  # Consume enough HLS to produce an actual viewer session and buffer history.
  location=$(location_at "$base" "$pb")
  if timeout 90 ffmpeg -hide_banner -loglevel error -i "$location" -t 20 -f null - >"$STACK_STATE_DIR/$class-viewer.log" 2>&1; then
    pass "$class Free viewer decodes twenty seconds of HLS"
  else
    fail "$class Free viewer cannot decode HLS"; return 1
  fi
  eventually 90 "$class Free tenant requests a clip from its live stream" tenant_request_clip || return 1
  eventually 240 "$class Free clip finishes" tenant_clip_ready || return 1
  eventually 90 "$class signed clip.ready names this clip" saw_event "$since" clip.ready ".artifact.artifactId == \"$clip_hash\"" || return 1
  every_replica "$class Free clip serves real media" "$clip_pb" media_at
}
tenant_dvr_finalized() {
  local chapters
  chapters=$(tenant_chapters)
  echo "$chapters" | jq -e 'length > 0 and all((.state == "FINALIZED" or .state == "FROZEN") and .segmentCount > 0 and (.playbackId | length > 0))' >/dev/null
}
tenant_replay_artifacts() {
  local playback
  eventually 240 "$class Free DVR chapters finalize after disconnect" tenant_dvr_finalized || return 1
  playback=$(tenant_chapters | jq -r '.[0].playbackId')
  eventually 90 "$class signed recording.ready names a replayable chapter" saw_event "$since" recording.ready ".artifact.streamId == \"$uuid\" and .artifact.playbackId == \"$playback\"" || return 1
  every_replica "$class Free DVR chapter replays real media" "$playback" media_at
  gql 'mutation($id:ID!){deleteClip(id:$id){__typename}}' "$(jq -cn --arg id "$clip" '{id:$id}')" >/dev/null
  clip=""
  gql 'mutation($id:ID!){deleteDVR(dvrHash:$id){__typename}}' "$(jq -cn --arg id "$dvr_hash" '{id:$id}')" >/dev/null
  dvr_hash=""
}
tenant_all_analytics() {
  local class protocol stream uuid
  if [ "$(date +%s)" -ge "$next_session_refresh" ]; then
    STACK_API_TOKEN=$(refresh_session_token "$cookies") || return 1
    export STACK_API_TOKEN
    next_session_refresh=$(($(date +%s) + 300))
  fi
  while IFS=$'\t' read -r class protocol stream uuid; do
    [ "$protocol" != rtmp ] || tenant_viewer_analytics || return 1
  done <"$STACK_STATE_DIR/analytics-flows.tsv"
  python3 - "$STACK_LIB_DIR" "$STACK_STATE_DIR/analytics-flows.tsv" "$since" <<'PY'
import os, sys

sys.path.insert(0, sys.argv[1])
from sweep import Sources, collect_runtime, runtime_violations

with open(sys.argv[2]) as flows:
    streams = {line.rstrip().split('\t')[3] for line in flows}
runtime = collect_runtime(Sources(os.environ), int(sys.argv[3]), streams)
if runtime is None or len(streams) != 4:
    sys.exit(1)
bad, judged = runtime_violations(**runtime)
final_streams = {row['stream'] for row in runtime['finals']}
sys.exit(0 if not bad and judged == 4 and final_streams == streams and not runtime['live'] else 1)
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
  rows=$(ch "SELECT node_id,cluster_id,source_started_at_ms,source_ended_at_ms,downloaded_bytes FROM periscope.stream_sessions_final_v WHERE tenant_id=toUUID('$tenant') AND stream_id=toUUID('$uuid') AND closed_reason='final' FORMAT JSONEachRow")
  printf '%s\n' "$rows" >"$STACK_STATE_DIR/$class-$protocol-final.jsonl"
  echo "$rows" | jq -se --arg n "$node" --arg c "$cluster" --argjson s "$start" --argjson e "$end" \
    'length == 1 and all((.source_started_at_ms | tonumber) as $start | (.source_ended_at_ms | tonumber) as $end |
      .node_id == $n and .cluster_id == $c and ($start - $s | fabs) < 5000 and ($end - $e | fabs) < 5000 and $end > $start and (.downloaded_bytes | tonumber) > 0)' >/dev/null
}
