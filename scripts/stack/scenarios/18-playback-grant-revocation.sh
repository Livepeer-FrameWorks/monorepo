#!/usr/bin/env bash
# stack-scenario: default
# A revoked signing key ends exactly the viewer sessions whose token it
# signed, on the edge, whichever Foghorn replica applied the revocation and
# whichever replica granted the edge. Viewers watch a JWT stream with tokens
# from three keys; keys are revoked one at a time until a revocation was
# applied by the replica that does not hold the serving edge's control stream
# (the grant push then comes from the peer announcement).
# Measures: the edge re-checks its sessions on the new grant and refuses the
# revoked key's sessions locally; no per-viewer PLAY_REWRITE or USER_NEW
# reaches Foghorn for the re-check; the other viewers keep playing.
# Catches: an authority change that only reaches edges granted by the
# replica that applied it; per-viewer USER_NEW herds on revocation.
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 docker || finish

new_key() { # new_key <name> -> {id kid pem}
  gql 'mutation($i:CreateSigningKeyInput!){createSigningKey(input:$i){__typename ... on CreateSigningKeySuccess{privateKeyPem signingKey{id kid}}}}' \
    "$(jq -cn --arg n "$1" '{i:{name:$n}}')" | jq -c '.data.createSigningKey | {id:.signingKey.id, kid:.signingKey.kid, pem:.privateKeyPem}'
}
mint() { # mint <key json> <subject>: an ES256 viewer JWT valid for an hour
  python3 - "$1" "$2" <<'PY'
import base64, json, sys, time
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature
key = json.loads(sys.argv[1])
b64 = lambda raw: base64.urlsafe_b64encode(raw).rstrip(b"=").decode()
now = int(time.time())
head = b64(json.dumps({"alg": "ES256", "typ": "JWT", "kid": key["kid"]}).encode())
body = b64(json.dumps({"sub": sys.argv[2], "iat": now, "exp": now + 3600}).encode())
private = serialization.load_pem_private_key(key["pem"].encode(), password=None)
r, s = decode_dss_signature(private.sign(f"{head}.{body}".encode(), ec.ECDSA(hashes.SHA256())))
print(f"{head}.{body}.{b64(r.to_bytes(32, 'big') + s.to_bytes(32, 'big'))}")
PY
}
KEYS=()
cleanup() {
  kill "${PUB:-}" 2>/dev/null
  [ -n "${SID:-}" ] && delete_stream "$SID"
  local k
  for k in "${KEYS[@]}"; do
    gql 'mutation($id:ID!){revokeSigningKey(id:$id){__typename}}' "$(jq -cn --arg id "$k" '{id:$id}')" >/dev/null
  done
}
trap cleanup EXIT

S=$(create_stream "stack-revoke-$(date +%s)" false)
SID=$(echo "$S" | jq -r '.id // empty')
UUID=$(echo "$S" | jq -r '.streamId // empty')
PB=$(echo "$S" | jq -r '.playbackId // empty')
[ -n "$SID" ] || { fail "createStream: $S"; finish; }
IN=$(internal_name_of "$UUID")
declare -A KEY TOKEN
for n in 1 2 3; do
  KEY[$n]=$(new_key "stack-revoke-$n-$(date +%s)")
  [ "$(echo "${KEY[$n]}" | jq -r '.kid // empty')" != "" ] || { fail "createSigningKey: ${KEY[$n]}"; finish; }
  KEYS+=("$(echo "${KEY[$n]}" | jq -r .id)")
  TOKEN[$n]=$(mint "${KEY[$n]}" "viewer-$n") || { fail "mint a viewer token"; finish; }
done
R=$(gql 'mutation($i:SetPlaybackPolicyInput!){setPlaybackPolicy(input:$i){__typename}}' \
  "$(jq -cn --arg s "$SID" '{i:{streamId:$s, policy:{type:"JWT", jwt:{}}}}')")
gql_ok "$R" '.data.setPlaybackPolicy.__typename' Stream || { fail "setPlaybackPolicy: $R"; finish; }
PUB=$(publish_until_admitted "$EDGE_A_RTMP" "$(echo "$S" | jq -r '.streamKey')" 640x360 15 900) ||
  { fail "publisher never admitted"; finish; }

play_location() { curl -s -m 10 -o /dev/null -D - "$FOGHORN_A_URL/play/$PB/hls?jwt=$1" | awk 'tolower($1)=="location:"{print $2}' | tr -d '\r'; }
declare -A VARIANT POLLER EDGE CELL_REPLICAS
# Placement puts each viewer on its own edge; the stream is served from both
# cells' edges. A viewer's refusal, its USER_NEW re-run and its Foghorn calls
# are read on the edge that viewer's session is on.
opened() {
  local n loc host
  for n in 1 2 3; do
    [ -n "${VARIANT[$n]:-}" ] && continue
    loc=$(play_location "${TOKEN[$n]}")
    [ -n "$loc" ] || return 1
    VARIANT[$n]=$(hls_session_url "$loc")
    [ -n "${VARIANT[$n]}" ] || return 1
    host=$(printf '%s' "$loc" | sed -E 's#^[a-z]+://([^/:]+).*#\1#')
    case "$host" in
      *edge-b*) EDGE[$n]=$STACK_EDGE_B_SERVICE CELL_REPLICAS[$n]="foghorn-b foghorn-b-2" ;;
      *) EDGE[$n]=$STACK_EDGE_A_SERVICE CELL_REPLICAS[$n]="foghorn foghorn-2" ;;
    esac
  done
}
eventually 120 "three JWT viewers (one per key) open sessions" opened || finish
# The viewers keep polling for the rest of the scenario, as players do: an
# idle Mist session ends, and a revocation must act on live sessions.
for n in 1 2 3; do
  (hls_poll "${VARIANT[$n]}" >"$STACK_STATE_DIR/revoke-viewer-$n.log") &
  POLLER[$n]=$!
done
trap 'kill "${POLLER[@]}" 2>/dev/null; cleanup' EXIT
playing() { [ "$(hls_poll_state "$STACK_STATE_DIR/revoke-viewer-$1.log" "$2")" = ok ]; }
stopped() { [ "$(hls_poll_state "$STACK_STATE_DIR/revoke-viewer-$1.log" "$2")" = fail ]; }
T0=$(date +%s)
sleep 8
for n in 1 2 3; do
  if playing "$n" "$T0"; then pass "viewer $n plays media"; else
    fail "viewer $n plays media"
    tail -3 "$STACK_STATE_DIR/revoke-viewer-$n.log" | sed 's/^/    poll /'
  fi
done
for n in 1 2 3; do
  echo "    viewer $n on ${EDGE[$n]}; cell replicas ${CELL_REPLICAS[$n]}"
done
granted() { [ -n "$(log_json 10m "select(.msg == \"Playback grant applied\" and .internal_name == \"live+$IN\") | .time" "$1")" ]; }
for edge in $(printf '%s\n' "${EDGE[@]}" | sort -u); do
  check "$edge, serving a viewer, holds the stream's playback grant" granted "$edge"
done

# foghorn_calls <since> <before>: per-viewer triggers any edge serving a viewer
# sent Foghorn before the Docker receive time <before>. The lines' own .time is
# whole seconds, which would count a new session's call made after the refusal
# within the same second.
foghorn_calls() {
  # shellcheck disable=SC2046 # one word per distinct edge service
  log_json_at "$1" "select(.at < \"$2\" and (.msg == \"PLAY_REWRITE resolved by Foghorn\" or .msg == \"USER_NEW approved by Foghorn\" or .msg == \"USER_NEW denied by Foghorn\")) | .msg" $(printf '%s\n' "${EDGE[@]}" | sort -u) | grep -c .
}
CROSSED=0
for n in 1 2; do
  kid=$(echo "${KEY[$n]}" | jq -r .kid)
  log "revoke key $n ($kid)"
  SINCE=$(utc_now) SINCE_S=$(date +%s)
  R=$(gql 'mutation($id:ID!){revokeSigningKey(id:$id){__typename}}' "$(jq -cn --arg id "$(echo "${KEY[$n]}" | jq -r .id)" '{id:$id}')")
  gql_ok "$R" '.data.revokeSigningKey.__typename' SigningKey || { fail "revokeSigningKey: $R"; break; }
  REFUSED_AT=""
  SERVING=${EDGE[$n]} FOGHORNS=(${CELL_REPLICAS[$n]})
  refused_on_edge() {
    REFUSED_AT=$(log_json_at "$SINCE" "select(.msg == \"Playback session refused on the updated grant\" and .kid == \"$kid\") | .at" "$SERVING" | sed -n 1p)
    [ -n "$REFUSED_AT" ]
  }
  eventually 60 "$SERVING (viewer $n's edge) refused key $n's session on the updated grant" refused_on_edge || break
  rerun_refused() { log_has "$SINCE" 'USER_NEW refused: session failed' "$SERVING"; }
  eventually 20 "Mist's re-run USER_NEW for it was answered on $SERVING" rerun_refused
  calls=$(foghorn_calls "$SINCE" "$REFUSED_AT")
  check "the re-check sent no per-viewer trigger to Foghorn until the refusal (got $calls)" [ "$calls" = 0 ]
  gone() { stopped "$n" "$SINCE_S"; }
  eventually 30 "viewer $n (revoked key) no longer gets media" gone
  kill "${POLLER[$n]}" 2>/dev/null
  check "viewer 3 (active key) kept playing through the revocation" playing 3 "$SINCE_S"
  pushes=$(log_json "$SINCE" "select(.msg == \"Playback grant pushed on an authority change\" and .internal_name == \"live+$IN\") | \"\\(.node_id) \\(.authority_apply) \\(.object_authority_version)\"" "${FOGHORNS[@]}" | sort -u)
  printf '%s\n' "$pushes" | awk 'NF { printf "    grant pushed to %s after a %s apply (object v%s)\n", $1, $2, $3 }'
  case "$pushes" in *" peer "*) CROSSED=1 ;; esac
  [ "$CROSSED" = 1 ] && break
done
if [ "$CROSSED" = 1 ]; then
  pass "a revocation applied by the other replica reached the edge through the replicas' announcement"
else
  blocked "both revocations were applied by the replica holding the edge; the cross-replica path was not exercised"
fi
finish
