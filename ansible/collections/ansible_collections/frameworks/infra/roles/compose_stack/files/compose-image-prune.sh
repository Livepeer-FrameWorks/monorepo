#!/bin/sh
# compose-image-prune.sh <project-dir>
#
# Removes a compose stack's superseded images after the stack is up. For each
# image repository the stack declares, the images its containers use and the
# newest other image (the previous release, so a rollback needs no pull) stay;
# every older image of that repository is removed. Docker refuses to remove an
# image a container still uses, so images other stacks run are never removed.
# Prints "removed <reference>" per removed reference.
set -u

project_dir=$1

in_use=$(docker compose --project-directory "$project_dir" images --quiet 2>/dev/null | sed 's/^sha256://' | sort -u)
images=$(docker compose --project-directory "$project_dir" config --images) || exit 1

used() {
  candidate=${1#sha256:}
  for u in $in_use; do
    case $candidate in "$u"*) return 0 ;; esac
  done
  return 1
}

for image in $(printf '%s\n' "$images" | sort -u); do
  ref=${image%@*}
  case ${ref##*/} in
    *:*) repo=${ref%:*} ;;
    *) repo=$ref ;;
  esac
  # Newest first; one line per image ID.
  listing=$(docker image ls --no-trunc --digests --format '{{.ID}} {{.Tag}} {{.Digest}}' "$repo") || continue
  kept_previous=0
  for id in $(printf '%s\n' "$listing" | awk '!seen[$1]++ { print $1 }'); do
    if used "$id"; then
      continue
    fi
    if [ "$kept_previous" -eq 0 ]; then
      kept_previous=1
      continue
    fi
    printf '%s\n' "$listing" | awk -v id="$id" '$1 == id { print $2, $3 }' | while read -r tag digest; do
      if [ "$tag" != "<none>" ]; then
        target="$repo:$tag"
      elif [ "$digest" != "<none>" ]; then
        target="$repo@$digest"
      else
        target=$id
      fi
      if docker image rm "$target" >/dev/null 2>&1; then
        printf 'removed %s\n' "$target"
      fi
    done
  done
done
exit 0
