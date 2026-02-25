# OpenClaw Heartbeat Pattern

Research synthesized from current sources. Sources linked throughout.

## What OpenClaw Is

[OpenClaw](https://openclaw.ai/) is an open-source AI agent that runs autonomously on a
user's machine. It gained traction in mid-2025 after Peter Steinberger's viral post about
using Claude Code as his primary interface to his machine.

Source: [AIML API overview](https://aimlapi.com/blog/openclaw-open-source-ai-agent-that-actually-takes-action),
[Turing College review](https://www.turingcollege.com/blog/openclaw)

**Note**: OpenClaw has [known security issues](https://www.theregister.com/2026/02/03/openclaw_security_problems/)
(Feb 2026). Reference the pattern, not the implementation.

## The Heartbeat Engine

OpenClaw runs on a "Heartbeat Engine" — a cron-like execution loop that wakes the agent
at configurable intervals (default: every 30 minutes to 4 hours depending on context).

Source: [OpenClaw docs - Cron vs Heartbeat](https://docs.openclaw.ai/automation/cron-vs-heartbeat)

### How It Differs From a Cron Job

| Aspect  | Cron                    | Heartbeat                                        |
| ------- | ----------------------- | ------------------------------------------------ |
| Trigger | Fixed schedule          | Fixed schedule                                   |
| Action  | Run a predefined task   | **Decide** whether to act based on context       |
| Context | None — executes blindly | Full conversation history + environment state    |
| Output  | Task result             | HEARTBEAT_OK (nothing to do) **or** action taken |

The key difference is **context-aware decision-making**. The LLM wakes up, reviews the
full situation, and makes an intelligent decision about whether anything needs attention.
Most heartbeats should be silent.

### Heartbeat Flow

```
1. Wake up on schedule
2. Check for updates (skill versions, new capabilities)
3. Review current state (DMs, feed, pending items)
4. Decide: is anything worth acting on?
5. If yes → take action, report what was done
6. If no → log HEARTBEAT_OK, go back to sleep
```

### Response Format Convention

```
HEARTBEAT_OK - Checked [system], all good.
```

or

```
Checked [system] - [actions taken]. [optional: plan for follow-up].
```

## Moltbook Heartbeat (Reference Implementation)

The agent-skill-system RFC includes a Moltbook heartbeat reference in its
Moltbook references section.

Moltbook's heartbeat checks: skill updates, DMs, feed, and decides whether to post,
reply, or flag something for the human. It explicitly notes: "heartbeat is just a backup
to make sure you don't forget to check in. Think of it like a gentle reminder, not a rule."

The agent can also check **anytime it wants** — the heartbeat is a floor, not a ceiling.

## Applying the Pattern to Skipper

Skipper's heartbeat adapts the OpenClaw pattern for streaming monitoring:

| OpenClaw                    | Skipper                                                     |
| --------------------------- | ----------------------------------------------------------- |
| Check DMs, feed, mentions   | Check active streams, health metrics, recent alerts         |
| Review conversation history | Review tenant context, past investigations, support history |
| Decide: post/reply/ignore   | Decide: investigate/flag/skip                               |
| HEARTBEAT_OK                | HEARTBEAT_OK (no action needed for this tenant)             |
| Tell human if needed        | Notify tenant via email/MCP SSE/dashboard alert             |

Key adaptation: Skipper's heartbeat is **multi-tenant**. Each heartbeat cycle iterates
over tenants with active streams and consultant access enabled, making a per-tenant
decision about whether investigation is warranted.

The heartbeat is also **not the only trigger**. Threshold-based alerts and Lookout
incident events can trigger immediate investigation outside the heartbeat cycle.
