#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'staging-media-smoke: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

for command_name in curl ffmpeg jq sops; do
  require_command "$command_name"
done

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || die "run this from a monorepo checkout"
gitops_dir=${FRAMEWORKS_GITOPS_DIR:-"$(dirname "$repo_root")/gitops"}
secrets_file=${STAGING_SECRETS_FILE:-"$gitops_dir/secrets/staging.env"}
bridge_domain=${STAGING_BRIDGE_DOMAIN:-bridge.staging.frameworks.network}
bridge_ip=${STAGING_BRIDGE_IP:-192.168.10.17}
foghorn_domain=${STAGING_FOGHORN_DOMAIN:-foghorn.staging.frameworks.network}
foghorn_ip=${STAGING_FOGHORN_IP:-192.168.10.17}
edge_eu_ip=${STAGING_EDGE_EU_IP:-192.168.10.23}
edge_us_ip=${STAGING_EDGE_US_IP:-192.168.10.24}
operator_email=${STAGING_OPERATOR_EMAIL:-staging@frameworks.network}
publish_seconds=${STAGING_SMOKE_SECONDS:-90}

[[ -f $secrets_file ]] || die "staging secrets file not found: $secrets_file"
[[ $publish_seconds =~ ^[0-9]+$ && $publish_seconds -ge 15 ]] || die "STAGING_SMOKE_SECONDS must be at least 15"

work_dir=$(mktemp -d "${TMPDIR:-/tmp}/frameworks-staging-smoke.XXXXXX")
cookie_jar="$work_dir/cookies"
publisher_log="$work_dir/ffmpeg.log"
stream_id=
publisher_pid=
smoke_passed=false

bridge_curl=(curl --fail-with-body --silent --show-error --connect-timeout 5 --max-time 10 --resolve "$bridge_domain:443:$bridge_ip")
foghorn_curl=(curl --fail-with-body --silent --show-error --connect-timeout 5 --max-time 10 --resolve "$foghorn_domain:443:$foghorn_ip")

graphql() {
  local query=$1 variables=$2
  jq -cn --arg query "$query" --argjson variables "$variables" '{query:$query,variables:$variables}' |
    "${bridge_curl[@]}" --cookie "$cookie_jar" --header 'Content-Type: application/json' \
      --data-binary @- "https://$bridge_domain/graphql"
}

cleanup() {
  local cleanup_response
  if [[ -n $publisher_pid ]]; then
    kill "$publisher_pid" >/dev/null 2>&1 || true
    wait "$publisher_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n $stream_id && -s $cookie_jar ]]; then
    # GraphQL variables are literal here; the shell must not expand $id.
    # shellcheck disable=SC2016
    cleanup_response=$(graphql \
      'mutation($id: ID!){deleteStream(id:$id){__typename}}' \
      "$(jq -cn --arg id "$stream_id" '{id:$id}')" 2>/dev/null || true)
    if [[ $(jq -r '.data.deleteStream.__typename // empty' <<<"$cleanup_response" 2>/dev/null) != DeleteSuccess ]]; then
      printf 'staging-media-smoke: warning: disposable stream cleanup did not confirm success\n' >&2
    fi
  fi
  if [[ $smoke_passed != true && -s $publisher_log ]]; then
    printf 'staging-media-smoke: publisher log follows\n' >&2
    tail -40 "$publisher_log" >&2
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

operator_password=$(sops --decrypt --extract '["PLATFORM_ADMIN_PASSWORD"]' "$secrets_file")
login_payload=$(jq -cn --arg email "$operator_email" --arg password "$operator_password" \
  '{email:$email,password:$password,turnstile_token:"XXXX.DUMMY.TOKEN.XXXX"}')
unset operator_password

printf '[1/5] authenticate staging operator\n'
printf '%s' "$login_payload" | "${bridge_curl[@]}" \
  --cookie-jar "$cookie_jar" \
  --header 'Content-Type: application/json' \
  --data-binary @- \
  "https://$bridge_domain/auth/login" >/dev/null
unset login_payload
[[ -s $cookie_jar ]] || die "staging login returned no session cookies"

printf '[2/5] create disposable stream\n'
stream_name="staging-smoke-$(date -u +%Y%m%dT%H%M%SZ)"
# GraphQL variables are literal here; the shell must not expand $input.
# shellcheck disable=SC2016
create_response=$(graphql \
  'mutation($input: CreateStreamInput!){createStream(input:$input){__typename ... on Stream {streamId playbackId streamKey} ... on ValidationError {message} ... on AuthError {message}}}' \
  "$(jq -cn --arg name "$stream_name" '{input:{name:$name,record:false,ingestMode:"PUSH"}}')")
[[ $(jq -r '.errors | length // 0' <<<"$create_response") == 0 ]] || \
  die "createStream GraphQL error: $(jq -c '.errors' <<<"$create_response")"
[[ $(jq -r '.data.createStream.__typename // empty' <<<"$create_response") == Stream ]] || \
  die "createStream failed: $(jq -c '.data.createStream' <<<"$create_response")"
stream_id=$(jq -r '.data.createStream.streamId // empty' <<<"$create_response")
playback_id=$(jq -r '.data.createStream.playbackId // empty' <<<"$create_response")
stream_key=$(jq -r '.data.createStream.streamKey // empty' <<<"$create_response")
[[ -n $stream_id && -n $playback_id && -n $stream_key ]] || die "createStream returned incomplete identifiers"
unset create_response

printf '[3/5] resolve ingest and publish generated media\n'
publish_url=
ingest_error=
for _attempt in $(seq 1 60); do
  ingest_response=$("${foghorn_curl[@]}" "https://$foghorn_domain/ingest/$stream_key?protocol=rtmp" 2>/dev/null || true)
  publish_url=$(jq -r '.primary.rtmpUrl // empty' <<<"$ingest_response" 2>/dev/null || true)
  ingest_error=$(jq -r '.error // .message // empty' <<<"$ingest_response" 2>/dev/null || true)
  [[ -n $publish_url ]] && break
  sleep 2
done
[[ -n $publish_url ]] || die "Foghorn did not resolve an RTMP destination within 120 seconds${ingest_error:+: $ingest_error}"

publish_without_scheme=${publish_url#*://}
publish_host=${publish_without_scheme%%[:/]*}
case "$publish_host" in
  *staging-media-eu.staging.frameworks.network) publish_ip=$edge_eu_ip ;;
  *staging-media-us.staging.frameworks.network) publish_ip=$edge_us_ip ;;
  *) die "Foghorn returned an unknown staging edge host: $publish_host" ;;
esac
direct_publish_url=${publish_url/"://$publish_host"/"://$publish_ip"}

ffmpeg -hide_banner -loglevel warning -re \
  -f lavfi -i 'testsrc2=size=320x180:rate=15' \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000' \
  -t "$publish_seconds" -c:v libx264 -preset ultrafast -g 30 -pix_fmt yuv420p \
  -c:a aac -f flv "$direct_publish_url" >"$publisher_log" 2>&1 &
publisher_pid=$!

printf '[4/5] resolve playback and wait for HLS\n'
playback_location=
for _attempt in $(seq 1 60); do
  headers_file="$work_dir/playback.headers"
  "${foghorn_curl[@]}" --output /dev/null --dump-header "$headers_file" \
    "https://$foghorn_domain/play/$playback_id/hls" 2>/dev/null || true
  playback_location=$(awk 'tolower($1)=="location:" {print $2}' "$headers_file" | tr -d '\r' | tail -1)
  [[ -n $playback_location ]] && break
  kill -0 "$publisher_pid" >/dev/null 2>&1 || die "publisher stopped before playback resolved; see $publisher_log"
  sleep 2
done
[[ -n $playback_location ]] || die "Foghorn did not resolve playback within 120 seconds"

playback_without_scheme=${playback_location#*://}
playback_host=${playback_without_scheme%%[:/]*}
case "$playback_host" in
  *staging-media-eu.staging.frameworks.network) playback_ip=$edge_eu_ip ;;
  *staging-media-us.staging.frameworks.network) playback_ip=$edge_us_ip ;;
  *) die "Foghorn returned an unknown playback edge host: $playback_host" ;;
esac
playback_curl=(curl --fail-with-body --silent --show-error --connect-timeout 5 --max-time 10 --resolve "$playback_host:443:$playback_ip")

join_url() {
  local base=$1 ref=$2 scheme rest host
  case "$ref" in
    http://*|https://*) printf '%s\n' "$ref" ;;
    /*)
      scheme=${base%%://*}
      rest=${base#*://}
      host=${rest%%/*}
      printf '%s://%s%s\n' "$scheme" "$host" "$ref"
      ;;
    *) printf '%s/%s\n' "${base%/*}" "$ref" ;;
  esac
}

playlist_url=$playback_location
media_playlist=
for _attempt in $(seq 1 45); do
  media_playlist=$("${playback_curl[@]}" "$playlist_url" 2>/dev/null || true)
  if grep -q '^#EXTM3U' <<<"$media_playlist"; then
    break
  fi
  kill -0 "$publisher_pid" >/dev/null 2>&1 || die "publisher stopped before HLS became readable; see $publisher_log"
  sleep 2
done
grep -q '^#EXTM3U' <<<"$media_playlist" || die "edge did not return an HLS playlist within 90 seconds"

media_ref=$(awk 'NF && $1 !~ /^#/ {print; exit}' <<<"$media_playlist" | tr -d '\r')
[[ -n $media_ref ]] || die "HLS playlist contained no media URI"
if [[ $media_ref == *m3u8* ]]; then
  playlist_url=$(join_url "$playlist_url" "$media_ref")
  media_playlist=
  for _attempt in $(seq 1 30); do
    media_playlist=$("${playback_curl[@]}" "$playlist_url" 2>/dev/null || true)
    if grep -q '^#EXTM3U' <<<"$media_playlist"; then
      break
    fi
    kill -0 "$publisher_pid" >/dev/null 2>&1 || die "publisher stopped before the HLS media playlist became readable; see $publisher_log"
    sleep 2
  done
  grep -q '^#EXTM3U' <<<"$media_playlist" || die "edge did not return an HLS media playlist within 60 seconds"
  media_ref=$(awk 'NF && $1 !~ /^#/ {print; exit}' <<<"$media_playlist" | tr -d '\r')
  [[ -n $media_ref ]] || die "HLS media playlist contained no segment URI"
fi

printf '[5/5] fetch a media segment\n'
segment_url=$(join_url "$playlist_url" "$media_ref")
segment_file="$work_dir/segment"
segment_fetched=false
for _attempt in $(seq 1 30); do
  if "${playback_curl[@]}" --output "$segment_file" "$segment_url" 2>/dev/null; then
    segment_fetched=true
    break
  fi
  kill -0 "$publisher_pid" >/dev/null 2>&1 || die "publisher stopped before an HLS segment became readable; see $publisher_log"
  sleep 2
done
[[ $segment_fetched == true && -s $segment_file ]] || die "edge did not return a nonempty successful HLS segment response"
segment_bytes=$(wc -c <"$segment_file" | tr -d ' ')
[[ $segment_bytes -gt 0 ]] || die "HLS segment was empty"

smoke_passed=true
printf 'PASS stream=%s playback=%s publish_edge=%s playback_edge=%s segment_bytes=%s\n' \
  "$stream_id" "$playback_id" "$publish_host" "$playback_host" "$segment_bytes"
