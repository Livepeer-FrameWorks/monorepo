#!/bin/sh
#
# Tests for cutover-thumbnail-ownership-wipe.sh. Stubs psql/mc/date AND the frameworks CLI on PATH (the script now
# derives descriptors through `frameworks cluster storage descriptor`, whose YAML parsing is covered by the Go test
# TestParseManifest_S3PrefixQuotedAndCommented). Drives every guarded path: early execution (drain), missing input,
# unknown cluster key, alias/endpoint mismatch, SQL failure, bucket unreachable, deletion failure, verification list
# error, verification non-empty, success, and that bucket+prefix come from the descriptor. No real DB/store is touched.
#
# The STUB_DESC_JSON_* fixtures deliberately hold literal JSON (embedded quotes); the frameworks stub echoes them
# quoted, so shellcheck's literal-quote warnings on those exports are false positives here.
# shellcheck disable=SC2089,SC2090

set -eu

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
SCRIPT="$SELF_DIR/cutover-thumbnail-ownership-wipe.sh"
[ -f "$SCRIPT" ] || { echo "cannot find $SCRIPT" >&2; exit 1; }

STUBDIR="$(mktemp -d)"
trap 'rm -rf "$STUBDIR"' EXIT

cat > "$STUBDIR/date" <<'EOF'
#!/bin/sh
[ "$1" = "+%s" ] && { echo "${STUB_NOW:-0}"; exit 0; }
echo 0
EOF

# psql runs only the Commodore enumeration: return STUB_STREAMS or fail on STUB_PSQL_FAIL.
cat > "$STUBDIR/psql" <<'EOF'
#!/bin/sh
[ "${STUB_PSQL_FAIL:-0}" = "1" ] && exit 1
printf '%s' "${STUB_STREAMS:-}"; [ -n "${STUB_STREAMS:-}" ] && printf '\n'
exit 0
EOF

# frameworks: `cluster storage descriptor <key> --manifest <path>` emits the LITERAL JSON in STUB_DESC_JSON_<key-with-_>
# (so tests can inject null/newline/malformed prefixes exactly); nonzero if unset (unknown cluster). The script parses
# the JSON with REAL jq — that is the contract under test.
cat > "$STUBDIR/frameworks" <<'EOF'
#!/bin/sh
if [ "$1" = "cluster" ] && [ "$2" = "storage" ] && [ "$3" = "descriptor" ]; then
  safe="$(printf '%s' "$4" | tr '-' '_')"
  eval "j=\${STUB_DESC_JSON_$safe:-}"
  [ -n "$j" ] || { echo "cluster \"$4\" not found in manifest" >&2; exit 1; }
  printf '%s\n' "$j"
  exit 0
fi
exit 0
EOF

# mc: "alias ls <a> --json" -> {"URL": STUB_ALIAS_<a>}; "ls" on a /thumbnails/ path is verify, else accessibility;
# "rm" is a delete.
cat > "$STUBDIR/mc" <<'EOF'
#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "alias" ]; then
  al="$2"
  eval "url=\${STUB_ALIAS_$al:-}"
  [ -n "$url" ] || exit 1
  printf '{"status":"success","alias":"%s","URL":"%s"}\n' "$al" "$url"
  exit 0
fi
if [ "$cmd" = "ls" ]; then
  case "$1" in
    */thumbnails/*)
      [ "${STUB_VERIFY_LSFAIL:-0}" = "1" ] && exit 1
      [ "${STUB_VERIFY_NONEMPTY:-0}" = "1" ] && echo "some-object"
      exit 0 ;;
    *)
      [ "${STUB_BUCKET_UNREACHABLE:-0}" = "1" ] && exit 1
      exit 0 ;;
  esac
fi
if [ "$cmd" = "rm" ]; then
  [ "${STUB_RM_FAIL:-0}" = "1" ] && exit 1
  exit 0
fi
exit 0
EOF

chmod +x "$STUBDIR/date" "$STUBDIR/psql" "$STUBDIR/frameworks" "$STUBDIR/mc"

# GITOPS_CLUSTER_FILE only has to exist (the frameworks stub provides the descriptor; the script no longer parses it).
GITOPS="$STUBDIR/cluster.yaml"
echo "clusters: {}" > "$GITOPS"

NOW=1000000000
pass=0
fail=0

run_case() {
	name="$1"; want="$2"; substr="$3"
	out="$(PATH="$STUBDIR:$PATH" STUB_NOW="$NOW" sh "$SCRIPT" 2>&1)" && rc=0 || rc=$?
	ok=1
	if [ "$want" = "0" ]; then [ "$rc" -eq 0 ] || ok=0; else [ "$rc" -ne 0 ] || ok=0; fi
	case "$out" in *"$substr"*) : ;; *) ok=0 ;; esac
	if [ "$ok" = "1" ]; then
		pass=$((pass + 1)); echo "ok   - $name"
	else
		fail=$((fail + 1)); echo "FAIL - $name (rc=$rc, want=$want, missing substr: $substr)"; echo "    output: $out"
	fi
}

base_env() {
	unset STUB_PSQL_FAIL STUB_STREAMS STUB_BUCKET_UNREACHABLE STUB_RM_FAIL STUB_VERIFY_LSFAIL STUB_VERIFY_NONEMPTY 2>/dev/null || true
	export STOP_EPOCH=$((NOW - 2000)) # >= 1200s drain
	export COMMODORE_DSN="postgres://commodore" GITOPS_CLUSTER_FILE="$GITOPS"
	export EU_MC_ALIAS="eualias" EU_CLUSTER_KEY="media-eu-1"
	export US_MC_ALIAS="usalias" US_CLUSTER_KEY="media-us-1"
	export STUB_DESC_JSON_media_eu_1='{"bucket":"frameworks","prefix":"prod","endpoint":"https://eu.example.com"}'
	export STUB_DESC_JSON_media_us_1='{"bucket":"frameworks-enam","prefix":"prod","endpoint":"https://us.example.com"}'
	export STUB_ALIAS_eualias="https://eu.example.com"
	export STUB_ALIAS_usalias="https://us.example.com"
	export STUB_STREAMS="s1
s2"
}

# 1) Early execution: drain not elapsed.
base_env; export STOP_EPOCH=$((NOW - 100)); run_case "early-execution refused" nonzero "must wait"

# 2) Missing required input.
base_env; unset GITOPS_CLUSTER_FILE; run_case "missing GITOPS_CLUSTER_FILE refused" nonzero "GITOPS_CLUSTER_FILE"

# 3) Unknown cluster key (descriptor command fails).
base_env; export EU_CLUSTER_KEY="media-nope-1"; run_case "unknown cluster key refused" nonzero "descriptor"

# 4) Alias points at the wrong endpoint vs the descriptor.
base_env; export STUB_ALIAS_eualias="https://attacker.example.com"; run_case "endpoint mismatch refused" nonzero "wrong endpoint"

# 5) Enumeration (SQL) failure.
base_env; export STUB_PSQL_FAIL=1; run_case "enumeration failure aborts" nonzero "enumeration query failed"

# 6) Bucket unreachable (typo'd alias/bucket, or creds wrong) — must refuse before any delete.
base_env; export STUB_BUCKET_UNREACHABLE=1; run_case "bucket unreachable refused" nonzero "not reachable"

# 7) Deletion failure.
base_env; export STUB_RM_FAIL=1; run_case "delete failure aborts" nonzero "delete failed"

# 8) Verification list error (a list error is a failure, not "empty").
base_env; export STUB_VERIFY_LSFAIL=1; run_case "verify list error aborts" nonzero "cannot list"

# 9) Verification non-empty (object survived the wipe).
base_env; export STUB_VERIFY_NONEMPTY=1; run_case "verify non-empty aborts" nonzero "still has objects"

# 10) Success.
base_env; run_case "happy path succeeds" 0 "cutover: OK"

# 11) Prefix is AUTHORITATIVE from the descriptor (a different prefix changes the target).
base_env; export STUB_DESC_JSON_media_eu_1='{"bucket":"frameworks","prefix":"customprefix","endpoint":"https://eu.example.com"}'
run_case "prefix taken from descriptor" 0 "frameworks/customprefix/thumbnails"

# 12) A null prefix is REFUSED, never coerced to the literal "null".
base_env; export STUB_DESC_JSON_media_eu_1='{"bucket":"frameworks","prefix":null,"endpoint":"https://eu.example.com"}'
run_case "null prefix refused" nonzero "descriptor JSON invalid"

# 13) Malformed descriptor JSON is refused (not treated as an empty/degenerate target).
base_env; export STUB_DESC_JSON_media_eu_1='not-json'
run_case "malformed JSON refused" nonzero "descriptor JSON invalid"

# 14) A prefix with a CONTROL character (newline) is REFUSED — a destructive path uses a sane prefix grammar rather than
#     addressing a weird key. (\n in single quotes is a literal JSON escape jq decodes to a real newline.)
base_env; export STUB_DESC_JSON_media_eu_1='{"bucket":"frameworks","prefix":"prod\n","endpoint":"https://eu.example.com"}'
run_case "control-char prefix refused" nonzero "control-char-free"

# 15) A PRINTABLE special char in the prefix (e.g. '|') is allowed and lands in the target losslessly (jq builds it).
base_env; export STUB_DESC_JSON_media_eu_1='{"bucket":"frameworks","prefix":"a|b","endpoint":"https://eu.example.com"}'
run_case "printable-special prefix round-trips into target" 0 "frameworks/a|b/thumbnails"

echo "----"
echo "passed=$pass failed=$fail"
[ "$fail" -eq 0 ]
