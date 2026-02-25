# MCP in Production: Patterns and Deployments (2025-2026)

Research synthesized from current sources. Sources linked throughout.

## The Dominant Pattern: Customer-Side LLM + Company-Side MCP Server

The overwhelming model is: the SaaS company hosts a remote MCP server; the customer
brings their own LLM client (Claude, Cursor, ChatGPT, etc.). This is analogous to
REST APIs — company exposes endpoints, customers consume with whatever client they want.

### Confirmed Production Deployments

| Company          | What They Ship                                                | Status                                          |
| ---------------- | ------------------------------------------------------------- | ----------------------------------------------- |
| **Sentry**       | Error monitoring + debugging (16 tools) on Cloudflare Workers | Production (March 2025, first major remote MCP) |
| **Atlassian**    | Jira + Confluence access from Claude                          | Beta                                            |
| **GitHub**       | Official GitHub MCP server                                    | Public preview (April 2025)                     |
| **Cloudflare**   | Reference architecture for remote MCP servers                 | Production (infra provider)                     |
| **Confluent**    | Real-time data streams + Flink SQL                            | Production                                      |
| **Workato**      | Enterprise MCP, 100+ pre-built servers for Slack/Gong/Jira    | Production (Oct 2025)                           |
| **Oracle**       | Autonomous AI Database MCP Server                             | Production                                      |
| **Google Cloud** | Managed remote MCP servers for GCP services                   | Production                                      |
| **New Relic**    | Observability data for IDE copilots                           | Production                                      |
| **PagerDuty**    | Incident and service data for IDE copilots                    | Production                                      |

Sources: [Pento year-in-review](https://www.pento.ai/blog/a-year-of-mcp-2025-review),
[MCP Manager statistics](https://mcpmanager.ai/blog/mcp-adoption-statistics/),
[Pragmatic Engineer](https://newsletter.pragmaticengineer.com/p/mcp)

### Adoption Numbers

- 97M monthly SDK downloads (Python + TypeScript)
- 5,500+ servers in PulseMCP registry
- Remote MCP servers grew 4x since May 2025
- ~28% of Fortune 500 have implemented MCP servers

## How SaaS Companies Expose MCP (Ranked by Maturity)

**Pattern A: Developer Tool / IDE Integration (80%+ of usage)**
Developers connect from Cursor, Claude Code, VS Code. Use cases: debugging (Sentry),
code search (Sourcegraph), project management (Atlassian), CI/CD (CircleCI).

**Pattern B: Workflow Automation / Enterprise Integration**
Non-developer employees consume via ChatGPT/Claude. Workato and Zapier are main enablers.
Requires OAuth/SSO and RBAC.

**Pattern C: Customer Support / Knowledge Retrieval**
MCP servers expose knowledge bases and customer context for support AI. Mostly internal tooling.

**Pattern D: Autonomous Monitoring Agent (earliest stage)**
Company runs its own LLM server-side that uses MCP tools to monitor and act.
The November 2025 MCP spec's [Tasks primitive](https://medium.com/@dave-patten/mcps-next-phase-inside-the-november-2025-specification-49f298502b03)
enables this, but production deployments are rare. This is the 2026 frontier.

## Customer-Side vs Server-Side LLM Tradeoffs

### Customer-Side (dominant today)

| Pro                     | Detail                                                        |
| ----------------------- | ------------------------------------------------------------- |
| Zero LLM cost to vendor | Customer pays for their own tokens                            |
| Privacy simplification  | Vendor never sees prompts or LLM outputs, only tool calls     |
| Model-agnostic          | Works with any MCP-capable client                             |
| Faster time-to-market   | Building an MCP server is simpler than building an AI product |

| Con                               | Detail                                  |
| --------------------------------- | --------------------------------------- |
| No control over UX                | Can't control how the LLM presents data |
| Customer must have MCP client     | Requires Cursor/Claude/etc.             |
| Tool description quality critical | Poor descriptions = poor tool use       |

### Server-Side (emerging)

| Pro                       | Detail                                                              |
| ------------------------- | ------------------------------------------------------------------- |
| Full UX control           | Company controls prompt, model, output formatting                   |
| Enables autonomous agents | Monitoring, alerting, auto-remediation without customer interaction |
| Product differentiation   | "AI-powered feature X" is a feature, not just an API                |

| Con                      | Detail                                                 |
| ------------------------ | ------------------------------------------------------ |
| LLM cost falls on vendor | Token costs scale with usage                           |
| Security surface expands | Must handle prompt injection, data leakage             |
| Governance burden        | EU AI Act (August 2026) requires documented governance |

## Key Takeaway for Skipper

Phase 1 (customer-side LLM + MCP server) = the proven pattern. Skipper (server-side LLM)
= the frontier play. The differentiator is making AI diagnostics available to ALL customers,
not just those with MCP clients.
