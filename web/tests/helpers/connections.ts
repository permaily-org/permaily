/**
 * The consent screen and connections as the server answers them (ADR 0030, internal/contracts/connections.go), with
 * made-up vendors and values only. A test changes what it needs.
 */
import { MODEL_NOTICE_VERSION } from '../../src/lib/model-notice.ts'
import { personal, work } from './mailboxes.ts'
import { ana, meOf, ok, server } from './server.ts'

/** What a vendor sent the browser to /oauth/authorize with: everything after "?". Nothing of it may be kept. */
export const vendorQuery =
  'response_type=code&client_id=https%3A%2F%2Fclaude.ai%2Foauth%2Fclaude-code-client-metadata&redirect_uri=http%3A%2F%2Flocalhost%3A33418%2Fcallback&state=st8te-secret&code_challenge=Q0hBTExFTkdFLWNoYWxsZW5nZS1jaGFsbGVuZ2UtY2hhbGxlbmdl&code_challenge_method=S256&resource=https%3A%2F%2Fmcp.permaily.com%2Fmcp'

/** The request's identifier and where the browser goes back to the vendor: neither may be kept. */
export const requestId = 'pmq_r3qu3stIdentifierOfTheTest00000000000000000'
export const allowedRedirect =
  'http://localhost:33418/callback?code=pmc_c0deOfTheTest&state=st8te-secret&iss=https%3A%2F%2Fapi.permaily.com'
export const deniedRedirect =
  'http://localhost:33418/callback?error=access_denied&state=st8te-secret&iss=https%3A%2F%2Fapi.permaily.com'

export const endsAt = '2027-02-14T10:00:00Z'

export function sentence(text: string) {
  return { lang: 'en', text, lead: [], lines: [{ parts: [{ text }] }] }
}

export const defaultRule = {
  id: 'rul_default',
  version: 1,
  sentence: [sentence('Sign-in codes and password resets')],
}

export function snapshotOf(overrides: Record<string, unknown> = {}) {
  return { change: 'new', ends_at: endsAt, draft_id: 'drf_start', ...overrides }
}

/** Claude Code's request: no query, a loopback redirect, the default parts, for MCP. */
export function requestOf(overrides: Record<string, unknown> = {}) {
  return {
    id: requestId,
    vendor: { domain: 'claude.ai', verified: 'Anthropic' },
    client_name: 'Claude Code',
    redirect_host: 'localhost',
    loopback: true,
    resource: 'mcp',
    parts: ['sender', 'subject', 'body'],
    since: { new: true },
    ends_in_days: 90,
    mailboxes: [personal.id, work.id],
    draft: { mailboxes: [personal.id, work.id], since: { new: true } },
    rules: [defaultRule],
    change: 'new',
    snapshot: snapshotOf(),
    model_notice: MODEL_NOTICE_VERSION,
    expires_at: '2026-11-12T10:15:00Z',
    ...overrides,
  }
}

export function summary(n: number, from = `sender${n}@example.org`) {
  return {
    id: `0000000000000000000000000000000${n}`,
    received_at: '2026-11-10T09:00:00Z',
    expires_at: '2027-02-08T09:00:00Z',
    size: 1200,
    from: [{ name: `Sender ${n}`, address: from }],
    subject: `Subject ${n}`,
    snippet: '',
    attachments: 0,
    confirmation: false,
  }
}

/** A preview's answer: a page of matches, what approving does and its snapshot. */
export function previewOf(
  messages: unknown[] = [],
  overrides: {
    next?: string
    change?: string
    draft_id?: string
    held?: string[]
    needs_model?: string[]
    model?: string
  } = {},
) {
  const change = overrides.change ?? 'new'
  const draftId = overrides.draft_id ?? 'drf_start'
  return {
    draft_id: draftId,
    change,
    snapshot: snapshotOf({ change, draft_id: draftId }),
    page: {
      messages,
      needs_model: overrides.needs_model ?? [],
      unknown: [],
      ...(overrides.next ? { next: overrides.next } : {}),
      ...(overrides.held ? { held: overrides.held } : {}),
      ...(overrides.model ? { model: overrides.model } : {}),
    },
  }
}

export const device1 = {
  id: 'dev_one',
  made_at: '2026-11-01T10:00:00Z',
  state: 'active',
  parts: ['sender', 'subject', 'body'],
}

export const device2 = {
  id: 'dev_two',
  made_at: '2026-11-05T10:00:00Z',
  state: 'active',
  parts: ['sender', 'subject'],
  narrower: true,
}

/** Claude Code's connection to Ana's two mailboxes, on two devices. */
export function connectionOf(overrides: Record<string, unknown> = {}) {
  return {
    id: 'con_claudecode',
    vendor: { domain: 'claude.ai', verified: 'Anthropic' },
    client_id: 'https://claude.ai/oauth/claude-code-client-metadata',
    client_name: 'Claude Code',
    resource: 'mcp',
    state: 'active',
    parts: ['sender', 'subject', 'body'],
    mailboxes: [personal.id, work.id],
    since: { new: true, at: '2026-11-01T10:00:00Z' },
    made_at: '2026-11-01T10:00:00Z',
    ends_at: '2027-01-30T10:00:00Z',
    devices: [device1, device2],
    model_notice: MODEL_NOTICE_VERSION,
    delivery: 'delivering',
    version: 7,
    ...overrides,
  }
}

/** An app's connection with a query, through the API, from a vendor nobody verified. */
export const tripApp = connectionOf({
  id: 'con_tripapp',
  vendor: { domain: 'trips.example' },
  client_id: 'https://trips.example/client.json',
  client_name: 'Trip App',
  resource: 'api',
  parts: ['sender', 'subject'],
  query: {
    formula: { v: 1, all: [] },
    id: 'qry_trips',
    sentence: sentence('Flight and hotel bookings'),
    model: false,
  },
  mailboxes: [personal.id],
  since: { new: false, at: '2026-10-01T00:00:00Z' },
  devices: [device1],
  version: 2,
})

/** An ended connection, replaced by Claude Code's. */
export const ended = connectionOf({
  id: 'con_old',
  state: 'ended',
  ended_at: '2026-11-01T10:00:00Z',
  end_reason: 'replaced',
  replaced_by: 'con_claudecode',
  devices: [
    { ...device1, state: 'ended', ended_at: '2026-11-01T10:00:00Z', end_reason: 'replaced' },
  ],
  version: 4,
})

/** The account's rules when it has none of its own (ADR 0040): the default rule, on. */
export const noRules = {
  default: { rule: defaultRule, on: true },
  rules: [],
  version: 1,
  max: 16,
}

/**
 * The routes the consent screen and a connection's page read besides their own: who is signed in, the mailboxes'
 * names, and the account's rules.
 */
export function signedInWithMailboxes(account = ana, session: { confirmed_until?: string } = {}) {
  return server
    .on('GET /api/v1/me', ok(meOf(account, session)))
    .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
    .on('GET /api/v1/rules', ok(noRules))
}

export const startRoute = 'POST /api/v1/oauth/requests'
export const previewRoute = `POST /api/v1/oauth/requests/${requestId}/matches`
export const decisionRoute = `POST /api/v1/oauth/requests/${requestId}/decision`
