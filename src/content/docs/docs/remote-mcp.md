---
title: Remote MCP
description: How agents connect to Permaily, the tools they get, and how they ask for more.
---

Agents connect through one address and start with nothing. Connecting alone shares no mail.

```text
https://mcp.permaily.com
```

## Connect an agent

1. Choose the agent.
2. Add the Permaily address to it.
3. Come back and choose what it can see, while the preview updates.

An agent doesn't declare a [query](/docs/queries/), because MCP clients have no way to declare what they need up front. The person ticks the kinds of mail it may search at [consent](/docs/consent-screen/#agents-choose-differently), and every search it writes stays inside that scope.

## Tools

Each tool is limited to the access the token carries.

| Tool | Purpose | Available when |
| --- | --- | --- |
| `describe_access` | Describes this connection's scope, parts and actions, so agents can plan | Always |
| `search_messages` | Runs the agent's own search, inside its scope | Read allowed |
| `get_message` | Returns the approved parts of one message | Read allowed |
| `apply_label`, `archive` | Tidying actions | Granted explicitly |
| `create_draft` | Prepares a message without sending it | Granted explicitly |

## Scope challenges

Asking for more than the access permits returns a scope challenge, which the specification's step-up flow turns into a new consent screen. An agent can ask the person for more, and it can never take more.

## Authorization

Permaily follows the [2026-07-28 MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).

- Permaily is both the OAuth authorization server and the MCP resource server.
- It publishes Protected Resource Metadata and supports Client ID Metadata Documents.
- Tokens are bound to Permaily's server through resource indicators.
- An agent never receives a Gmail token.

## Prompt injection

Email is attacker-controlled text, and an MCP server hands it straight to a language model. Read [Untrusted content](/docs/untrusted-content/) before you rely on an agent's answers.
