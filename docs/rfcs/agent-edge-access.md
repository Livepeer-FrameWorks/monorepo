# RFC: Agent ↔ Edge Node Direct Access

Status: Draft

## Problem

MCP access is currently centralized at the API gateway. Agents cannot directly interact with video streaming edge nodes (MistServer) for geo-local operations, deployment, or real-time diagnostics.

## Goals

- Enable agents to discover and interact with video edge nodes.
- Support agent-driven edge node deployment and configuration.
- Provide geo-local MCP endpoints for lower latency video operations.
- Preserve billing and authentication enforcement through prepaid/x402.

## Non-Goals

- Replacing gateway MCP entirely.
- Changing the core billing model.
- Modifying website/docs infrastructure or reverse proxy behavior.

## Open Questions

- Edge node identity: wallet-based, certificate-based, or service identity?
- Which MCP tools should be edge-local vs gateway-only?
- How does agent → edge authentication work (signed requests, x402, or service tokens)?
- How do edge nodes report usage for prepaid billing and enforcement?
- What is the discovery model for edge-local MCP endpoints?

## Related

- `docs/architecture/agent-access.md`
- `docs/skills/skill.md`
- `docs/skills/skill.json`
