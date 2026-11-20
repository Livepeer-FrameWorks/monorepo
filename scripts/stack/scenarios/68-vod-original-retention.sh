#!/usr/bin/env bash
# stack-scenario: default
# tenant: qa
# staging: unsafe, changes the tenant's original-upload policy
# Preserve source bytes, opt out, download privately, and delete only the original.
. "$(dirname "$0")/../lib.sh"
need ffmpeg jq curl python3 grep || finish

set_policy() {
  gql 'mutation($keep:Boolean){setVodOriginalPolicy(keepVodOriginals:$keep){__typename ... on MediaRetentionPolicy{effectiveKeepVodOriginals} ... on ValidationError{message}}}' "$(jq -cn --argjson keep "$1" '{keep:$keep}')"
}
ACTIVATION_EPOCH=
restore_policy() {
  if [ -n "$ACTIVATION_EPOCH" ]; then
    pg "$FOGHORN_A_DB" "UPDATE foghorn.vod_original_retention_activation SET activated_at=to_timestamp($ACTIVATION_EPOCH) WHERE singleton" >/dev/null
  fi
  set_policy null >/dev/null
}
trap restore_policy EXIT
ready() { [ "$(vod_status "$1")" = READY ]; }
synced() { [ "$(pg "$FOGHORN_A_DB" "SELECT sync_status FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$1'")" = synced ]; }
original_size() { pg "$FOGHORN_A_DB" "SELECT COALESCE(v.original_size_bytes,0) FROM foghorn.vod_metadata v JOIN foghorn.artifacts a USING(artifact_hash) WHERE a.tenant_id='$STACK_TENANT_ID' AND a.artifact_hash='$1'"; }
projected_without_original() {
  gql 'query($id:ID!){node(id:$id){... on VodAsset{status originalFile{sizeBytes}}}}' "$(jq -cn --arg id "$1" '{id:$id}')" | jq -e '.data.node.status=="READY" and .data.node.originalFile==null' >/dev/null
}

purge_upload() {
  local id=$1 hash=$2 object=$3 deleted
  deleted=$(gql 'mutation($id:ID!){deleteVodAsset(id:$id){__typename ... on DeleteSuccess{success}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  check 'full VOD deletion accepted' json_has "$deleted" '.data.deleteVodAsset.success==true'
  prefix_empty() { python3 "$STACK_LIB_DIR/s3-prefix-empty.py" "${object%%/*}" "vod/$STACK_TENANT_ID/$hash/"; }
  eventually 300 'full deletion purges the upload prefix' prefix_empty
}

render_mp4 "$STACK_STATE_DIR/original-source.mp4" 1280x720 10 8 || { fail 'render original fixture'; finish; }
ffmpeg -hide_banner -loglevel error -y -i "$STACK_STATE_DIR/original-source.mp4" -c copy "$STACK_STATE_DIR/original-source.mkv" || { fail 'make original MKV'; finish; }
# During activation, upload availability stays independent of original retention.
ACTIVATION_EPOCH=$(pg "$FOGHORN_A_DB" "SELECT EXTRACT(EPOCH FROM activated_at) FROM foghorn.vod_original_retention_activation WHERE singleton")
[[ "$ACTIVATION_EPOCH" =~ ^[0-9]+([.][0-9]+)?$ ]] || { fail 'read retention activation'; ACTIVATION_EPOCH=; finish; }
pg "$FOGHORN_A_DB" "UPDATE foghorn.vod_original_retention_activation SET activated_at=NULL WHERE singleton" >/dev/null || { fail 'disable retention for rollout test'; finish; }
rollout=$(upload_vod "$STACK_STATE_DIR/original-source.mp4" 'stack original during activation' '{"keepOriginal":true}')
rollout_id=$(echo "$rollout" | jq -r '.id // empty')
rollout_hash=$(echo "$rollout" | jq -r '.artifactHash // empty')
check 'pre-postdeploy upload is accepted' test -n "$rollout_id"
if [ -n "$rollout_hash" ]; then
  check 'retention decision is disabled for that upload' test "$(pg "$FOGHORN_A_DB" "SELECT keep_original FROM foghorn.vod_metadata v JOIN foghorn.artifacts a USING(artifact_hash) WHERE a.tenant_id='$STACK_TENANT_ID' AND a.artifact_hash='$rollout_hash'")" = f
fi
pg "$FOGHORN_A_DB" "UPDATE foghorn.vod_original_retention_activation SET activated_at=to_timestamp($ACTIVATION_EPOCH) WHERE singleton" >/dev/null || { fail 'restore retention activation'; finish; }
ACTIVATION_EPOCH=
if [ -n "$rollout_id" ] && eventually 600 'activation upload completes without retrying creation' ready "$rollout_id"; then
  check 'later activation does not retroactively retain the source' test "$(original_size "$rollout_hash")" = 0
  source_url=$(pg "$FOGHORN_A_DB" "SELECT s3_url FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$rollout_hash'")
  purge_upload "$rollout_id" "$rollout_hash" "${source_url#s3://}"
fi

# Exercise the billing query itself, including the retained original's storage obligation.
usage_bytes() {
  local query
  query=$(awk '/^-- name: ListColdStorageUsage / {capture=1;next} capture {print;if (/;$/) exit}' "$STACK_REPO/api_balancing/internal/database/queries/control_storage_usage.sql")
  pg "$FOGHORN_A_DB" "SELECT COALESCE(SUM(total_bytes),0) FROM (${query%;}) billed WHERE tenant_id='$STACK_TENANT_ID' AND artifact_type='vod'"
}
set_policy false | jq -e '.data.setVodOriginalPolicy.effectiveKeepVodOriginals==false' >/dev/null || { fail 'set account opt-out'; finish; }
for entry in 'mkv false' 'mkv true' 'mp4 true'; do
  read -r container keep <<<"$entry"
  source_file="$STACK_STATE_DIR/original-source.$container"
  options='{}'
  [ "$keep" = false ] || options='{"keepOriginal":true}'
  result=$(upload_vod "$source_file" "stack original $container $keep" "$options")
  id=$(echo "$result" | jq -r '.id // empty')
  hash=$(echo "$result" | jq -r '.artifactHash // empty')
  playback=$(echo "$result" | jq -r '.playbackId // empty')
  [ -n "$id" ] || { fail "original upload: $result"; continue; }
  eventually 600 "original=$keep upload ready" ready "$id" || continue
  eventually 180 "original=$keep output synced" synced "$hash" || continue
  size=$(original_size "$hash")
  source_url=$(pg "$FOGHORN_A_DB" "SELECT COALESCE(s3_url,'') FROM foghorn.artifacts WHERE tenant_id='$STACK_TENANT_ID' AND artifact_hash='$hash'")
  object=${source_url#s3://}
  [ "$object" != "$source_url" ] && [ -n "$object" ] || { fail 'resolve upload storage bucket'; continue; }
  if [ "$keep" = false ]; then
    check 'account opt-out keeps no original' [ "$size" = 0 ]
    purge_upload "$id" "$hash" "$object"
    continue
  fi
  check 'upload override retains original bytes' test "${size:-0}" -gt 0
  grant=$(gql 'mutation($id:ID!){createVodOriginalDownload(id:$id){__typename ... on VodOriginalDownload{url sizeBytes filename} ... on ValidationError{message}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  url=$(echo "$grant" | jq -r '.data.createVodOriginalDownload.url // empty')
  [ -n "$url" ] || { fail "original download grant: $grant"; continue; }
  connect=()
  [ -z "${STORAGE_CONNECT_TO:-}" ] || connect=(--connect-to "$STORAGE_CONNECT_TO")
  curl -fsS -m 120 "${connect[@]}" -D "$STACK_STATE_DIR/original-headers" -o "$STACK_STATE_DIR/original-downloaded.mkv" "$url" || { fail 'download original'; continue; }
  check 'original downloaded byte-identically' python3 - "$source_file" "$STACK_STATE_DIR/original-downloaded.mkv" <<'PY'
import hashlib,sys
assert hashlib.sha256(open(sys.argv[1],'rb').read()).digest()==hashlib.sha256(open(sys.argv[2],'rb').read()).digest()
PY
  check 'original is an attachment' grep -qi '^content-disposition: attachment' "$STACK_STATE_DIR/original-headers"
  check 'original download burst is limited' python3 - "$BRIDGE_URL/graphql" "$STACK_API_TOKEN" "$id" <<'PYRATE'
import concurrent.futures,json,sys,urllib.request
endpoint,token,asset=sys.argv[1:]
body=json.dumps({"query":"mutation($id:ID!){createVodOriginalDownload(id:$id){__typename ... on ValidationError{message}}}","variables":{"id":asset}}).encode()
def grant(_):
    request=urllib.request.Request(endpoint,data=body,headers={"Authorization":"Bearer "+token,"Content-Type":"application/json"})
    with urllib.request.urlopen(request,timeout=30) as response:
        return json.load(response)
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    results=list(pool.map(grant,range(8)))
assert any("Too many original download requests" in (result.get("data",{}).get("createVodOriginalDownload") or {}).get("message","") for result in results),results
PYRATE
  usage_before=$(usage_bytes)
  deleted=$(gql 'mutation($id:ID!){deleteVodOriginal(id:$id){__typename ... on DeleteSuccess{success} ... on ValidationError{message}}}' "$(jq -cn --arg id "$id" '{id:$id}')")
  check 'original-only deletion accepted' json_has "$deleted" '.data.deleteVodOriginal.success==true'
  check 'original storage obligation removed' [ "$(original_size "$hash")" = 0 ]
  usage_after=$(usage_bytes)
  check 'storage billing drops exactly the retained original bytes' test "$((usage_before - usage_after))" -eq "$size"
  eventually 60 'catalog clears original, video stays ready' projected_without_original "$id"
  every_replica "processed video remains playable" "$playback" media_at || fail 'processed video plays after original deletion'
  original_removed() { [ "$(curl -sS -m 20 "${connect[@]}" --range 0-0 -o /dev/null -w '%{http_code}' "$url")" = 404 ]; }
  eventually 300 'original-only deletion removes the stored source' original_removed
  purge_upload "$id" "$hash" "$object"
done
finish
