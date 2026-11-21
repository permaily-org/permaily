/**
 * What the overview (`/`, src/pages/overview/OverviewPage.tsx) reads of the server: the mailboxes, the rules and the
 * connections. A test that lands on it, after signing in or by a route that leads home, answers them; a test of the
 * overview itself answers what it needs.
 */
import { ruleListOf } from './rules.ts'
import { ok, server } from './server.ts'

export const overviewRoutes = [
  'GET /api/v1/mailboxes',
  'GET /api/v1/rules',
  'GET /api/v1/connections',
] as const

/** An account with no mailbox, no rule of its own and no connection, as the overview reads it. */
export function emptyOverview() {
  return server
    .on('GET /api/v1/mailboxes', ok({ mailboxes: [], limit: 5 }))
    .on('GET /api/v1/rules', ok(ruleListOf()))
    .on('GET /api/v1/connections', ok({ connections: [] }))
}
