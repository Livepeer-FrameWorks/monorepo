#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/remote-dev.sh <doctor|sync|run|shell|path|ssh-config> [slot] [command...]

Environment:
  FRAMEWORKS_GITOPS_DIR  GitOps checkout (default: sibling ../gitops)
  REMOTE_DEV_HOST        Override the SOPS-managed host address
  REMOTE_DEV_PORT        Override the SSH port
  REMOTE_DEV_USER        Remote Unix account (default: local username)
  REMOTE_DEV_SLOT        Slot used when the positional slot is omitted
  REMOTE_DEV_REPO_URL    Bootstrap clone URL
EOF
}

die() {
  printf 'remote-dev: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || die "run this from a monorepo checkout"
gitops_dir=${FRAMEWORKS_GITOPS_DIR:-"$(dirname "$repo_root")/gitops"}
inventory="$gitops_dir/development/hosts.enc.yaml"
remote_user=${REMOTE_DEV_USER:-$(id -un)}

resolve_endpoint() {
  if [[ -z ${REMOTE_DEV_HOST:-} || -z ${REMOTE_DEV_PORT:-} ]]; then
    require_command sops
    [[ -f $inventory ]] || die "encrypted remote-dev inventory not found: $inventory"
  fi
  remote_host=${REMOTE_DEV_HOST:-$(sops --decrypt --extract '["hosts"]["remotedev"]["address"]' "$inventory")}
  remote_port=${REMOTE_DEV_PORT:-$(sops --decrypt --extract '["hosts"]["remotedev"]["port"]' "$inventory")}
  [[ -n $remote_host && $remote_port =~ ^[0-9]+$ ]] || die "invalid remote-dev endpoint"
  remote_target="$remote_user@$remote_host"
  # A pre-existing multiplexed master can retain stale supplementary groups
  # after onboarding. Dedicated sessions also keep concurrent agent slots from
  # sharing connection-level failure state.
  ssh_args=(-o BatchMode=yes -o ConnectTimeout=8 -o ControlMaster=no -o ControlPath=none -p "$remote_port")
}

resolve_slot() {
  slot=${1:-${REMOTE_DEV_SLOT:-}}
  [[ -n $slot ]] || die "a slot name is required"
  [[ $slot =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "slot must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}"
  remote_repo="/srv/frameworks-dev/workspaces/$remote_user/$slot/monorepo"
}

remote_environment() {
  cat <<EOF
export GOCACHE=/srv/frameworks-dev/cache/$remote_user/go-build
export GOMODCACHE=/srv/frameworks-dev/cache/$remote_user/go-mod
export PNPM_HOME=/srv/frameworks-dev/cache/$remote_user/pnpm-home
export PNPM_STORE_DIR=/srv/frameworks-dev/cache/$remote_user/pnpm-store
export CARGO_HOME=/srv/frameworks-dev/cache/$remote_user/cargo
export PATH="\$PNPM_HOME:\$HOME/go/bin:\$CARGO_HOME/bin:\$PATH"
mkdir -p "\$GOCACHE" "\$GOMODCACHE" "\$PNPM_HOME" "\$PNPM_STORE_DIR" "\$CARGO_HOME"
EOF
}

initialize_slot() {
  local head repo_url init_script
  head=$(git -C "$repo_root" rev-parse HEAD)
  repo_url=${REMOTE_DEV_REPO_URL:-https://github.com/Livepeer-FrameWorks/monorepo.git}
  init_script="set -e; mkdir -p $(printf '%q' "$(dirname "$remote_repo")"); if [[ ! -d $(printf '%q' "$remote_repo/.git") ]]; then git clone --filter=blob:none --no-checkout $(printf '%q' "$repo_url") $(printf '%q' "$remote_repo"); fi; cd $(printf '%q' "$remote_repo"); if git fetch --depth=1 origin $(printf '%q' "$head") >/dev/null 2>&1; then git checkout --detach --force $(printf '%q' "$head"); else printf '%s\\n' 'warning: local HEAD is not on origin; using origin/HEAD as Git baseline before mirroring files' >&2; git checkout --detach --force origin/HEAD; fi"
  ssh "${ssh_args[@]}" "$remote_target" "bash -lc $(printf '%q' "$init_script")"
}

action=${1:-}
[[ -n $action ]] || {
  usage
  exit 2
}
shift
if [[ $action == -h || $action == --help || $action == help ]]; then
  usage
  exit 0
fi
resolve_endpoint

case "$action" in
  doctor)
    require_command ssh
    require_command rsync
    ssh "${ssh_args[@]}" "$remote_target" "bash -lc 'set -e; test -d /srv/frameworks-dev/workspaces; test -w /srv/frameworks-dev/workspaces/$remote_user; test -w /srv/frameworks-dev/cache; command -v git docker flock rsync go pnpm >/dev/null; docker info >/dev/null; printf \"host=%s\\nuser=%s\\ncpus=%s\\nworkspace=%s\\n\" \"\$(hostnamectl --static)\" \"\$(id -un)\" \"\$(nproc)\" /srv/frameworks-dev/workspaces/$remote_user'"
    ;;
  sync)
    require_command rsync
    resolve_slot "${1:-}"
    initialize_slot
    exclude_file=$(mktemp "${TMPDIR:-/tmp}/frameworks-remote-dev-excludes.XXXXXX")
    trap 'rm -f "$exclude_file"' EXIT
    git -C "$repo_root" ls-files --others --ignored --exclude-standard --directory >"$exclude_file"
    rsync --archive --compress --delete \
      --exclude-from="$exclude_file" \
      --exclude='.git/' \
      --exclude='config/env/secrets.env' \
      --exclude='*.agekey' \
      -e "ssh -o BatchMode=yes -o ConnectTimeout=8 -o ControlMaster=no -o ControlPath=none -p $remote_port" \
      "$repo_root/" "$remote_target:$remote_repo/"
    printf 'Synced %s to %s:%s\n' "$repo_root" "$remote_target" "$remote_repo"
    ;;
  run)
    resolve_slot "${1:-}"
    shift || true
    [[ $# -gt 0 ]] || die "run requires a command"
    printf -v quoted_command '%q ' "$@"
    remote_script="set -e; umask 0002; cd $(printf '%q' "$remote_repo"); $(remote_environment); for lock in /srv/frameworks-dev/cache/.heavy-1.lock /srv/frameworks-dev/cache/.heavy-2.lock; do exec {lock_fd}>\"\$lock\"; if flock -n \"\$lock_fd\"; then exec $quoted_command; fi; exec {lock_fd}>&-; done; printf '%s\\n' 'remote-dev: both heavy-job slots are occupied; retry after one finishes' >&2; exit 75"
    ssh "${ssh_args[@]}" "$remote_target" "bash -lc $(printf '%q' "$remote_script")"
    ;;
  shell)
    resolve_slot "${1:-}"
    remote_script="cd $(printf '%q' "$remote_repo"); $(remote_environment); exec \"\$SHELL\" -l"
    ssh -t "${ssh_args[@]}" "$remote_target" "bash -lc $(printf '%q' "$remote_script")"
    ;;
  path)
    resolve_slot "${1:-}"
    printf '%s:%s\n' "$remote_target" "$remote_repo"
    ;;
  ssh-config)
    cat <<EOF
Host frameworks-remotedev
  HostName $remote_host
  Port $remote_port
  User $remote_user
  IdentitiesOnly yes
EOF
    ;;
  -h|--help|help)
    usage
    ;;
  *)
    usage >&2
    die "unknown action: $action"
    ;;
esac
