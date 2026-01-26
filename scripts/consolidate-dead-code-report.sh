#!/usr/bin/env bash
set -euo pipefail

# Consolidates dead code analysis reports into a summary markdown file

REPORTS_DIR="${REPORTS_DIR:-reports}"

cat << EOF
# Dead Code Analysis Report

Generated: $(date -u '+%Y-%m-%d %H:%M:%S UTC')

## Overview

This report summarizes potentially dead code found in the FrameWorks monorepo.
All findings should be manually reviewed before removal.

**Categories:**
- **Unreachable**: Functions that cannot be called from any entry point
- **Unused exports**: Exported symbols not used within the codebase
- **Orphaned files**: Files not imported by any other file
- **Unused dependencies**: Dependencies declared but not imported
EOF

error_files=()
for f in "$REPORTS_DIR"/*.txt "$REPORTS_DIR"/*.json; do
    if [[ -f "$f" ]] && grep -q '^# ERROR:' "$f"; then
        error_files+=("$f")
    fi
done

if [[ "${#error_files[@]}" -gt 0 ]]; then
    echo ""
    echo "---"
    echo ""
    echo "## Warnings"
    for f in "${error_files[@]}"; do
        summary=$(grep '^# ERROR:' "$f" | head -n 1 | sed 's/^# ERROR: //')
        echo "- $(basename "$f"): $summary"
    done
fi

cat << EOF

---

## Go Services

### Unreachable Functions (deadcode)

| Service | Dead Functions |
|---------|----------------|
EOF

for f in "$REPORTS_DIR"/deadcode-*.txt; do
    if [[ -f "$f" ]]; then
        service=$(basename "$f" .txt | sed 's/deadcode-//')
        count=$(wc -l < "$f" | tr -d ' ')
        echo "| $service | $count |"
    fi
done

cat << EOF

### Unused Identifiers (staticcheck U1000)

| Service | Unused Items |
|---------|--------------|
EOF

for f in "$REPORTS_DIR"/staticcheck-*.txt; do
    if [[ -f "$f" ]]; then
        service=$(basename "$f" .txt | sed 's/staticcheck-//')
        count=$(wc -l < "$f" | tr -d ' ')
        echo "| $service | $count |"
    fi
done

cat << EOF

---

## TypeScript/JavaScript

EOF

if [[ -f "$REPORTS_DIR/knip-report.json" ]]; then
    cat << EOF
### Summary (knip)

| Category | Count |
|----------|-------|
EOF

    files=$(jq '.files | length' "$REPORTS_DIR/knip-report.json" 2>/dev/null || echo 0)
    deps=$(jq '.dependencies | length' "$REPORTS_DIR/knip-report.json" 2>/dev/null || echo 0)
    exports=$(jq '.exports | length' "$REPORTS_DIR/knip-report.json" 2>/dev/null || echo 0)
    types=$(jq '.types | length' "$REPORTS_DIR/knip-report.json" 2>/dev/null || echo 0)

    echo "| Unused files | $files |"
    echo "| Unused dependencies | $deps |"
    echo "| Unused exports | $exports |"
    echo "| Unused types | $types |"
else
    echo "No knip report found. Run \`make dead-code-ts\` first."
fi

cat << EOF

---

## Detailed Reports

Individual reports are available in the \`reports/\` directory:

**Go:**
EOF

for f in "$REPORTS_DIR"/deadcode-*.txt "$REPORTS_DIR"/staticcheck-*.txt; do
    [[ -f "$f" ]] && echo "- \`$(basename "$f")\`"
done

cat << EOF

**TypeScript:**
- \`knip-report.json\` (machine-readable)
- \`knip-report.txt\` (human-readable)

---

## Review Guidelines

### Before Removing Code

1. **Verify with tests**: Run \`make test\` to ensure tests still pass
2. **Check external usage**: For npm_player/npm_studio, exports may be used by external consumers
3. **Check dynamic usage**: Reflection, string-based lookups, or dynamic imports may not be detected
4. **Check conditional compilation**: Build tags may hide usage
5. **Consider feature flags**: Code may be gated by runtime configuration

### False Positive Patterns

- **Interface implementations**: Methods implementing interfaces may appear unused
- **Reflection targets**: Code accessed via \`reflect\` package
- **Plugin systems**: Dynamically loaded code
- **Event handlers**: Callbacks registered at runtime
- **GraphQL resolvers**: May be called by generated code
- **Protobuf extensions**: Custom options or extensions

### Recommended Workflow

1. Create a tracking issue for dead code cleanup
2. Group related removals into focused PRs
3. Include evidence (tool output) in PR description
4. Get code review from domain expert
5. Monitor for regressions after merge
EOF
