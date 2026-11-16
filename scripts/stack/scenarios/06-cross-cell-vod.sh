#!/usr/bin/env bash
# stack-scenario: default
# staging: safe
# tenant: qa
# A VOD processed in its origin cell plays through every Foghorn replica of
# both cells, first attempt, once it is ready and synced. It is synced together
# with its .dtsh index, so the other cell's edge reads the stored index instead
# of rebuilding it from the media.
# Catches: N8 (only the PeerManager leader knew peer channels: non-leaders
# refused every cross-cell origin with "not authorized"), cross-cell VOD relay,
# rc8 (the index reached storage on the node's next artifact report, up to a
# minute after the media, and a cold read in that window rebuilt it).
. "$(dirname "$0")/../lib.sh"

need ffmpeg jq curl python3 || finish
LOG_SINCE=$(utc_now)
render_mp4 "$STACK_STATE_DIR/vod-xcell.mp4" 640x360 15 12 || { fail "render VOD source"; finish; }
V=$(upload_vod "$STACK_STATE_DIR/vod-xcell.mp4" "stack cross-cell vod")
VID=$(echo "$V" | jq -r '.id // empty')
VHASH=$(echo "$V" | jq -r '.artifactHash // empty')
VPB=$(echo "$V" | jq -r '.playbackId // empty')
[ -n "$VID" ] || { fail "upload: $V"; finish; }

origin_db() {
  local db
  for db in "$FOGHORN_A_DB" "$FOGHORN_B_DB"; do
    if [ -n "$(pg "$db" "SELECT 1 FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$VHASH'")" ]; then
      echo "$db"
      return
    fi
  done
}
ready_synced() {
  local s db
  s=$(vod_status "$VID")
  echo "    status=$s"
  [ "$s" = FAILED ] && return 2
  [ "$s" = READY ] || return 1
  db=$(origin_db)
  [ -n "$db" ] || return 1
  synced_with_index "$db" "$VHASH"
}
eventually 300 "VOD ready and synced with its .dtsh index in its origin cell" ready_synced

origin_a=$(pg "$FOGHORN_A_DB" "SELECT 1 FROM foghorn.artifacts WHERE artifact_hash='$VHASH' AND COALESCE(origin_cluster_id,'') IN ('', '${FOGHORN_A_CLUSTER:-}')")
echo "    origin in cell A: ${origin_a:-no}"
REMOTE_EDGE=$STACK_EDGE_B_SERVICE
[ -n "$origin_a" ] || REMOTE_EDGE=$STACK_EDGE_A_SERVICE

log "first-attempt resolve and media through every replica"
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  code=$(play_status "$base" "$VPB")
  if [ "$code" = 307 ] || [ "$code" = 302 ]; then
    pass "resolve via $base -> $code"
  else
    fail "resolve via $base -> $code $(curl -s -m 10 "$base/play/$VPB/hls" | head -c 200)"
  fi
  check "real TS segment via $base" media_at "$base" "$VPB"
done

signals=$(dtsh_generation_signals "$LOG_SINCE" "$VHASH" "$REMOTE_EDGE")
check "the other cell's edge ($REMOTE_EDGE) read the stored .dtsh of the freshly synced VOD (generation signals: $signals)" [ "$signals" = 0 ]

log "JWT-protected VOD follows /play redirects without client URL rewriting"
VIEWER_KEY_ID=''
cleanup_viewer_policy() {
  gql 'mutation($i:SetPlaybackPolicyInput!){setPlaybackPolicy(input:$i){__typename}}' \
    "$(jq -cn --arg v "$VID" '{i:{vodAssetId:$v,policy:{type:"PUBLIC"}}}')" >/dev/null
  if [ -n "$VIEWER_KEY_ID" ]; then
    gql 'mutation($id:ID!){revokeSigningKey(id:$id){__typename}}' \
      "$(jq -cn --arg id "$VIEWER_KEY_ID" '{id:$id}')" >/dev/null
  fi
}
trap cleanup_viewer_policy EXIT
VIEWER_KEY=$(gql 'mutation($i:CreateSigningKeyInput!){createSigningKey(input:$i){__typename ... on CreateSigningKeySuccess{privateKeyPem signingKey{id kid}}}}' \
  "$(jq -cn --arg n "stack-vod-redirect-$(date +%s)" '{i:{name:$n}}')" | \
  jq -c '.data.createSigningKey | {id:.signingKey.id,kid:.signingKey.kid,pem:.privateKeyPem}')
VIEWER_KEY_ID=$(jq -r '.id // empty' <<<"$VIEWER_KEY")
[ -n "$VIEWER_KEY_ID" ] || { fail 'create VOD signing key'; finish; }
POLICY=$(gql 'mutation($i:SetPlaybackPolicyInput!){setPlaybackPolicy(input:$i){__typename}}' \
  "$(jq -cn --arg v "$VID" '{i:{vodAssetId:$v,policy:{type:"JWT",jwt:{}}}}')")
gql_ok "$POLICY" '.data.setPlaybackPolicy.__typename' VodAsset || { fail 'set VOD JWT policy'; finish; }

jwt_vod_denied() {
  [ "$(curl -s -m 10 -o /dev/null -w '%{http_code}' "$1/play/$VPB/hls/index.m3u8$2")" = 403 ]
}
jwt_vod_media() {
  python3 - "$1" "$VPB" "$VIEWER_KEY" <<'PY'
import base64, json, sys, time, urllib.parse, urllib.request
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature
key = json.loads(sys.argv[3])
b64 = lambda raw: base64.urlsafe_b64encode(raw).rstrip(b"=").decode()
head = b64(json.dumps({"alg": "ES256", "typ": "JWT", "kid": key["kid"]}).encode())
body = b64(json.dumps({"sub": "vod-redirect-viewer", "exp": int(time.time()) + 3600}).encode())
private = serialization.load_pem_private_key(key["pem"].encode(), password=None)
r, s = decode_dss_signature(private.sign(f"{head}.{body}".encode(), ec.ECDSA(hashes.SHA256())))
token = f"{head}.{body}.{b64(r.to_bytes(32, 'big') + s.to_bytes(32, 'big'))}"
class Redirect(urllib.request.HTTPRedirectHandler):
    seen = False
    def redirect_request(self, request, fp, code, message, headers, newurl):
        if not self.seen:
            query = urllib.parse.parse_qs(urllib.parse.urlsplit(newurl).query)
            assert code == 307, "VOD did not issue a 307"
            assert query.get("fwjwt") == [token], "redirect lost the VOD playback credential"
            assert query.get("tkn") and "jwt" not in query, "redirect lost the distinct playback session"
            self.seen = True
        return super().redirect_request(request, fp, code, message, headers, newurl)
redirect = Redirect()
opener = urllib.request.build_opener(redirect)
url = f"{sys.argv[1]}/play/{sys.argv[2]}/hls/index.m3u8?" + urllib.parse.urlencode({"jwt": token})
try:
    for _ in range(4):
        with opener.open(url, timeout=10) as response:
            data, effective = response.read(), response.geturl()
        if data.startswith(b"#EXTM3U"):
            assert b"#EXT-X-ERROR" not in data, "edge refused the protected VOD"
            entries = [line.strip() for line in data.decode().splitlines() if line.strip() and not line.startswith("#")]
            assert entries, "protected VOD playlist has no media"
            url = urllib.parse.urljoin(effective, entries[0])
            continue
        assert redirect.seen and len(data) >= 3 * 188 and all(data[i] == 0x47 for i in (0, 188, 376)), "VOD did not serve transport-stream media"
        sys.exit(0)
    raise AssertionError("VOD exceeded playlist traversal limit")
except Exception as error:
    # urllib exceptions can include the credential-bearing request URL.
    detail = str(error) if isinstance(error, AssertionError) else type(error).__name__
    print(f"protected VOD playback failed: {detail}")
    sys.exit(1)
PY
}
for base in $FOGHORN_A_URLS $FOGHORN_B_URLS; do
  eventually 60 "VOD without JWT is refused through $base" jwt_vod_denied "$base" ''
  check "VOD with invalid JWT is refused through $base" jwt_vod_denied "$base" '?jwt=invalid-viewer-token'
  eventually 60 "JWT VOD redirect serves real HLS media through $base" jwt_vod_media "$base"
done
finish
