# 0007 OAuth authorization server for apps and MCP clients

Status: Proposed. Needs a spike (`spikes/oauth-server/`) before acceptance.
Date: 2026-11-09

## Context

F2 needs one authorization server inside the enclave for third-party apps (API) and AI agents (remote MCP: Claude.ai, Claude Code, ChatGPT/Codex). Portifact's production experience (2026-11-08) gives the requirements:

- Opaque tokens checked on every request, so revocation is immediate.
- Audience per resource (RFC 8707: `/api` vs `/mcp`) and protected resource metadata (RFC 9728).
- Scope step-up per tool: `403 insufficient_scope` with `WWW-Authenticate` scope; Claude.ai re-consents, Claude Code needs re-auth, Codex never steps up (ask for all scopes up front).
- Client ID Metadata Documents (CIMD) first, dynamic registration as fallback; all three clients used CIMD with PKCE and sent `resource=`.
- Device flow (RFC 8628) for the CLI.
- Metadata fetches must handle `304 Not Modified` (chatgpt.com), or Codex token refresh breaks after 5 minutes.

## Options

1. `zitadel/oidc` (Go, OpenID-certified OP library).
2. `ory/fosite` (Go, OAuth 2.0 framework; more assembly required).
3. A minimal own implementation.

## Decision

Pending. The spike must show, against real clients: CIMD, PKCE, resource indicators, step-up, device flow, opaque token introspection in process, and the 304 case.

## To verify

- The compatibility matrix in [../mcp-compatibility.md](../mcp-compatibility.md), filled in against production.
