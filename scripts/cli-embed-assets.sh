#!/usr/bin/env bash
# Stages the runtime assets the CLI embeds (cli/internal/runtimeassets) from
# the monorepo tree into cli/internal/runtimeassets/bundle/. The cli Go module
# cannot //go:embed paths outside its own directory, so every CLI build that
# ships (make build-bin-cli, release.yml build-cli*) runs this first.
#
# Staged layout:
#   bundle/ansible/{ansible.cfg,requirements.yml,playbooks/**,collections/ansible_collections/frameworks/**}
#   bundle/skipper/{sitemaps,faq}/**
#
# Third-party collections and Galaxy roles are not staged: the CLI installs
# them from requirements.yml into the per-user cache at runtime.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dest="${repo_root}/cli/internal/runtimeassets/bundle"

src_ansible="${repo_root}/ansible"
src_skipper="${repo_root}/config/skipper"

for required in \
  "${src_ansible}/ansible.cfg" \
  "${src_ansible}/requirements.yml" \
  "${src_ansible}/playbooks" \
  "${src_ansible}/collections/ansible_collections/frameworks" \
  "${src_skipper}/sitemaps" \
  "${src_skipper}/faq"; do
  if [[ ! -e "$required" ]]; then
    echo "cli-embed-assets: missing source $required" >&2
    exit 1
  fi
done

mkdir -p "$dest"
stage="$(mktemp -d "${dest}/.stage.XXXXXX")"
trap 'rm -rf "$stage"' EXIT

mkdir -p "${stage}/ansible/collections/ansible_collections" "${stage}/skipper"
cp "${src_ansible}/ansible.cfg" "${src_ansible}/requirements.yml" "${stage}/ansible/"

# Molecule scenarios are role test harnesses, not runtime content.
copy_tree() {
  local from_parent="$1" name="$2" to_parent="$3"
  tar -C "$from_parent" \
    --exclude='molecule' --exclude='__pycache__' --exclude='*.pyc' --exclude='.DS_Store' \
    -cf - "$name" | tar -C "$to_parent" -xf -
}
copy_tree "$src_ansible" playbooks "${stage}/ansible"
copy_tree "${src_ansible}/collections/ansible_collections" frameworks "${stage}/ansible/collections/ansible_collections"
copy_tree "$src_skipper" sitemaps "${stage}/skipper"
copy_tree "$src_skipper" faq "${stage}/skipper"

rm -rf "${dest}/ansible" "${dest}/skipper"
mv "${stage}/ansible" "${dest}/ansible"
mv "${stage}/skipper" "${dest}/skipper"
