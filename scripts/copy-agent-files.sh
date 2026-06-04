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
