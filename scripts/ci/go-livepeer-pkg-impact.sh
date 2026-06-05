#!/usr/bin/env bash
set -euo pipefail

base="${1:-}"
head="${2:-HEAD}"

if [[ -z "$base" || "$base" =~ ^0+$ ]]; then
	base="$(git rev-parse "${head}^")"
fi

closure_file="$(mktemp)"
changed_file="$(mktemp)"
trap 'rm -f "$closure_file" "$changed_file"' EXIT

module_path="$(cd pkg && go list -m)"
roots=(
	./clients/decklog
	./geoip
)

{
	printf '%s\n' pkg/go.mod pkg/go.sum
	cd pkg
	go list -deps "${roots[@]}" | while IFS= read -r import_path; do
		case "$import_path" in
			"$module_path")
				printf '%s\n' pkg
				;;
			"$module_path"/*)
				rel="${import_path#"$module_path"/}"
				printf 'pkg/%s\n' "$rel"
				case "$rel" in
					proto/*)
						proto_name="${rel#proto/}"
						if [[ "$proto_name" != */* ]]; then
							printf 'pkg/proto/%s.proto\n' "$proto_name"
						fi
						;;
				esac
				;;
		esac
	done
} | sort -u >"$closure_file"

git diff --name-only "$base" "$head" -- pkg >"$changed_file"

affected=false
matched=()

while IFS= read -r changed; do
	[[ -n "$changed" ]] || continue
	while IFS= read -r root; do
		[[ -n "$root" ]] || continue
		if [[ "$changed" == "$root" || "$changed" == "$root"/* ]]; then
			affected=true
			matched+=("$changed")
			break
		fi
	done <"$closure_file"
done <"$changed_file"

if [[ "${GITHUB_OUTPUT:-}" != "" ]]; then
	{
		printf 'affected=%s\n' "$affected"
		printf 'matched_files<<EOF\n'
		printf '%s\n' "${matched[@]}"
		printf 'EOF\n'
	} >>"$GITHUB_OUTPUT"
fi

if [[ "$affected" == "true" ]]; then
	printf 'go-livepeer monorepo/pkg impact detected:\n'
	printf '  %s\n' "${matched[@]}"
else
	printf 'No go-livepeer-imported monorepo/pkg packages changed.\n'
fi
