#!/usr/bin/env bash
#
# Copies agent discovery files from docs/skills/ into a web app's static directory.
# Used by prebuild hooks so bare-metal builds get the same files as Docker builds.
#
# Usage: ./scripts/copy-agent-files.sh <static_dir>
#   e.g. ./scripts/copy-agent-files.sh static    (website_application)
#        ./scripts/copy-agent-files.sh public    (website_marketing, website_docs)

set -euo pipefail

TARGET="${1:?Usage: $0 <static_dir>}"

# resolve paths relative to the monorepo root
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
SKILLS_DIR="$REPO_ROOT/docs/skills"

# if called from a subdirectory (package.json prebuild), TARGET is relative to cwd
if [[ ! "$TARGET" = /* ]]; then
  TARGET="$(pwd)/$TARGET"
fi

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

# .well-known discovery files
cp "$SKILLS_DIR/mcp.json"                       "$TARGET/.well-known/mcp.json"
cp "$SKILLS_DIR/security.txt"                   "$TARGET/.well-known/security.txt"
cp "$SKILLS_DIR/did.json"                       "$TARGET/.well-known/did.json"
cp "$SKILLS_DIR/oauth-protected-resource.json"  "$TARGET/.well-known/oauth-protected-resource.json"

echo "agent files copied to $TARGET"
