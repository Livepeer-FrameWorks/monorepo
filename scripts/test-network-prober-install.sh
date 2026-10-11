#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
binary="$repo/bin/network-prober"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/frameworks-prober-install.XXXXXX")
trap 'rm -rf -- "$fixture"' EXIT
umask 077

fail() { echo "$*" >&2; exit 1; }
mode() {
  if [[ $(uname -s) == Darwin ]]; then stat -f '%Lp' "$1"; else stat -c '%a' "$1"; fi
}

"$repo/api_network_observer/deploy/package-network-prober.sh" "$binary" "$fixture/prober.tar.gz"
mkdir "$fixture/release"
tar xzf "$fixture/prober.tar.gz" -C "$fixture/release"
installer="$fixture/release/install-network-prober.sh"
[[ -x $installer ]] || fail 'Release archive does not contain an executable installer'
[[ -f $fixture/release/network-prober.service ]] || fail 'Release archive does not contain the systemd unit'
[[ -f $fixture/release/network-prober.env.example ]] || fail 'Release archive does not contain example configuration'
binary="$fixture/release/network-prober"

# Public Ethereum test key; no real wallet or network access is used.
printf '%064d\n' 1 > "$fixture/key"
"$binary" sign-discovery --key-file "$fixture/key" --output "$fixture/identity.env"
cat > "$fixture/prober.env" <<'CONFIG'
NETWORK_OBSERVER_URL=https://observer.example.net
NETWORK_OBSERVER_SECRET=installation-test-secret
NETWORK_PROBER_ID=install-test
NETWORK_PROBER_REGION=eu-west
NETWORK_PROBER_SPOOL_DIR=/var/lib/frameworks-network-prober
CONFIG

"$installer" --binary "$binary" --config "$fixture/prober.env" --identity "$fixture/identity.env" --root "$fixture/root" --no-start
installed="$fixture/root/usr/local/bin/network-prober"
config_dir="$fixture/root/etc/frameworks"
[[ -x $installed ]] || fail 'Installed binary is not executable'
[[ $(mode "$config_dir/network-prober.env") == 600 ]] || fail 'Configuration is not private'
[[ $(mode "$config_dir/network-prober.identity.env") == 600 ]] || fail 'Identity is not private'
[[ $(mode "$fixture/root/var/lib/frameworks-network-prober") == 750 ]] || fail 'State directory has unexpected permissions'
cmp "$fixture/identity.env" "$config_dir/network-prober.identity.env"
cmp "$repo/api_network_observer/deploy/network-prober.service" "$fixture/root/etc/systemd/system/frameworks-network-prober.service"
"$installed" check-config --env-file "$config_dir/network-prober.env" --identity-file "$config_dir/network-prober.identity.env"

# Reconfiguration must reuse the existing identity and allow the installed binary.
"$installer" --binary "$installed" --config "$config_dir/network-prober.env" --root "$fixture/root" --no-start
cmp "$fixture/identity.env" "$config_dir/network-prober.identity.env"

sed 's|https://observer.example.net|http://observer.example.net|' "$fixture/prober.env" > "$fixture/invalid.env"
if "$installer" --binary "$binary" --config "$fixture/invalid.env" --identity "$fixture/identity.env" --root "$fixture/rejected" --no-start > "$fixture/rejected.log" 2>&1; then
  fail 'Installer accepted unverified HTTP coordinator configuration'
fi
[[ ! -e $fixture/rejected ]] || fail 'Invalid configuration modified the installation root'

echo 'External prober installation, identity reuse and configuration rejection verified.'
