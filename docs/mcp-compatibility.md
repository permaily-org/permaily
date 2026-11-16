# MCP compatibility

How each MCP client connects to `mcp.permaily.com` (F2, ADR 0030). Every row must be tested against production (or a staging image on the production hosts) and dated. The MCP server is designed, not built: what is known so far comes from the spike of ADR 0030 (2026-11-16), a prototype run against the real clients through a public tunnel.

| Client | Registration | Protocol version | Scope step-up | Status |
|---|---|---|---|---|
| Claude Code 2.1.325 | CIMD `https://claude.ai/oauth/claude-code-client-metadata`, PKCE S256, `resource=`; loopback redirect on any port; no client authentication | 2026-07-28 for MCP calls (no `initialize`, no session); 2025-11-25 in its discovery requests | Says to run `/mcp`; the next login asks for the union of scopes and revokes its old tokens first | Spike, 2026-11-16 (not production) |
| Codex CLI 0.169.0 | CIMD `https://chatgpt.com/oauth/codex/client.json`, PKCE S256, `resource=`; loopback redirect; no client authentication | 2025-06-18 (`initialize`); unauthenticated probes 2024-11-05 | Never: the tool fails; `codex mcp login --scopes …` asks for everything up front | Spike, 2026-11-16 (not production); its refresh succeeded on a `304 Not Modified` from chatgpt.com |
| Claude.ai custom connector | CIMD `https://claude.ai/oauth/mcp-oauth-client-metadata` (published), redirect `https://claude.ai/api/mcp/auth_callback`, no client authentication | to observe | to observe | not tested: the owner's guided session (ADR 0030) |
| Claude Desktop | expected to be Claude.ai's connector | to observe | to observe | not tested: the owner's guided session |
| ChatGPT | CIMD `https://chatgpt.com/oauth/client.json` or `https://chatgpt.com/oauth/chatgpt/client.json` (both published), both declaring `private_key_jwt` with `https://chatgpt.com/oauth/jwks.json` | to observe | expected never | not tested: the owner's guided session, which also shows whether it sends a client assertion |

Earlier expectations came from Portifact's production tests on 2026-11-08 (same clients, same flows); see [decisions/0007-oauth-server.md](decisions/0007-oauth-server.md), superseded by [decisions/0030-connections-and-the-authorization-server-how.md](decisions/0030-connections-and-the-authorization-server-how.md).

## Known pitfalls to test

- chatgpt.com answers `304 Not Modified` when a client metadata document is revalidated; treating it as an error breaks Codex token refresh after 5 minutes (seen in the spike).
- Every client sends `resource=`; tokens must only work at the resource in their audience.
- Claude Code reads the resource's metadata without a 401 first and asks for the scopes it lists; Codex asks for the scopes of the 401's challenge.
- A loopback client's document lists `http://localhost/callback` without a port: any port must be accepted.
- With access tokens of a few minutes Claude Code refreshes before every call; an hour avoids it.
- A `401` drops Codex's tools until `codex mcp login`: a storage error must be a `503`, never a `401`.
- Tool results that contain email content must be marked as data, and the server instructions must tell the model that email content is never instructions.
