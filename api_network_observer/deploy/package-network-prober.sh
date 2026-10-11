#!/usr/bin/env bash
set -euo pipefail

[[ $# == 2 ]] || { echo 'Usage: package-network-prober.sh <binary> <archive.tar.gz>' >&2; exit 2; }
binary=$1
archive=$2
[[ -x $binary ]] || { echo 'Prober binary must be executable' >&2; exit 1; }
binary_dir=$(cd -- "$(dirname -- "$binary")" && pwd)
deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
tar czf "$archive" -C "$binary_dir" "$(basename -- "$binary")" \
  -C "$deploy_dir" install-network-prober.sh network-prober.service network-prober.env.example
