#!/usr/bin/env bash
# stack-scenario: manual
# Node lifecycle faults (slow and disruptive, so manual only):
#   1. Helmsman restarts with a processing job in flight: the job finishes or is
#      re-dispatched within minutes, never after the 30-min lease.
#   2. Edge auto-update to a newly staged release while a stream is live:
#      Foghorn's node_components reaches the target Helmsman and Mist versions,
#      the update ends idle, the node returns to normal mode (no leftover
#      update fence), and the stream keeps its publisher, its ingest
#      generation and its media through the Helmsman restart and Mist reload.
#   3. Privateer restart while services resolve .internal names.
# Catches: R3 (in-flight jobs orphaned until lease expiry), N3 (update fence
# never lifted, node excluded from later updates), WS3b (Mist API wedged after
# a rolling reload), F12 (.internal leaked to the LAN resolver).
. "$(dirname "$0")/../lib.sh"

EDGE_SERVICE=${STACK_EDGE_A_SERVICE:-edge}
need ffmpeg jq curl || finish

log "1. Helmsman restart with a processing job in flight"
render_mp4 "$STACK_STATE_DIR/vod-restart.mp4" 854x480 15 60 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-restart.mp4" "stack restart vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
if [ -n "$VID" ]; then
  processing() {
    [ "$(pg "$FOGHORN_A_DB" "SELECT status FROM foghorn.processing_jobs WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH' ORDER BY created_at DESC LIMIT 1")" = processing ]
  }
  if eventually 120 "processing job running on the edge" processing; then
    # s6 owns Helmsman in the edge bundle; a pkill fallback would also match
    # its own `sh -c` command line and kill the shell running it.
    if stack_exec "$EDGE_SERVICE" /command/s6-svc -r /run/service/helmsman; then
      pass "Helmsman restarted mid-job"
      # Later checks and scenarios publish through this edge, whose ingest
      # triggers Helmsman answers.
      helmsman_up() { stack_exec "$EDGE_SERVICE" curl -sf -m 3 -o /dev/null http://localhost:18007/health; }
      eventually 60 "Helmsman serves again after the restart" helmsman_up
      terminal() {
        local s
        s=$(vod_status "$VID")
        echo "    status: $s"
        [ "$s" = READY ] && return 0
        [ "$s" = FAILED ] && return 2
        return 1
      }
      eventually 300 "the job completes after the restart (not after the 30-min lease)" terminal
    else
      fail "Helmsman restart command failed"
    fi
  fi
else
  fail "upload: $V"
fi

log "2. edge auto-update while live"
# The stack stages the release itself: the edge's own image-baked Helmsman
# binary and Mist tree, packed as release artifacts under new version labels,
# served from this runner over HTTP and published as cell A's release target in
# Quartermaster's catalog tables, which Foghorn's release reconciler reads. A
# staging run stages its target with STACK_EDGE_UPDATE_CMD and names the
# versions it rolls out in STACK_EDGE_UPDATE_HELMSMAN_VERSION and
# STACK_EDGE_UPDATE_MIST_VERSION.
UPDATE_CLUSTER=${FOGHORN_A_CLUSTER:-demo-media}
UPDATE_SRV="" UPDATE_STAGED="" SID="" PUB=""
update_cleanup() {
  [ -n "$PUB" ] && kill "$PUB" 2>/dev/null
  [ -n "$SID" ] && delete_stream "$SID"
  [ -n "$UPDATE_SRV" ] && kill "$UPDATE_SRV" 2>/dev/null
  if [ -n "$UPDATE_STAGED" ]; then
    # The target goes first so the reconciler cannot push the release again.
    # The edge then reports its image-seeded version labels again: the edge
    # seed replaces an installed component on a later image only while its
    # label is the image-seeded one.
    pg quartermaster "DELETE FROM quartermaster.cluster_release_targets WHERE cluster_id='$UPDATE_CLUSTER';
      DELETE FROM quartermaster.edge_releases WHERE channel='stable' AND version='$UPDATE_STAGED'" >/dev/null
    # shellcheck disable=SC2016 # expanded by the edge's shell
    stack_exec "$EDGE_SERVICE" sh -c 'f=/etc/frameworks/component-versions.env
      [ -e "$f.stack-update" ] || exit 0
      mv "$f.stack-update" "$f" && chown frameworks:frameworks "$f"' &&
      stack_exec "$EDGE_SERVICE" /command/s6-svc -r /run/service/helmsman
  fi
}
trap update_cleanup EXIT
# stage_stack_release <version>: sets WANT_HELMSMAN and WANT_MIST.
stage_stack_release() {
  local v=$1 dir="$STACK_STATE_DIR/edge-release" arch hs ms base
  case "$(stack_exec "$EDGE_SERVICE" uname -m | tr -d '\r')" in
    x86_64) arch=amd64 ;;
    aarch64) arch=arm64 ;;
    *) echo "    unknown edge architecture"; return 1 ;;
  esac
  [ -z "$(pg quartermaster "SELECT 1 FROM quartermaster.cluster_release_targets WHERE cluster_id='$UPDATE_CLUSTER'")" ] ||
    { echo "    $UPDATE_CLUSTER already has a release target"; return 1; }
  rm -rf "$dir" && mkdir -p "$dir" || return 1
  stack_exec "$EDGE_SERVICE" tar -C /usr/share/frameworks/dist/helmsman -czf - helmsman >"$dir/helmsman.tar.gz" || return 1
  # Mist's updater wants bin/MistController at the archive root and keeps no
  # hard links.
  stack_exec "$EDGE_SERVICE" tar --hard-dereference -C /usr/share/frameworks/dist/mistserver -czf - . >"$dir/mist.tar.gz" || return 1
  hs=sha256:$(sha256sum "$dir/helmsman.tar.gz" | cut -d' ' -f1)
  ms=sha256:$(sha256sum "$dir/mist.tar.gz" | cut -d' ' -f1)
  python3 -m http.server 18990 --bind 0.0.0.0 --directory "$dir" >"$dir.log" 2>&1 &
  UPDATE_SRV=$!
  base="http://$(hostname -i | awk '{print $1}'):18990"
  served() { stack_exec "$EDGE_SERVICE" curl -sf -o /dev/null -m 5 "$base/mist.tar.gz"; }
  eventually 30 "the edge reaches the staged artifacts at $base" served || return 1
  # Restored on cleanup; Helmsman rewrites the file as it records each update.
  stack_exec "$EDGE_SERVICE" cp -p /etc/frameworks/component-versions.env /etc/frameworks/component-versions.env.stack-update || return 1
  WANT_HELMSMAN="$v-helmsman" WANT_MIST="$v-mist"
  UPDATE_STAGED=$v
  pg quartermaster "INSERT INTO quartermaster.edge_releases (channel, version, components) VALUES ('stable', '$v',
      '{\"helmsman\":{\"version\":\"$WANT_HELMSMAN\",\"artifacts\":{\"linux/$arch\":{\"artifact_url\":\"$base/helmsman.tar.gz\",\"checksum\":\"$hs\"}}},
        \"mist\":{\"version\":\"$WANT_MIST\",\"artifacts\":{\"linux/$arch\":{\"artifact_url\":\"$base/mist.tar.gz\",\"checksum\":\"$ms\"}}}}'::jsonb);
    INSERT INTO quartermaster.cluster_release_targets (cluster_id, channel, target_version, rollout_plan, paused)
      VALUES ('$UPDATE_CLUSTER', 'stable', '$v', '{}', false) RETURNING 1" | grep -q 1
}
WANT_HELMSMAN="" WANT_MIST="" STAGED=0
if [ "${STACK_TARGET:-stack}" = stack ]; then
  need python3 sha256sum && stage_ready=1 || stage_ready=0
elif [ -n "${STACK_EDGE_UPDATE_CMD:-}" ] && [ -n "${STACK_EDGE_UPDATE_HELMSMAN_VERSION:-}" ] && [ -n "${STACK_EDGE_UPDATE_MIST_VERSION:-}" ]; then
  stage_ready=1
else
  blocked "auto-update on $STACK_TARGET needs STACK_EDGE_UPDATE_CMD with STACK_EDGE_UPDATE_HELMSMAN_VERSION and STACK_EDGE_UPDATE_MIST_VERSION"
  stage_ready=0
fi
if [ "$stage_ready" = 1 ]; then
  EDGE_NODE=${STACK_EDGE_A_NODE:-edge-node-1}
  S=$(create_stream "stack-update-$(date +%s)" false)
  SID=$(echo "$S" | jq -r '.id // empty')
  PB=$(echo "$S" | jq -r '.playbackId // empty')
  IN=$(internal_name_of "$(echo "$S" | jq -r '.streamId // empty')")
  if [ -z "$SID" ]; then
    fail "createStream: $S"
  elif ! PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 900); then
    PUB=""
    fail "publisher admitted before the update"
  else
    eventually 90 "stream live before the update" media_at "${FOGHORN_A_URLS%% *}" "$PB"
    G1=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
    echo "    generation $G1"
    if [ "${STACK_TARGET:-stack}" = stack ]; then
      stage_stack_release "stack-$(date +%s)" && STAGED=1
    else
      WANT_HELMSMAN=$STACK_EDGE_UPDATE_HELMSMAN_VERSION WANT_MIST=$STACK_EDGE_UPDATE_MIST_VERSION
      bash -c "$STACK_EDGE_UPDATE_CMD" && STAGED=1
    fi
  fi
  if [ "$STAGED" = 1 ]; then
    pass "edge release target staged (helmsman $WANT_HELMSMAN, mist $WANT_MIST)"
    components_at_target() {
      local got h m phase
      got=$(pg "$FOGHORN_A_DB" "SELECT component || '=' || COALESCE(current_version, '') FROM foghorn.node_components
          WHERE node_id='$EDGE_NODE' AND component IN ('helmsman', 'mist') ORDER BY component")
      phase=$(pg "$FOGHORN_A_DB" "SELECT phase || ' ' || COALESCE(target_release, '') || ' ' || COALESCE(last_error, '')
          FROM foghorn.node_update_state WHERE node_id='$EDGE_NODE'")
      echo "    $(echo "$got" | tr '\n' ' ')| phase: $phase"
      case "$phase" in failed*) return 2 ;; esac
      h=$(echo "$got" | sed -n 's/^helmsman=//p')
      m=$(echo "$got" | sed -n 's/^mist=//p')
      [ "$h" = "$WANT_HELMSMAN" ] && [ "$m" = "$WANT_MIST" ] && [ "${phase%% *}" = idle ]
    }
    eventually 900 "node_components reports helmsman and mist at the target and the update ends idle" components_at_target
    # The node's durable operational mode; the tenant API token cannot read
    # infrastructure nodes. No row is normal.
    mode_normal() {
      local m
      m=$(pg "$FOGHORN_A_DB" "SELECT COALESCE((SELECT mode FROM foghorn.node_maintenance WHERE node_id='$EDGE_NODE'), 'normal')")
      echo "    $EDGE_NODE mode: $m"
      [ "$m" = normal ]
    }
    eventually 120 "the updated edge is back in normal mode (no leftover update fence)" mode_normal
    check "the publisher survived the update" kill -0 "$PUB"
    eventually 60 "the live stream still serves media after the update" media_at "${FOGHORN_A_URLS%% *}" "$PB"
    G2=$(open_generation "$FOGHORN_A_DB" "$IN" | head -1)
    check "the stream kept ingest generation $G1 through the update (now $G2)" test -n "$G1" -a "$G2" = "$G1"
  elif [ -n "$SID" ] && [ -n "$PUB" ]; then
    fail "stage the edge release target"
  fi
fi
update_cleanup
UPDATE_STAGED="" UPDATE_SRV="" SID="" PUB=""

log "3. Privateer restart while resolving .internal"
if stack_ctl restart privateer 2>/dev/null; then
  leaked=0
  for _ in $(seq 1 40); do
    addr=$(getent hosts quartermaster.internal 2>/dev/null | awk '{print $1}')
    case "$addr" in "" | 10.* | 172.* | 192.168.*) ;; *) leaked=$((leaked + 1)); echo "    quartermaster.internal -> $addr" ;; esac
    sleep 0.5
  done
  check ".internal never resolved outside the mesh during the restart" [ "$leaked" = 0 ]
else
  blocked "the stack has no privateer service; F12 remains a staging check"
fi
finish
