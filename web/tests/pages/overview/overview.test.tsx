import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { connectionOf, ended, tripApp } from '../../helpers/connections.ts'
import { lost, mailboxOf, personal, work } from '../../helpers/mailboxes.ts'
import { emptyOverview, overviewRoutes } from '../../helpers/overview.ts'
import { bankRule, ruleListOf, ruleOf } from '../../helpers/rules.ts'
import { network, ok, once, server } from '../../helpers/server.ts'

function list(name: string) {
  return within(screen.getByRole('list', { name }))
}

/** Each row of a list, as its text, and where it leads when it leads anywhere. */
function rows(name: string): Array<[string, string | null]> {
  return list(name)
    .getAllByRole('listitem')
    .filter((item) => item.parentElement?.getAttribute('aria-label') === name)
    .map((item) => [
      item.textContent ?? '',
      item.querySelector(':scope > a')?.getAttribute('href') ?? null,
    ])
}

async function openOverview() {
  const view = await open('/')
  await screen.findByRole('heading', { level: 1, name: 'Overview' })
  return view
}

describe('the overview', () => {
  it('shows the account’s mailboxes, rules and connections as the server answers them, and nothing else', async () => {
    const paused = mailboxOf({ id: 'mbx_paused', label: 'Paused', state: 'paused' })
    const off = ruleOf(
      { id: 'rul_off' },
      { ...bankRule.check, draft: { ...bankRule.check.draft, on: false } },
    )
    const quiet = connectionOf({ id: 'con_quiet', client_name: '', delivery: 'model_unavailable' })
    server
      .signedIn()
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, paused, work, lost], limit: 5 }))
      .on('GET /api/v1/rules', ok(ruleListOf([ruleOf(), off])))
      .on('GET /api/v1/connections', ok({ connections: [connectionOf(), tripApp, quiet, ended] }))
    await openOverview()
    await screen.findByRole('list', { name: 'Your connections' })
    await screen.findByRole('list', { name: 'Your rules' })
    await screen.findByRole('list', { name: 'Your mailboxes' })

    // Each mailbox by its label and state, opening its inbox when it has one to open, as in Accounts.
    const boxes = rows('Your mailboxes')
    expect(boxes.map(([, href]) => href)).toEqual([
      '/inbox/mbx_personal',
      '/inbox/mbx_paused',
      null,
      null,
    ])
    expect(boxes[0][0]).toBe('PersonalReceiving since 3 Nov 2026')
    expect(boxes[1][0]).toBe('PausedPaused by Permaily')
    expect(boxes[2][0]).toBe('WorkWaiting for Gmail')
    expect(boxes[3][0]).toContain("Can't be read")

    // Every account's rule, then the person's own, each with what it does and who for in the server's words.
    expect(rows('Your rules')).toEqual([
      ['Sign-in and securityHolds the email back. Every connection.', '/rules'],
      [
        `${ruleOf().check.draft.name || ruleOf().check.sentence.text}${ruleOf().check.does}${ruleOf().check.for}`,
        '/rules/rul_home',
      ],
      ['BankHolds the email backOnly trips.example/client.jsonOff', '/rules/rul_off'],
    ])

    // The connections that read mail now, each opening its own page; the ended one is on the Connections page.
    expect(rows('Your connections')).toEqual([
      [
        'Claude Codeclaude.aiMCP, for agentsReads: All mail',
        '/connections/claude.ai/con_claudecode',
      ],
      [
        'Trip Apptrips.exampleThe API, for appsReads: Flight and hotel bookings',
        '/connections/trips.example/con_tripapp',
      ],
      [
        'claude.aiclaude.aiMCP, for agentsReads: All mailGets no mail now',
        '/connections/claude.ai/con_quiet',
      ],
    ])

    // Only these were asked: no count, filter or suggestion the server does not give.
    expect(new Set(server.calls.map((c) => `${c.method} ${c.path}`))).toEqual(
      new Set(['GET /api/v1/me', ...overviewRoutes]),
    )
    const main = screen.getByRole('main')
    expect(main.textContent).not.toMatch(/\d+ emails|waiting for you|All mailboxes/)
    expect(within(main).queryByRole('button')).toBeNull()
    expect(within(main).getByRole('link', { name: 'See accounts' }).getAttribute('href')).toBe(
      '/accounts',
    )
    expect(within(main).getByRole('link', { name: 'See rules' }).getAttribute('href')).toBe(
      '/rules',
    )
    expect(within(main).getByRole('link', { name: 'See connections' }).getAttribute('href')).toBe(
      '/connections',
    )
    await expectAccessible()
  })

  it('says when there is no mailbox, no rule of the person’s own and no connection, and where to start', async () => {
    server.signedIn()
    emptyOverview()
    await openOverview()
    expect(
      await screen.findByText(
        'No mailbox yet. Mail you forward from Gmail to a mailbox of your own shows here.',
      ),
    ).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Connect a mailbox' }).getAttribute('href')).toBe(
      '/accounts/new',
    )
    expect(await screen.findByText(/^No rules of your own yet\./)).toBeTruthy()
    expect(screen.getByRole('link', { name: 'New rule' }).getAttribute('href')).toBe('/rules/new')
    // Every account's rule is still there, as it is on the Rules page.
    expect(rows('Your rules')).toEqual([
      ['Sign-in and securityHolds the email back. Every connection.', '/rules'],
    ])
    expect(await screen.findByText(/^No app or agent reads your mail\./)).toBeTruthy()
    expect(screen.queryByRole('list', { name: 'Your connections' })).toBeNull()
    expect(screen.queryByRole('list', { name: 'Your mailboxes' })).toBeNull()
    await expectAccessible()
  })

  it('says no connection reads mail when every one has ended, and that the default rule is off when it is', async () => {
    server
      .signedIn()
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal], limit: 5 }))
      .on('GET /api/v1/rules', ok(ruleListOf([], { on: false })))
      .on('GET /api/v1/connections', ok({ connections: [ended] }))
    await openOverview()
    expect(await screen.findByText(/^No app or agent reads your mail\./)).toBeTruthy()
    await screen.findByRole('list', { name: 'Your rules' })
    expect(rows('Your rules')).toEqual([
      ['Sign-in and securityHolds the email back. Every connection.Off', '/rules'],
    ])
  })

  it('shows each part loading, and one that could not load says why and tries again', async () => {
    server
      .signedIn()
      .on('GET /api/v1/mailboxes', once(network, ok({ mailboxes: [personal], limit: 5 })))
      .on('GET /api/v1/rules', once(network, ok(ruleListOf())))
      .on('GET /api/v1/connections', once(network, ok({ connections: [connectionOf()] })))
    const { user } = await openOverview()
    for (const what of ['Your mailboxes', 'Your rules', 'Your connections']) {
      await screen.findByText(new RegExp(`^${what} couldn't load\\.`))
    }
    await expectAccessible()
    for (const retry of screen.getAllByRole('button', { name: 'Try again' }))
      await user.click(retry)
    await screen.findByRole('list', { name: 'Your mailboxes' })
    await screen.findByRole('list', { name: 'Your rules' })
    await screen.findByRole('list', { name: 'Your connections' })
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('shows Loading while the server has not answered', async () => {
    server.signedIn()
    const never = () => new Promise<never>(() => {})
    for (const route of overviewRoutes) server.on(route, never)
    await openOverview()
    await settle()
    const main = within(screen.getByRole('main'))
    expect(main.getAllByRole('status').map((s) => s.textContent)).toEqual([
      'Loading',
      'Loading',
      'Loading',
    ])
  })
})

describe('the sample screens are gone', () => {
  // The routes that showed sample data lead where any unknown route leads, and the overview shows the server's.
  for (const path of [
    '/consent/trip-app',
    '/consent/claude',
    '/consent/ledgerly',
    '/connections/new',
    '/?state=first-use',
  ]) {
    it(`${path} shows the overview from the server`, async () => {
      server.signedIn()
      emptyOverview()
      await open(path)
      await screen.findByRole('heading', { level: 1, name: 'Overview' })
      await waitFor(() => expect(current().startsWith('/')).toBe(true))
      if (!path.startsWith('/?')) expect(current()).toBe('/')
      expect(await screen.findByText(/^No app or agent reads your mail\./)).toBeTruthy()
      expect(screen.queryByText(/Nothing can read your mail|Connect an agent/)).toBeNull()
    })
  }

  it('the sidebar shows no count the server did not give', async () => {
    server.signedIn()
    emptyOverview()
    await openOverview()
    const nav = within(screen.getByRole('navigation', { name: 'Main' }))
    for (const link of nav.getAllByRole('link')) expect(link.textContent).not.toMatch(/\d/)
  })
})
