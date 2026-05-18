#!/usr/bin/env bash
set -euo pipefail

required_files=(
  "infrastructure/demo-recordings/clips/demo_live_stream_001/a1b2c3d4e5f6789012345678901234ab.mp4"
  "infrastructure/demo-recordings/vod/c3d4e5f678901234567890123456abcd.webm"
  "infrastructure/demo-recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba/fedcba98765432109876543210fedcba.m3u8"
  "infrastructure/demo-recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba/segments/segment_0.ts"
  "infrastructure/demo-recordings/dvr/5eedfeed-11fe-ca57-feed-11feca570001/fedcba98765432109876543210fedcba/segments/segment_1.ts"
)

restored=0

restore_from_head() {
  local path="$1"
  mkdir -p "$(dirname "$path")"
  if git cat-file -e "HEAD:$path" 2>/dev/null; then
    git show "HEAD:$path" > "$path"
    git add "$path"
    restored=1
    echo "restored demo recording fixture: $path"
    return 0
  fi
  echo "ERROR: missing demo recording fixture and HEAD has no copy: $path" >&2
  return 1
}

for path in "${required_files[@]}"; do
  staged_status="$(git diff --cached --name-status -- "$path" || true)"
  if [[ "$staged_status" == D* ]] || [[ ! -f "$path" ]]; then
    restore_from_head "$path"
  elif ! git ls-files --error-unmatch "$path" >/dev/null 2>&1; then
    git add "$path"
    restored=1
    echo "staged demo recording fixture: $path"
  fi
done

if [[ "$restored" -eq 1 ]]; then
  echo "Demo recording fixtures were restored/staged. Review the commit and run git commit again."
  exit 1
fi
