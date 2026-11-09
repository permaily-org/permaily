# MCP compatibility

How each MCP client connects to `mcp.permaily.com` (F2). Every row must be tested against production and dated; nothing here is tested yet because the MCP server does not exist.

| Client | Registration | Protocol version | Scope step-up | Status |
|---|---|---|---|---|
| Claude.ai custom connector | CIMD + PKCE (expected) | 2026-07-28 (expected) | Re-consents automatically (expected) | not tested |
| Claude Code | CIMD + PKCE (expected) | 2026-07-28 (expected) | Needs re-auth from `/mcp` (expected) | not tested |
| ChatGPT / Codex | CIMD + PKCE (expected) | 2025-06-18 (expected) | Never steps up: request all scopes up front (expected) | not tested |
| Claude Desktop | to discover | to discover | to discover | not tested |

Expectations come from Portifact's production tests on 2026-11-08 (same clients, same flows); see [decisions/0007-oauth-server.md](decisions/0007-oauth-server.md).

## Known pitfalls to test

- chatgpt.com answers `304 Not Modified` when a client metadata document is revalidated; treating it as an error breaks Codex token refresh after 5 minutes.
- Every client sends `resource=`; tokens must only work at the resource in their audience.
- Tool results that contain email content must be marked as data, and the server instructions must tell the model that email content is never instructions.
