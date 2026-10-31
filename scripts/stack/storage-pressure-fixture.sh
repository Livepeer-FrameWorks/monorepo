#!/usr/bin/env bash
# Age only durable, completed media owned by the isolated run's tenant. A fast
# suite leaves prior scenarios' cache files younger than the cleanup retention.
# Source leases and the retention rule itself remain unchanged.
age_synced_pressure_fixture_cache() {
  local hash hashes
  [ "${STACK_TARGET:-stack}" != staging ] || return 1
  hashes=$(pg "$FOGHORN_A_DB" "SELECT artifact_hash FROM foghorn.artifacts
    WHERE tenant_id='$STACK_TENANT_ID' AND status='completed' AND sync_status='synced'
      AND artifact_type IN ('vod','clip','dvr')") || return 1
  while IFS= read -r hash; do
    [ -n "$hash" ] || continue
    case "$hash" in *[!a-zA-Z0-9_-]*) echo 'unsafe fixture artifact hash' >&2; return 1 ;; esac
    as_helmsman "find $STORE/vod $STORE/clips $STORE/dvr -type f \\( -name '$hash.*' -o -path '*/$hash/*' \\) -exec touch -d @$OLD {} +" || return 1
  done <<<"$hashes"
}
