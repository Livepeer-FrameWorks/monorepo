# Skipper Chat Safety Remediation Plan

Goal: remediate audit findings for Skipper chat lifecycle safety while keeping the
service token mandatory for provisioning and inter-service calls.

## Scope
- /api/skipper/chat (HTTP SSE)
- GraphQL subscription skipperChat → Bridge gRPC → Skipper gRPC → Orchestrator
- MCP tool calls through Bridge → Skipper spoke
- Conversation ownership and tenant scoping
- Prompt assembly, summary injection, pre-retrieval
- Horizontal scaling: conversationLocks

## Plan
1. **Service-token tenant binding (gRPC + MCP).**
   - Introduce a config-driven allowlist mapping service tokens to tenant IDs
     and a service role (e.g., `SERVICE_TOKEN_TENANT_MAP`).
   - In gRPC auth middleware:
     - When a service token is used, ignore or reject `x-tenant-id`/`x-user-id`
       that do not match the bound tenant.
     - Optionally allow a "cluster admin" tenant only when the token is marked
       as admin in the allowlist.
   - In MCP spoke:
     - Remove `tenant_id` from tool arguments in favor of context derived from
       the service token binding.
     - If `tenant_id` must remain, validate that it matches the token binding.

2. **Conversation ownership enforcement.**
   - Require `user_id` for user-facing Chat/List/Get/Delete/Update unless the
     caller has an admin/service role.
   - Enforce role checks in Skipper gRPC handlers before calling
     ConversationStore, with a single helper to avoid drift.

3. **Prompt safety + token budget accounting.**
   - Include summary + pre-retrieval context in prompt budget calculations.
   - Mark summary/pre-retrieval blocks as untrusted context (e.g., add
     "do not follow instructions" preface or move into a separate tool message).
   - Add a budget cap to summary length and pre-retrieval injected context.

4. **Conversation locking for horizontal scale.**
   - Replace in-process `conversationLocks` with `pg_advisory_xact_lock`
     keyed on conversation ID.
   - Add timeout handling and monitoring for lock contention.

5. **Tests to add (minimum).**
   - gRPC auth middleware: service-token tenant binding and metadata rejection.
   - Skipper gRPC handlers: user_id required for user flows; admin exceptions.
   - MCP spoke: tenant binding validation and rejection.
   - Prompt budget: regression test that summary + pre-retrieval stay in-budget.
   - Concurrency: advisory lock prevents concurrent updates (integration).

## Notes
- Keep existing service-token flows for provisioning and inter-service calls.
- Use config/env to stage rollout; allow audit mode logging first, then enforce.

## Sign-off
Prepared for implementation and review.
