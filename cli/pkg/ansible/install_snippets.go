package ansible

// EnsureCurlInstallSnippet installs curl if not already on PATH. Always run
// before any tarball-fetching install path.
const EnsureCurlInstallSnippet = `
if ! command -v curl >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    apt-get -o DPkg::Lock::Timeout=300 update
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y curl ca-certificates
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y curl
  elif command -v yum >/dev/null 2>&1; then
    yum install -y curl
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Syu --noconfirm --needed curl
  else
    echo "unsupported package manager" >&2
    exit 1
  fi
fi
`

// EnsureJavaRuntimeInstallSnippet ensures a Java runtime >= 11 is active.
// Skips when one is already on PATH. On Arch (detected via archlinux-java),
// scans /usr/lib/jvm for installed providers directly and emits a targeted
// activation hint if a dormant compatible provider is found — otherwise
// falls through to install.
const EnsureJavaRuntimeInstallSnippet = `
have_compatible_java=0
if command -v java >/dev/null 2>&1; then
  ver_raw="$(java -version 2>&1 | head -n1 | sed -n 's/.*"\([0-9._]*\)".*/\1/p')"
  case "$ver_raw" in
    1.*) java_major="$(printf '%s' "$ver_raw" | cut -d. -f2)" ;;
    *)   java_major="$(printf '%s' "$ver_raw" | cut -d. -f1)" ;;
  esac
  if [ -n "$java_major" ] && [ "$java_major" -ge 11 ] 2>/dev/null; then
    have_compatible_java=1
  fi
fi

if [ "$have_compatible_java" -eq 1 ]; then
  :
else
  if command -v archlinux-java >/dev/null 2>&1; then
    compatible_provider=""
    for prov_dir in /usr/lib/jvm/*; do
      [ -d "$prov_dir" ] && [ ! -L "$prov_dir" ] || continue
      prov_java="$prov_dir/bin/java"
      [ -x "$prov_java" ] || continue
      prov_ver_raw="$("$prov_java" -version 2>&1 | head -n1 | sed -n 's/.*"\([0-9._]*\)".*/\1/p')"
      case "$prov_ver_raw" in
        1.*) prov_major="$(printf '%s' "$prov_ver_raw" | cut -d. -f2)" ;;
        *)   prov_major="$(printf '%s' "$prov_ver_raw" | cut -d. -f1)" ;;
      esac
      if [ -n "$prov_major" ] && [ "$prov_major" -ge 11 ] 2>/dev/null; then
        compatible_provider="${prov_dir#/usr/lib/jvm/}"
        break
      fi
    done
    if [ -n "$compatible_provider" ]; then
      echo "A compatible Java runtime is installed but not active: $compatible_provider" >&2
      echo "Run 'archlinux-java set $compatible_provider' to activate it, then retry." >&2
      exit 1
    fi
  fi
  if command -v apt-get >/dev/null 2>&1; then
    apt-get -o DPkg::Lock::Timeout=300 update
    DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=300 install -y default-jre-headless
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y java-17-openjdk-headless
  elif command -v yum >/dev/null 2>&1; then
    yum install -y java-17-openjdk-headless
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Syu --noconfirm --needed jre-openjdk-headless
  else
    echo "unsupported package manager" >&2
    exit 1
  fi
fi
`

// SafeTarballExtractSnippet defines extract_tarball_to(archive, dest), a
// shell helper that extracts archive to a mktemp dir, asserts it contains
// exactly one top-level directory entry, and moves that entry to dest.
// Callers splice this once before any extract_tarball_to call.
const SafeTarballExtractSnippet = `
extract_tarball_to() {
  local archive="$1" dest="$2"
  local tmpdir count inner
  tmpdir="$(mktemp -d)"
  tar -xf "$archive" -C "$tmpdir"
  count=$(find "$tmpdir" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')
  if [ "$count" != "1" ]; then
    echo "unexpected tar layout in $archive: expected 1 top-level entry, got $count" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  inner="$(find "$tmpdir" -mindepth 1 -maxdepth 1)"
  if [ ! -d "$inner" ]; then
    echo "unexpected tar layout in $archive: top-level entry is not a directory" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  rm -rf "$dest"
  mv "$inner" "$dest"
  rmdir "$tmpdir" 2>/dev/null || true
}
`

// TimeSyncInstallSnippet ensures a time-sync daemon is active. No-op when
// any of chronyd, chrony, ntpd, ntp, or systemd-timesyncd is already active.
const TimeSyncInstallSnippet = `
if systemctl is-active --quiet chronyd 2>/dev/null \
   || systemctl is-active --quiet chrony 2>/dev/null \
   || systemctl is-active --quiet ntpd 2>/dev/null \
   || systemctl is-active --quiet ntp 2>/dev/null \
   || systemctl is-active --quiet systemd-timesyncd 2>/dev/null; then
  :
else
  if command -v apt-get >/dev/null; then
    apt-get -o DPkg::Lock::Timeout=300 update -qq
    apt-get -o DPkg::Lock::Timeout=300 install -y -qq chrony
  elif command -v dnf >/dev/null; then
    dnf install -y -q chrony
  elif command -v yum >/dev/null; then
    yum install -y -q chrony
  elif command -v pacman >/dev/null; then
    pacman -Syu --noconfirm --needed chrony
  fi
  systemctl enable --now chronyd 2>/dev/null || systemctl enable --now chrony 2>/dev/null || true
fi
`
