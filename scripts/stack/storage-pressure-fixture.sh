#!/usr/bin/env bash
# Age only durable, playable media owned by the isolated run's tenant. A fast
# suite leaves prior scenarios' cache files younger than the cleanup retention.
# Source leases and the retention rule itself remain unchanged.
age_synced_pressure_fixture_cache() {
  local hash hashes
  [ "${STACK_TARGET:-stack}" != staging ] || return 1
  hashes=$(pg "$FOGHORN_A_DB" "SELECT artifact_hash FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND sync_status='synced'
      AND ((artifact_type IN ('vod','clip') AND status='ready')
        OR (artifact_type='dvr' AND status='completed'))") || return 1
  while IFS= read -r hash; do
    [ -n "$hash" ] || continue
    case "$hash" in *[!a-zA-Z0-9_-]*) echo 'unsafe fixture artifact hash' >&2; return 1 ;; esac
    as_helmsman "find $STORE/vod $STORE/clips $STORE/dvr -type f \\( -name '$hash.*' -o -path '*/$hash/*' \\) -exec touch -d @$OLD {} +" || return 1
  done <<<"$hashes"
}

# A different artifact reaching the target cannot prove the recording's S3-prefix
# check. Require the control-plane verdict for this recording itself.
pressure_dvr_durability_verified() {
  local since=$1 hash=$2 verdict filter
  case "$hash" in ''|*[!a-zA-Z0-9_-]*) return 1 ;; esac
  filter="select(.asset_hash == \"$hash\" and "
  filter+='((.msg // "") | startswith("Asset durable copy verified, safe to delete local copy"))) | .asset_hash'
  verdict=$(log_json "$since" "$filter" foghorn foghorn-2) || return 1
  [ -n "$verdict" ]
}
