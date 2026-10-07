#!/bin/sh
# yugabyte-prune-releases.sh <install-dir> <check|apply> [unit-file ...]
#
# Removes YugabyteDB engine trees under <install-dir> that the node no longer
# selects: every releases/<id> other than the one <install-dir>/current points
# at, and the in-place tree (bin, lib, postgres, ... and its .installed-*
# identity) from before release directories. conf, current and releases are
# never touched. A tree stays for a later run while any running process
# executes or maps a file from it (a master or tserver not yet restarted onto
# the selected release, or its postgres children) or a given unit file names it (a
# stopped service whose unit is switched only at its admitted restart).
# Without a resolvable current nothing is removed. An upgrade never returns to
# an older engine, so nothing else needs a superseded tree.
#
# Prints "removed <path>" per removed path, or "would remove <path>" in check.
# PROC_ROOT (default /proc) is where running executables are read from.
set -eu

install_dir=$1
mode=$2
shift 2

[ -L "$install_dir/current" ] || exit 0
selected=$(readlink -f "$install_dir/current") || exit 0
[ -d "$selected" ] || exit 0
install_real=$(readlink -f "$install_dir")
releases="$install_real/releases"

# Every path a running process executes or maps, or a unit file names, resolved.
in_use=$(
  for exe in "${PROC_ROOT:-/proc}"/[0-9]*/exe; do
    readlink -f "$exe" 2>/dev/null || true
  done
  for maps in "${PROC_ROOT:-/proc}"/[0-9]*/maps; do
    grep -o "$install_real/[^[:space:]]*" "$maps" 2>/dev/null || true
  done | sort -u
  for unit in "$@"; do
    [ -f "$unit" ] || continue
    grep -o "$install_dir/[^[:space:]\"']*" "$unit" 2>/dev/null |
      sed "s|^$install_dir/|$install_real/|" || true
  done
)

used_under() {
  for path in $in_use; do
    case $path in "$1"/*) return 0 ;; esac
  done
  return 1
}

prune() {
  if [ "$mode" = apply ]; then
    rm -rf -- "$1"
    printf 'removed %s\n' "$1"
  else
    printf 'would remove %s\n' "$1"
  fi
}

for dir in "$releases"/*; do
  [ -d "$dir" ] || continue
  [ "$(readlink -f "$dir")" = "$selected" ] && continue
  used_under "$(readlink -f "$dir")" && continue
  prune "$install_dir/releases/${dir##*/}"
done

# The in-place tree holds the same top-level names as a release directory.
for path in $in_use; do
  case $path in
    "$releases"/* | "$install_real"/conf/* | "$install_real"/current/*) ;;
    "$install_real"/*) exit 0 ;;
  esac
done
for entry in "$selected"/* "$selected"/.[!.]* "$install_dir"/.installed-*; do
  name=${entry##*/}
  case $name in
    conf | current | releases | '.[!.]*' | '.installed-*' | '*') continue ;;
  esac
  target="$install_dir/$name"
  [ -e "$target" ] || [ -L "$target" ] || continue
  prune "$target"
done
exit 0
