#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo 'Usage: install-network-prober.sh --binary FILE --config FILE [--identity FILE] [--no-start] [--root ABSOLUTE_STAGING_DIR]'
}
binary= config= identity= stage_root= no_start=false
deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
while (($#)); do
  case "$1" in
    --binary|--config|--identity|--root)
      (($# >= 2)) || { usage >&2; exit 2; }
      case "$1" in
        --binary) binary=$2 ;; --config) config=$2 ;; --identity) identity=$2 ;; --root) stage_root=$2 ;;
      esac
      shift 2 ;;
    --no-start) no_start=true; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ -n $binary && -n $config ]] || { usage >&2; exit 2; }
if [[ -n $stage_root ]]; then
  [[ $stage_root == /* && $stage_root != / && $no_start == true ]] || { echo 'Staging needs an absolute non-root directory and --no-start.' >&2; exit 2; }
else
  [[ $(id -u) == 0 && $(uname -s) == Linux ]] || { echo 'Live installation requires root on a Linux systemd host.' >&2; exit 1; }
  command -v systemctl >/dev/null
fi
[[ -x $binary && -f $config && -f $deploy_dir/network-prober.service ]] || { echo 'Binary, configuration or service template missing.' >&2; exit 1; }
config_dir="$stage_root/etc/frameworks"
identity_target="$config_dir/network-prober.identity.env"
validation=(check-config --env-file "$config")
if [[ -n $identity ]]; then validation+=(--identity-file "$identity")
elif [[ -f $identity_target ]]; then validation+=(--identity-file "$identity_target")
fi
"$binary" "${validation[@]}"

if [[ -z $stage_root ]]; then
  if ! id network-prober >/dev/null 2>&1; then useradd --system --no-create-home --shell /usr/sbin/nologin network-prober; fi
fi
install -d -m 0755 "$stage_root/usr/local/bin" "$stage_root/etc/systemd/system"
install -d -m 0750 "$config_dir" "$stage_root/var/lib/frameworks-network-prober"
binary_target="$stage_root/usr/local/bin/network-prober"
if [[ $binary -ef $binary_target ]]; then chmod 0755 "$binary_target"; else install -m 0755 "$binary" "$binary_target"; fi
copy_config() {
  local source=$1 target=$2
  if [[ $source -ef $target ]]; then chmod 0600 "$target"; else install -m 0600 "$source" "$target"; fi
}
copy_config "$config" "$config_dir/network-prober.env"
if [[ -n $identity ]]; then copy_config "$identity" "$identity_target"; fi
install -m 0644 "$deploy_dir/network-prober.service" "$stage_root/etc/systemd/system/frameworks-network-prober.service"
if [[ -z $stage_root ]]; then
  chown network-prober:network-prober /var/lib/frameworks-network-prober
  systemctl daemon-reload
  if [[ $no_start == false ]]; then systemctl enable --now frameworks-network-prober.service; fi
fi
echo 'Prober installed. Confirm coordinator readiness and successful_discovery_checks on /health before retiring the old process.'
