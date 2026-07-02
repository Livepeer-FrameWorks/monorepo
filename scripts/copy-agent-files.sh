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
        ' "$doc" >> "$TARGET/llms-full.txt"
      done
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
