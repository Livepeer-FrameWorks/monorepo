#!/usr/bin/env bash
# tenant: qa
. "$(dirname "$0")/../lib.sh"

need node jq || finish
REPO=${STACK_REPO:-/repo}
token_id=
cleanup_token() {
  [ -n "$token_id" ] || return 0
  local revoked
  revoked=$(gql_session 'mutation($id:ID!){revokeDeveloperToken(id:$id){__typename}}' "$(jq -cn --arg id "$token_id" '{id:$id}')")
  json_has "$revoked" '.data.revokeDeveloperToken.__typename == "DeleteSuccess"' || return 1
  token_id=
}
cleanup_exit() {
  local result=$?
  trap - EXIT INT TERM
  if ! cleanup_token; then
    fail "temporary MCP token revocation failed"
    result=1
  fi
  exit "$result"
}
trap cleanup_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# The application template includes stream keys and metrics as well as reads.
# execute_query also requires an explicit arbitrary-query automation grant.
created=$(gql_session 'mutation($input:CreateDeveloperTokenInput!){createDeveloperToken(input:$input){__typename ... on DeveloperToken{id tokenValue} ... on ValidationError{message} ... on AuthError{message}}}' \
  "$(jq -cn '{input:{name:"stack MCP smoke",expiresIn:1,permissions:"streams:read,streams:write,analytics:read,developer:read,developer:write,mcp:high-risk"}}')")
token_id=$(jq -r '.data.createDeveloperToken.id // empty' <<<"$created")
token_value=$(jq -r '.data.createDeveloperToken.tokenValue // empty' <<<"$created")
if [ -z "$token_id" ] || [ -z "$token_value" ]; then
  fail "create scoped token: $(jq -c 'del(.data.createDeveloperToken.tokenValue)' <<<"$created")"
  finish
fi

check "generated query executes with scoped caller auth" node "$REPO/scripts/stack/sdk/mcp-smoke.mjs" "$BRIDGE_URL" "$token_value"
check "temporary MCP token revoked" cleanup_token
finish
