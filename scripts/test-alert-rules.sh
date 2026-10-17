#!/usr/bin/env bash
# Runs the vmalert-tool unit tests under pkg/grafana/rules/tests against the
# embedded alert rules, with the vmalert-tool from the vmutils release
# config/infrastructure.yaml pins for vmagent. VMALERT_TOOL overrides the
# binary; otherwise the verified download is cached per version.
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
fail() { echo "test-alert-rules: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture $(uname -m)" ;;
esac

# Prints "<version> <url> <checksum>" for the vmagent entry's artifact of this
# platform.
pinned_vmutils() {
  awk -v want="$os-$arch" '
    /^  - name: / { in_entry = ($3 == "vmagent"); next }
    !in_entry { next }
    /^    version:/ { gsub(/"/, "", $2); version = $2 }
    /^      - arch:/ { matched = ($3 == want) }
    matched && /^        url:/ { url = $2 }
    matched && /^        checksum:/ { checksum = $2; matched = 0 }
    END { if (version != "" && url != "" && checksum != "") print version, url, checksum }
  ' "$repo/config/infrastructure.yaml"
}

tool=${VMALERT_TOOL:-}
if [[ -z $tool ]]; then
  read -r version url checksum < <(pinned_vmutils) || fail "config/infrastructure.yaml pins no vmutils for $os-$arch"
  cache=${FRAMEWORKS_TOOL_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/frameworks}/vmutils-$version-$os-$arch
  tool=$cache/vmalert-tool-prod
  if [[ ! -x $tool ]]; then
    mkdir -p "$cache"
    archive=$(mktemp "$cache/vmutils.XXXXXX")
    trap 'rm -f -- "$archive"' EXIT
    curl -fsSL --retry 3 -o "$archive" "$url"
    want=${checksum#sha256:}
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "$archive" | cut -d' ' -f1)
    else
      got=$(shasum -a 256 "$archive" | cut -d' ' -f1)
    fi
    [[ $got == "$want" ]] || fail "vmutils checksum mismatch: got $got, want $want"
    tar -xzf "$archive" -C "$cache" vmalert-tool-prod
  fi
fi

cd "$repo"
shopt -s nullglob
tests=(pkg/grafana/rules/tests/*.yml)
[[ ${#tests[@]} -gt 0 ]] || fail "no rule tests under pkg/grafana/rules/tests"
for test in "${tests[@]}"; do
  "$tool" unittest --files "$test"
done
