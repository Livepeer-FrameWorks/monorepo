#!/bin/sh
#
# Copies agent discovery files from docs/skills/ into a web app's static directory.
# Used by prebuild hooks so bare-metal builds get the same files as Docker builds.
#
# Usage: ./scripts/copy-agent-files.sh <static_dir>
#   e.g. ./scripts/copy-agent-files.sh static    (website_application)
#        ./scripts/copy-agent-files.sh public    (website_marketing, website_docs)

set -eu

TARGET="${1:?Usage: $0 <static_dir>}"

# resolve paths relative to the monorepo root
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SKILLS_DIR="$REPO_ROOT/docs/skills"

# if called from a subdirectory (package.json prebuild), TARGET is relative to cwd
case "$TARGET" in
  /*) ;;
  *) TARGET="$(pwd)/$TARGET" ;;
esac

if [ ! -d "$SKILLS_DIR" ]; then
  echo "warn: $SKILLS_DIR not found, skipping agent file copy" >&2
  exit 0
fi

SITE_CONFIG="$REPO_ROOT/packages/site-config/index.ts"

# Reads `export const NAME = "value";` from the shared site config.
site_const() {
  sed -n "s/^export const $1 = \"\(.*\)\";$/\1/p" "$SITE_CONFIG"
}

# Prints a sed script that replaces the docs' %TOKEN% placeholders with the
# values website_docs/astro.config.mjs substitutes into the rendered pages, so
# llms-full.txt carries the same URLs as the HTML. Unset VITE_* values fall back
# to the same defaults as that config; the app and marketing origins fall back
# to the public sites.
docs_url_tokens() {
  gateway="${VITE_GATEWAY_URL:-http://localhost:18000}"
  gateway_ws="${VITE_GRAPHQL_WS_URL:-$(printf '%s' "${gateway%/}" | sed 's/^http/ws/')/graphql/ws}"
  ingest="${VITE_STREAMING_INGEST_URL:-http://localhost:8080}"
  edge="${VITE_STREAMING_EDGE_URL:-http://localhost:8080}"
  play="${VITE_STREAMING_PLAY_URL:-http://localhost:18008}"
  app="${VITE_APP_URL:-$(site_const APP_SITE_URL)}"
  marketing="${VITE_MARKETING_SITE_URL:-$(site_const MARKETING_SITE_URL)}"

  ingest_scheme="${ingest%%://*}"
  ingest_hostport="${ingest#*://}"
  ingest_hostport="${ingest_hostport%%/*}"
  ingest_host="${ingest_hostport%%:*}"
  ingest_port_part=""
  case "$ingest_hostport" in *:*) ingest_port_part=":${ingest_hostport#*:}" ;; esac

  edge_scheme="${edge%%://*}"
  edge_hostport="${edge#*://}"
  edge_hostport="${edge_hostport%%/*}"
  edge_host="${edge_hostport%%:*}"
  edge_port_part=""
  case "$edge_hostport" in *:*) edge_port_part=":${edge_hostport#*:}" ;; esac

  rtmp_port="$(site_const STREAMING_RTMP_PORT)"
  srt_port="$(site_const STREAMING_SRT_PORT)"
  rtmp_path="$(site_const STREAMING_RTMP_PATH)"
  hls_path="$(site_const STREAMING_HLS_PATH)"
  webrtc_path="$(site_const STREAMING_WEBRTC_PATH)"

  for pair in \
    "APP_URL|$app" \
    "MARKETING_URL|$marketing" \
    "GATEWAY_URL|$gateway" \
    "GATEWAY_WS_URL|$gateway_ws" \
    "API_URL|$gateway" \
    "RTMP_URL|rtmp://$ingest_host:$rtmp_port$rtmp_path" \
    "SRT_HOST|$ingest_host:$srt_port" \
    "WHIP_URL|$ingest_scheme://$ingest_host$ingest_port_part$webrtc_path" \
    "EDGE_URL|$edge_scheme://$edge_host$edge_port_part" \
    "PLAY_URL|$play" \
    "INGEST_URL|$ingest" \
    "HLS_PATH|$hls_path" \
    "WEBRTC_PATH|$webrtc_path" \
    "INGEST_HOSTNAME|$ingest_host" \
    "SRT_PORT|$srt_port" \
    "DISCORD_URL|$(site_const DISCORD_URL)" \
    "GITHUB_URL|$(site_const GITHUB_URL)"; do
    printf 's|%%%s%%|%s|g;' "${pair%%|*}" "${pair#*|}"
  done
}

mkdir -p "$TARGET/.well-known"

# root-level files
cp "$SKILLS_DIR/SKILL.md"      "$TARGET/SKILL.md"
cp "$SKILLS_DIR/skill.json"    "$TARGET/skill.json"
cp "$SKILLS_DIR/heartbeat.md"  "$TARGET/heartbeat.md"
cp "$SKILLS_DIR/llms.txt"      "$TARGET/llms.txt"
cp "$SKILLS_DIR/robots.txt"    "$TARGET/robots.txt"

# Append the per-site sitemap, derived from the resolved $TARGET path (already made
# absolute above) so this is correct whether invoked from a package prebuild hook
# (cwd = the site dir) or from the repo root with an explicit target path.
case "$TARGET" in
  */website_marketing/*)
    printf '\nSitemap: https://frameworks.network/sitemap.xml\n' >> "$TARGET/robots.txt"
    ;;
  */website_docs/*)
    printf '\nSitemap: https://logbook.frameworks.network/sitemap-index.xml\n' >> "$TARGET/robots.txt"
    DOCS_URL_TOKENS="$(docs_url_tokens)"

    # Full-content companion to llms.txt (docs site only): the whole docs corpus
    # in one fetch for AI agents, frontmatter stripped, one URL header per page.
    DOCS_CONTENT="$REPO_ROOT/website_docs/src/content/docs"
    if [ -d "$DOCS_CONTENT" ]; then
      printf '# FrameWorks full documentation\n\n> Every page from https://logbook.frameworks.network/ concatenated. See llms.txt for the curated index.\n' > "$TARGET/llms-full.txt"
      find "$DOCS_CONTENT" -type f \( -name '*.md' -o -name '*.mdx' \) | LC_ALL=C sort | while read -r doc; do
        if grep -q '^draft: true' "$doc"; then continue; fi
        rel="${doc#"$DOCS_CONTENT"/}"
        rel="${rel%.mdx}"
        rel="${rel%.md}"
        rel="${rel%index}"
        # Emit "# <title>" + canonical URL per page, drop frontmatter, and drop
        # leading MDX component imports (imports inside code fences come after
        # real content, so they are preserved).
        awk -v url="https://logbook.frameworks.network/$rel" '
          NR==1 && /^---[[:space:]]*$/ { fm=1; next }
          fm && /^title:/ { t=$0; sub(/^title:[[:space:]]*/, "", t); gsub(/^"|"$/, "", t); title=t; next }
          fm && /^---[[:space:]]*$/ {
            fm=0
            if (title != "") printf("\n\n---\n# %s\n%s\n\n", title, url)
            else printf("\n\n---\n# %s\n\n", url)
            next
          }
          fm { next }
          !body && (/^[[:space:]]*$/ || /^import /) { next }
          { body=1; print }
        ' "$doc" | sed -e "$DOCS_URL_TOKENS" >> "$TARGET/llms-full.txt"
      done
      # A placeholder added to astro.config.mjs but not to docs_url_tokens
      # would ship raw; fail the build instead.
      leftover="$(grep -oE '%[A-Z][A-Z_]*_(URL|HOST|HOSTNAME|PORT|PATH)%' "$TARGET/llms-full.txt" | sort -u | tr '\n' ' ')"
      if [ -n "$leftover" ]; then
        echo "error: llms-full.txt has unsubstituted placeholders: $leftover" >&2
        exit 1
      fi
    fi
    ;;
  */website_application/*)
    printf '\nSitemap: https://app.frameworks.network/sitemap.xml\n' >> "$TARGET/robots.txt"
    ;;
esac

# .well-known discovery files
cp "$SKILLS_DIR/mcp.json"                       "$TARGET/.well-known/mcp.json"
cp "$SKILLS_DIR/security.txt"                   "$TARGET/.well-known/security.txt"
cp "$SKILLS_DIR/did.json"                       "$TARGET/.well-known/did.json"
cp "$SKILLS_DIR/oauth-protected-resource.json"  "$TARGET/.well-known/oauth-protected-resource.json"

echo "agent files copied to $TARGET"
