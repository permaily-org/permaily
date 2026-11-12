import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import * as api from '../../../src/lib/api.ts'
import { asAdmin, boss, confirm, helper, record, stats } from '../../helpers/admin.ts'
import { current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { confirmationNeeded, network, ok, once, refused, server } from '../../helpers/server.ts'

// What the admin panel sees and does of mailboxes (ADR 0026, "What the operator and admins see and do"): the
// readable part of a mailbox's record, never an address, a label or mail; pausing and resuming, and nothing
// else.

const eve = record({ mailboxes: 2 })
const receiving: api.MailboxRecord = {
  id: 'mbx_receiving',
  account: 'acc_eve',
  state: 'receiving',
  created_at: '2026-03-06T10:00:00Z',
  retention_days: 90,
  receiving_since: '2026-03-07T10:00:00Z',
  verified: true,
}
const paused: api.MailboxRecord = {
  id: 'mbx_paused',
  account: 'acc_eve',
  state: 'paused',
  created_at: '2026-03-06T11:00:00Z',
  retention_days: 30,
  paused_at: '2026-11-12T09:30:00Z',
  verified: false,
}
const waiting: api.MailboxRecord = {
  ...paused,
  id: 'mbx_waiting',
  state: 'waiting',
  paused_at: undefined,
}
const unreadable: api.MailboxRecord = {
  id: 'mbx_unreadable',
  account: 'acc_eve',
  state: 'unreadable',
  created_at: '2026-03-06T12:00:00Z',
  retention_days: 0,
  verified: false,
}

const mailboxesOf = 'GET /api/v1/admin/accounts/acc_eve/mailboxes'

function section() {
  return within(screen.getByRole('region', { name: 'Mailboxes' }))
}

async function account(
  shown: Record<string, unknown> = eve,
  mailboxes: api.MailboxRecord[] = [receiving, paused, unreadable],
  as = boss,
) {
  asAdmin(as)
    .on(`GET /api/v1/admin/accounts/${shown.id}`, ok(shown))
    .on(`GET /api/v1/admin/accounts/${shown.id}/mailboxes`, ok({ mailboxes }))
  const opened = await open(`/admin/accounts/${shown.id}`)
  await screen.findByRole('heading', { level: 1, name: String(shown.address) })
  await section().findByText('mbx_receiving')
  return opened
}

describe("an account's mailboxes", () => {
  it('says how many it has and what each record says, never an address or a label', async () => {
    await account()
    expect(screen.getByRole('region', { name: 'The account' }).textContent).toContain('Mailboxes2')
    const rows = section()
      .getAllByRole('listitem')
      .map((row) => row.textContent)
    expect(rows).toEqual([
      'mbx_receivingReceiving. Keeps mail 90 days. Google confirmed its Gmail account.Added 6 Mar 2026, receiving since 7 Mar 2026Pause mailbox',
      'mbx_pausedPaused since 12 Nov 2026, 09:30 UTC. Keeps mail 30 days.Added 6 Mar 2026Resume mailbox',
      'mbx_unreadableUnreadable. It accepts no mail.Added 6 Mar 2026',
    ])
    expect(
      section().getByText(
        "The admin panel can't read a mailbox, see its address or label, disconnect it or delete its mail.",
      ),
    ).toBeTruthy()
    expect(document.body.textContent).not.toContain('@inbox.')
    await expectAccessible()
  })

  it('says when the account has none, and when a server does not say how many', async () => {
    await account(record(), [receiving])
    expect(screen.getByRole('region', { name: 'The account' }).textContent).toContain(
      'MailboxesNot said by this server',
    )
    asAdmin()
      .on('GET /api/v1/admin/accounts/acc_eve', ok(record({ mailboxes: 0 })))
      .on(mailboxesOf, ok({ mailboxes: [] }))
    await open('/admin/accounts/acc_eve')
    await screen.findByRole('heading', { level: 1, name: 'eve@example.org' })
    await section().findByText('This account has no mailboxes.')
  })

  it('says when they could not load, and tries again', async () => {
    asAdmin().on('GET /api/v1/admin/accounts/acc_eve', ok(eve)).on(mailboxesOf, network)
    const { user } = await open('/admin/accounts/acc_eve')
    await screen.findByRole('heading', { level: 1, name: 'eve@example.org' })
    const alert = await section().findByText(/The mailboxes couldn't load\./)
    await expectAccessible()
    server.on(mailboxesOf, ok({ mailboxes: [waiting] }))
    await user.click(
      within(alert.parentElement as HTMLElement).getByRole('button', { name: 'Try again' }),
    )
    await section().findByText('mbx_waiting')
    expect(section().getByText(/^Waiting for mail/)).toBeTruthy()
  })

  it('pauses one: asks first, naming the mailbox, confirms it is you, and says it is done', async () => {
    const { user } = await account()
    server.on(
      'POST /api/v1/admin/mailboxes/mbx_receiving/pause',
      once(
        confirmationNeeded,
        ok({ ...receiving, state: 'paused', paused_at: '2026-11-12T10:00:00Z' }),
      ),
    )
    await user.click(section().getAllByRole('button', { name: 'Pause mailbox' })[0] as HTMLElement)
    const question = within(screen.getByRole('dialog', { name: 'Pause this mailbox?' }))
    expect(question.getByText('mbx_receiving')).toBeTruthy()
    expect(
      question.getByText(
        'It refuses new mail until it is resumed. The mail it holds stays, and its owner can still read, delete and disconnect it. They are told by email.',
      ),
    ).toBeTruthy()
    await waitFor(() =>
      expect(document.activeElement).toBe(
        question.getByRole('heading', { name: 'Pause this mailbox?' }),
      ),
    )
    await expectAccessible()
    await user.click(question.getByRole('button', { name: 'Pause mailbox' }))
    await confirm(user)
    await section().findByText('Mailbox paused.')
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Mailboxes' })),
    )
    expect(section().getAllByRole('button', { name: 'Resume mailbox' })).toHaveLength(2)
    expect(server.asked('POST /api/v1/admin/mailboxes/mbx_receiving/pause')).toHaveLength(2)
    await waitFor(() => expect(server.asked('GET /api/v1/admin/stats')).toHaveLength(2))
  })

  it('resumes one', async () => {
    const { user } = await account()
    server.on(
      'POST /api/v1/admin/mailboxes/mbx_paused/resume',
      ok({
        ...paused,
        state: 'receiving',
        paused_at: undefined,
        receiving_since: '2026-03-08T10:00:00Z',
      }),
    )
    await user.click(section().getByRole('button', { name: 'Resume mailbox' }))
    const question = within(screen.getByRole('dialog', { name: 'Resume this mailbox?' }))
    expect(question.getByText('It accepts mail again. Its owner is told by email.')).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Resume mailbox' }))
    await section().findByText('Mailbox resumed.')
    expect(section().queryByRole('button', { name: 'Resume mailbox' })).toBeNull()
  })

  it.each([
    [
      refused(429, 'too_many_requests'),
      'This mailbox was paused or resumed 10 times in the last 30 days. Try again later.',
      1,
    ],
    [refused(404, 'not_found'), "That mailbox isn't there any more.", 2],
    [
      refused(403, 'super_admin_only'),
      "Only the super admin can pause or resume an admin's mailbox.",
      2,
    ],
  ])(
    'says why a pause was refused, and reads the list again when it changed: %s',
    async (reply, words, reads) => {
      const { user } = await account()
      server.on('POST /api/v1/admin/mailboxes/mbx_receiving/pause', reply)
      await user.click(
        section().getAllByRole('button', { name: 'Pause mailbox' })[0] as HTMLElement,
      )
      const question = within(screen.getByRole('dialog', { name: 'Pause this mailbox?' }))
      await user.click(question.getByRole('button', { name: 'Pause mailbox' }))
      await question.findByText(words)
      await settle()
      expect(server.asked(mailboxesOf)).toHaveLength(reads)
      await user.click(question.getByRole('button', { name: 'Not now' }))
      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Pause this mailbox?' })).toBeNull(),
      )
    },
  )

  it("offers an admin nothing on an admin's mailboxes, and the super admin everything", async () => {
    await account(record({ admin: 'admin', mailboxes: 2 }), [receiving, paused], helper)
    expect(
      section().getByText("Only the super admin can pause or resume an admin's mailbox."),
    ).toBeTruthy()
    expect(section().queryAllByRole('button')).toHaveLength(0)
    await account(record({ admin: 'admin', mailboxes: 2 }), [receiving, paused], boss)
    expect(section().getAllByRole('button')).toHaveLength(2)
  })
})

describe('finding a mailbox by its address', () => {
  const first = 'GET /api/v1/admin/accounts?limit=50'
  const reported = 'K7m2q9vx@inbox.permaily.com'
  const findPath = `GET /api/v1/admin/mailboxes?address=${encodeURIComponent(reported)}`

  function finder() {
    return within(screen.getByRole('region', { name: 'Find a mailbox' }))
  }

  it('finds the mailbox and whose it is, and keeps the address nowhere', async () => {
    asAdmin()
      .on(first, ok({ accounts: [], unreadable: 0 }))
      .on(findPath, ok({ mailboxes: [paused] }))
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await user.type(finder().getByLabelText('Its address, exactly'), `  ${reported} `)
    await user.click(finder().getByRole('button', { name: 'Find' }))
    const found = within(await screen.findByLabelText('Mailbox found'))
    expect(found.getByText('mbx_paused')).toBeTruthy()
    expect(found.getByText('Paused')).toBeTruthy()
    expect(found.getByRole('link', { name: 'acc_eve' }).getAttribute('href')).toBe(
      '/admin/accounts/acc_eve',
    )
    expect(screen.getByRole('region', { name: 'Find a mailbox' }).textContent).not.toContain(
      reported,
    )
    expect(server.asked(findPath)).toHaveLength(1)
    expectNotKept(reported, reported.toLowerCase())
    await expectAccessible()
    await user.click(finder().getByRole('button', { name: 'Clear' }))
    expect((finder().getByLabelText('Its address, exactly') as HTMLInputElement).value).toBe('')
    await waitFor(() =>
      expect(document.activeElement).toBe(finder().getByLabelText('Its address, exactly')),
    )
  })

  it('says when no mailbox has the address, without saying it back', async () => {
    asAdmin()
      .on(first, ok({ accounts: [], unreadable: 0 }))
      .on(findPath, ok({ mailboxes: [] }))
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await user.type(finder().getByLabelText('Its address, exactly'), reported)
    await user.click(finder().getByRole('button', { name: 'Find' }))
    await finder().findByText('No mailbox has that address.')
    expect(screen.getByRole('region', { name: 'Find a mailbox' }).textContent).not.toContain(
      reported,
    )
  })

  it('asks for a whole address, and says when it could not look', async () => {
    asAdmin()
      .on(first, ok({ accounts: [], unreadable: 0 }))
      .on(findPath, network)
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    const field = finder().getByLabelText('Its address, exactly')
    await user.type(field, 'k7m2q9vx')
    await user.click(finder().getByRole('button', { name: 'Find' }))
    await finder().findByText('Enter the whole address of the mailbox.')
    await waitFor(() => expect(document.activeElement).toBe(field))
    await user.clear(field)
    await user.type(field, reported)
    await user.click(finder().getByRole('button', { name: 'Find' }))
    await finder().findByText(
      "Couldn't look for it. Permaily can't be reached. Check your connection, then try again.",
    )
    await settle()
    expect(current()).toBe('/admin/accounts')
  })
})

describe('the rest of the panel', () => {
  it('counts the mailboxes with the numbers, when the server does', async () => {
    asAdmin()
      .on('GET /api/v1/admin/stats', ok({ ...stats, mailboxes: { total: 7 } }))
      .on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 0 }))
    await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    const numbers = within(await screen.findByRole('region', { name: 'Numbers' }))
    await waitFor(() =>
      expect(numbers.getByText('Mailboxes').nextElementSibling?.textContent).toBe('7'),
    )

    asAdmin().on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 0 }))
    await open('/admin/accounts')
    await screen.findByRole('region', { name: 'Numbers' })
    expect(screen.queryByText('Mailboxes')).toBeNull()
  })

  it('says in the activity which mailbox was paused or resumed', async () => {
    asAdmin().on(
      'GET /api/v1/admin/log?limit=50',
      ok({
        entries: [
          {
            at: '2026-11-12T10:00:00Z',
            kind: 'mailbox_resumed',
            admin: { id: 'acc_boss', address: 'boss@example.org' },
            account: { id: 'acc_eve', address: 'eve@example.org' },
            mailbox: 'mbx_paused',
          },
          {
            at: '2026-11-12T09:00:00Z',
            kind: 'mailbox_paused',
            admin: { id: 'acc_boss', address: 'boss@example.org' },
            account: { id: 'acc_eve', address: 'eve@example.org' },
            mailbox: 'mbx_paused',
          },
        ],
      }),
    )
    await open('/admin/activity')
    const log = within(await screen.findByRole('region', { name: 'What admins did' }))
    await log.findAllByText('Mailbox')
    expect(log.getAllByRole('listitem').map((row) => row.textContent)).toEqual([
      '12 Nov 2026, 10:00 UTCResumed a mailboxeve@example.orgMailbox mbx_pausedBy boss@example.org',
      '12 Nov 2026, 09:00 UTCPaused a mailboxeve@example.orgMailbox mbx_pausedBy boss@example.org',
    ])
    await expectAccessible()
  })
})

describe('the calls of the admin panel about mailboxes', () => {
  it('reads only what has the shape of a mailbox, and fails on an answer that is not one', async () => {
    server
      .on(
        mailboxesOf,
        ok({ mailboxes: [receiving, null, { id: 'mbx_x', state: 'odd' }, { state: 'paused' }] }),
      )
      .on('GET /api/v1/admin/accounts/acc_bad/mailboxes', ok({}))
      .on('POST /api/v1/admin/mailboxes/mbx_receiving/pause', ok({ id: 'mbx_receiving' }))
      .on('GET /api/v1/admin/stats', ok({ accounts: stats.accounts }))
      .on('GET /api/v1/admin/mailboxes?address=a%40b', ok({ mailboxes: [] }))
    expect((await api.adminAccountMailboxes('acc_eve')).map((one) => one.id)).toEqual([
      'mbx_receiving',
    ])
    await expect(api.adminAccountMailboxes('acc_bad')).rejects.toMatchObject({ code: 'other' })
    await expect(api.adminMailboxAction('mbx_receiving', 'pause')).rejects.toMatchObject({
      code: 'other',
    })
    expect(await api.adminFindMailbox('a@b')).toBeNull()
    const counted = await api.adminStatsWithMailboxes()
    expect(counted.mailboxes).toBeUndefined()
    expect(counted.invitations.unused).toBe(0)
    expect(await api.adminStats()).toEqual({
      accounts: stats.accounts,
      invitations: { unused: 0 },
      complete: true,
    })
    server.on('GET /api/v1/admin/stats', ok(null))
    await expect(api.adminStatsWithMailboxes()).rejects.toMatchObject({ code: 'other' })
    expect(api.accountMailboxes(record() as unknown as api.AccountRecord)).toBeUndefined()
    expect(
      api.logEntryMailbox({ at: '', kind: 'block', admin: { id: 'acc_a', address: '' } }),
    ).toBeUndefined()
  })
})
