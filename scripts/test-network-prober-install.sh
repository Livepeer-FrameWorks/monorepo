#!/usr/bin/env bash
set -euo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
binary="$repo/bin/network-prober"
fixture=$(mktemp -d "${TMPDIR:-/tmp}/frameworks-prober-install.XXXXXX")
trap 'rm -rf -- "$fixture"' EXIT
umask 077

fail() { echo "$*" >&2; exit 1; }
# Permission bits only, on both platforms: a setgid TMPDIR (a shared workspace)
# passes its setgid bit on to every directory the installer creates under it.
mode() {
  if [[ $(uname -s) == Darwin ]]; then stat -f '%Lp' "$1"; else printf '%o\n' $((0$(stat -c '%a' "$1") & 0777)); fi
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
# Operators start from the shipped example: the observer relay on Bridge, under
# its /network-observer path prefix.
sed 's|^NETWORK_OBSERVER_SECRET=.*|NETWORK_OBSERVER_SECRET=installation-test-secret|' \
  "$fixture/release/network-prober.env.example" > "$fixture/prober.env"
grep -qx 'NETWORK_OBSERVER_URL=https://bridge.example.net/network-observer' "$fixture/prober.env" ||
  fail 'Shipped example no longer points the prober at the Bridge observer relay'

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

# An isolated test network may let the prober dial its private orchestrators.
{ cat "$fixture/prober.env"; echo 'NETWORK_PROBER_ALLOWED_PRIVATE_CIDRS=10.231.1.0/24,192.168.50.0/24'; } > "$fixture/private.env"
"$binary" check-config --env-file "$fixture/private.env" --identity-file "$fixture/identity.env" >/dev/null ||
  fail 'Prober rejected valid private orchestrator networks'

reject() { # <description> <config file>
  local root="$fixture/rejected-$RANDOM"
  if "$installer" --binary "$binary" --config "$2" --identity "$fixture/identity.env" --root "$root" --no-start > "$fixture/rejected.log" 2>&1; then
    fail "Installer accepted $1"
  fi
  [[ ! -e $root ]] || fail "Installer modified the installation root for $1"
}
sed 's|https://bridge.example.net|http://bridge.example.net|' "$fixture/prober.env" > "$fixture/insecure.env"
reject 'unverified HTTP coordinator configuration' "$fixture/insecure.env"
sed 's|/network-observer$|/network-observer?token=x|' "$fixture/prober.env" > "$fixture/query.env"
reject 'a coordinator URL carrying a query string' "$fixture/query.env"
{ cat "$fixture/prober.env"; echo 'NETWORK_PROBER_ALLOWED_PRIVATE_CIDRS=10.231.1.0'; } > "$fixture/bad-cidr.env"
reject 'a malformed private orchestrator network' "$fixture/bad-cidr.env"

echo 'External prober installation from the shipped example, identity reuse, private test networks and configuration rejection verified.'
