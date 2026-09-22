#!/usr/bin/env bash
# Rejects explicit database transactions that bypass database.WithRetryablePostgresTx.
#
# Online migrations abort open transactions on colocated YugabyteDB databases, and both
# PostgreSQL and YugabyteDB raise 40001/40P01 under contention, so every transaction must be
# replayable. A transaction that cannot be replayed keeps its Begin call with a comment directly
# above it: `// Raw transaction: <reason>.`, where the reason is a sentence of at least 20
# characters ending in a period.
#
# Matched: database/sql BeginTx(...) and Begin(), pgx Begin(ctx), and BEGIN or START TRANSACTION
# sent as SQL text. A second transaction opened on the pool inside a retry callback cannot be
# detected here; retry callbacks must use only the transaction they are given.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

violations=0
while IFS= read -r file; do
  while IFS=: read -r line _; do
    previous=""
    if [[ "$line" -gt 1 ]]; then
      previous="$(sed -n "$((line - 1))p" "$file")"
    fi
    if ! [[ "$previous" =~ //\ Raw\ transaction:\ .{20,}\.$ ]]; then
      echo "$file:$line: transaction outside database.WithRetryablePostgresTx without a '// Raw transaction: <reason>.' comment" >&2
      violations=$((violations + 1))
    fi
  done < <(grep -nE '\.BeginTx\(|\.Begin\((ctx)?\)|(Exec|Query)(Context)?\([^"]*"[[:space:]]*(BEGIN|START TRANSACTION)' "$file" || true)
done < <(git ls-files --cached --others --exclude-standard -- 'api_*/*.go' 'pkg/*.go' 'cli/*.go' \
  | grep -v '_test\.go$' \
  | grep -v '^pkg/database/retry\.go$')

if [[ "$violations" -gt 0 ]]; then
  echo "verify-db-transactions: $violations unreplayable transaction(s); see docs/standards/schema-migrations.md" >&2
  exit 1
fi
echo "verify-db-transactions: every explicit transaction is replayable or documented"
