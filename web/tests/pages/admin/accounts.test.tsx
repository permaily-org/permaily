import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { asAdmin, boss, cancelConfirm, confirm, helper, record } from '../../helpers/admin.ts'
import { current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  confirmationNeeded,
  later,
  network,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

const first = 'GET /api/v1/admin/accounts?limit=50'
const eve = record()
const blocked = record({
  id: 'acc_bo',
  address: 'bo@example.org',
  name: '',
  blocked_at: '2026-11-01T09:00:00Z',
})
const closing = record({
  id: 'acc_cy',
  address: 'cy@example.org',
  state: 'closing',
  closes_at: '2026-11-18T14:32:00Z',
  closing_by: 'person',
})
const both = record({
  ...closing,
  id: 'acc_di',
  address: 'di@example.org',
  blocked_at: '2026-11-02T09:00:00Z',
})

function list() {
  return within(screen.getByRole('region', { name: 'Every account' }))
}

describe('the list of accounts', () => {
  it('lists every account with its state in words, and says how many could not be read', async () => {
    asAdmin().on(first, ok({ accounts: [eve, blocked, closing, both], unreadable: 2 }))
    await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await screen.findByRole('link', { name: 'eve@example.org' })
    const rows = list()
      .getAllByRole('listitem')
      .map((row) => row.textContent)
    expect(rows).toEqual([
      'eve@example.orgEveActiveSince 6 Mar 2026',
      'bo@example.orgNo nameBlockedSince 6 Mar 2026',
      'cy@example.orgEveBeing deletedSince 6 Mar 2026',
      'di@example.orgEveBlocked, being deletedSince 6 Mar 2026',
    ])
    expect(list().getByRole('link', { name: 'eve@example.org' }).getAttribute('href')).toBe(
      '/admin/accounts/acc_eve',
    )
    expect(list().getByText("2 accounts couldn't be read, and aren't listed.")).toBeTruthy()
    expect(list().queryByRole('button', { name: 'Show more accounts' })).toBeNull()
    await expectAccessible()
  })

  it('pages, the oldest first, and says when a page could not load', async () => {
    asAdmin()
      .on(first, ok({ accounts: [eve], unreadable: 1, next: 'cur_2' }))
      .on(`${first}&after=cur_2`, network)
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    const more = await list().findByRole('button', { name: 'Show more accounts' })
    await user.click(more)
    await list().findByText(
      "More accounts couldn't load. Permaily can't be reached. Check your connection, then try again.",
    )
    server.on(`${first}&after=cur_2`, ok({ accounts: [blocked], unreadable: 0 }))
    await user.click(more)
    await list().findByRole('link', { name: 'bo@example.org' })
    expect(list().getAllByRole('listitem')).toHaveLength(2)
    expect(list().getByText("1 account couldn't be read, and isn't listed.")).toBeTruthy()
    expect(list().queryByRole('button', { name: 'Show more accounts' })).toBeNull()
    expect(list().queryByRole('alert')).toBeNull()
  })

  it('says when there are none, and when the list could not load', async () => {
    asAdmin().on(first, ok({ accounts: [], unreadable: 0 }))
    await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await list().findByText('There are no accounts.')

    const loading = later()
    server.on(first, loading.answer)
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await list().findByText('Loading')
    await expectAccessible()
    loading.give(refused(429, 'too_many_requests'))
    const alert = await list().findByRole('alert')
    expect(alert.textContent).toContain(
      "The accounts couldn't load. Too many attempts. Wait a few minutes, then try again.",
    )
    await expectAccessible()
    server.on(first, ok({ accounts: [eve], unreadable: 0 }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await list().findByRole('link', { name: 'eve@example.org' })
  })

  it('finds one by its exact address, which is kept nowhere', async () => {
    asAdmin()
      .on(first, ok({ accounts: [], unreadable: 0 }))
      .on(
        'GET /api/v1/admin/accounts?address=eve%40example.org',
        ok({ accounts: [eve], unreadable: 0 }),
      )
      .on(
        'GET /api/v1/admin/accounts?address=nobody%40example.org',
        ok({ accounts: [], unreadable: 0 }),
      )
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    const find = within(screen.getByRole('region', { name: 'Find an account' }))
    const field = find.getByLabelText('Its address, exactly')
    await user.type(field, 'eve')
    await user.click(find.getByRole('button', { name: 'Find' }))
    await find.findByText('Enter the whole address of the account.')
    expect(document.activeElement).toBe(field)

    await user.clear(field)
    await user.type(field, ' eve@example.org ')
    await user.click(find.getByRole('button', { name: 'Find' }))
    const found = await find.findByRole('link', { name: 'eve@example.org' })
    expect(found.getAttribute('href')).toBe('/admin/accounts/acc_eve')
    await expectAccessible()
    expectNotKept('eve@example.org')

    await user.clear(field)
    await user.type(field, 'nobody@example.org')
    await user.click(find.getByRole('button', { name: 'Find' }))
    await find.findByText(/No account has the address/)
    expect(find.getByText('nobody@example.org')).toBeTruthy()
    expectNotKept('nobody@example.org')

    await user.click(find.getByRole('button', { name: 'Clear' }))
    expect((field as HTMLInputElement).value).toBe('')
    expect(document.activeElement).toBe(field)
    expect(find.queryByText(/No account has the address/)).toBeNull()

    server.on('GET /api/v1/admin/accounts?address=x%40example.org', network)
    await user.type(field, 'x@example.org')
    await user.click(find.getByRole('button', { name: 'Find' }))
    await find.findByText(
      "Couldn't look for it. Permaily can't be reached. Check your connection, then try again.",
    )
  })
})

describe('one account', () => {
  /** Opens an account's page and waits for it. */
  async function account(shown: Record<string, unknown> = eve, as = boss) {
    asAdmin(as).on(`GET /api/v1/admin/accounts/${shown.id}`, ok(shown))
    const opened = await open(`/admin/accounts/${shown.id}`)
    await screen.findByRole('heading', { level: 1, name: String(shown.address) })
    return opened
  }

  function actions() {
    return within(screen.getByRole('region', { name: 'Actions' }))
  }

  it("shows everything the panel knows of it, in words and the person's own dates", async () => {
    await account(record({ admin: 'admin', passkey_only: true }), boss)
    const facts = within(screen.getByRole('region', { name: 'The account' }))
    const text = facts.getByRole('heading', { name: 'The account' }).parentElement?.parentElement
      ?.textContent
    expect(text).toContain('NameEve')
    expect(text).toContain('StateActive')
    expect(text).toContain('Created6 Mar 2026')
    expect(text).toContain('Address checked6 Mar 2026')
    expect(text).toContain('LanguageEnglish')
    expect(text).toContain('Passkeys2Passkey only on')
    expect(text).toContain('ProvidersGoogle, Facebook')
    expect(text).toContain('Devices3 signed in')
    expect(text).toContain('AdminAdmin')
    expect(text).toContain('Idacc_eve')
    expect(
      actions().getByText(
        "The admin panel can't change an account's address or how it signs in, and can't sign in as its owner.",
      ),
    ).toBeTruthy()
    expect(
      actions()
        .getAllByRole('button')
        .map((button) => button.textContent),
    ).toEqual(['Block account', 'Delete account', 'Sign out every device'])
    await expectAccessible()
  })

  it('says what is not known, and the closing day of an account being deleted', async () => {
    await account(
      record({
        ...both,
        name: '',
        address_verified_at: '',
        language: '',
        passkeys: 0,
        providers: [],
        sessions: 1,
        closing_by: 'operator',
      }),
    )
    const text = screen.getByRole('region', { name: 'The account' }).textContent
    expect(text).toContain('NameNone given')
    expect(text).toContain('Blocked, being deleted')
    expect(text).toContain('Blocked since 2 Nov 2026, 09:00 UTC')
    expect(text).toContain('Removed on Wednesday, 18 November 2026. An admin asked for it.')
    expect(text).toContain('Address checkedNot yet')
    expect(text).toContain('PasskeysNonePasskey only off')
    expect(text).toContain('ProvidersNone linked')
    expect(text).toContain('Devices1 signed in')
    expect(
      actions()
        .getAllByRole('button')
        .map((button) => button.textContent),
    ).toEqual(['Unblock account', 'Cancel the deletion', 'Sign out every device'])
  })

  it('offers no cancel for a deletion the person asked for, and says so', async () => {
    await account(record({ ...closing, sessions: 0 }))
    expect(screen.getByRole('region', { name: 'The account' }).textContent).toContain(
      'The person asked for it.',
    )
    expect(
      actions().getByText('The person asked for this deletion: only they can cancel it.'),
    ).toBeTruthy()
    expect(
      actions()
        .getAllByRole('button')
        .map((button) => button.textContent),
    ).toEqual(['Block account'])
  })

  it.each([
    [
      'Block account',
      'Block this account?',
      'block',
      'Every device signed in to it is signed out, and nobody can sign in to it until it is unblocked.',
      { blocked_at: '2026-11-12T10:00:00Z' },
      'Account blocked.',
    ],
    [
      'Sign out every device',
      'Sign out every device?',
      'sessions/close',
      'Every device signed in to it is signed out. The person can sign in again.',
      { sessions: 0 },
      'Every device was signed out.',
    ],
  ])(
    '%s: asks first, naming the account, confirms it is you, and says it is done',
    async (label, title, path, says, after, done) => {
      const { user } = await account()
      server.on(
        `POST /api/v1/admin/accounts/acc_eve/${path}`,
        once(confirmationNeeded, ok(record(after))),
      )
      await user.click(actions().getByRole('button', { name: label }))
      const question = within(screen.getByRole('dialog', { name: title }))
      expect(question.getByText('eve@example.org')).toBeTruthy()
      expect(question.getByText(says)).toBeTruthy()
      await waitFor(() =>
        expect(document.activeElement).toBe(question.getByRole('heading', { name: title })),
      )
      await expectAccessible()
      await user.click(question.getByRole('button', { name: label }))
      await confirm(user)
      await actions().findByText(done)
      await waitFor(() =>
        expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Actions' })),
      )
      expect(screen.queryByRole('dialog', { name: title })).toBeNull()
      expect(server.asked(`POST /api/v1/admin/accounts/acc_eve/${path}`)).toHaveLength(2)
      // The numbers on top are read again.
      await waitFor(() => expect(server.asked('GET /api/v1/admin/stats')).toHaveLength(2))
    },
  )

  it('asks to delete, with the day it would be removed', async () => {
    const { user } = await account()
    server.on(
      'POST /api/v1/admin/accounts/acc_eve/delete',
      ok(record({ state: 'closing', closes_at: '2026-11-19T10:00:00Z', closing_by: 'operator' })),
    )
    await user.click(actions().getByRole('button', { name: 'Delete account' }))
    const question = within(screen.getByRole('dialog', { name: 'Delete this account?' }))
    expect(
      question.getByText(
        /^It stops working now and is removed for good in 7 days, on .+\. Until then the person can sign in and cancel, unless it is blocked\.$/,
      ),
    ).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Delete account' }))
    await actions().findByText('Deletion asked for.')
    expect(actions().getByRole('button', { name: 'Cancel the deletion' })).toBeTruthy()
  })

  it('unblocks, and cancels a deletion it asked for', async () => {
    const { user } = await account(record({ ...both, closing_by: 'operator' }))
    server
      .on(
        'POST /api/v1/admin/accounts/acc_di/unblock',
        ok(record({ ...closing, id: 'acc_di', address: 'di@example.org', closing_by: 'operator' })),
      )
      .on(
        'POST /api/v1/admin/accounts/acc_di/delete/cancel',
        ok(record({ id: 'acc_di', address: 'di@example.org' })),
      )
    await user.click(actions().getByRole('button', { name: 'Unblock account' }))
    let question = within(screen.getByRole('dialog', { name: 'Unblock this account?' }))
    expect(
      question.getByText(
        'It can be signed in to again. Its devices stay signed out until the person signs in.',
      ),
    ).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Unblock account' }))
    await actions().findByText('Account unblocked.')
    await user.click(actions().getByRole('button', { name: 'Cancel the deletion' }))
    question = within(screen.getByRole('dialog', { name: 'Cancel this deletion?' }))
    expect(question.getByText('The account is kept, and works as it did before.')).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Cancel the deletion' }))
    await actions().findByText('Deletion cancelled.')
    expect(screen.getByRole('region', { name: 'The account' }).textContent).toContain('StateActive')
  })

  it('says when only the person can cancel, and reads the account again', async () => {
    const { user } = await account(record({ ...closing, closing_by: 'operator' }))
    server.on('POST /api/v1/admin/accounts/acc_cy/delete/cancel', refused(409, 'not_cancellable'))
    server.on('GET /api/v1/admin/accounts/acc_cy', ok(closing))
    await user.click(actions().getByRole('button', { name: 'Cancel the deletion' }))
    const question = within(screen.getByRole('dialog', { name: 'Cancel this deletion?' }))
    await user.click(question.getByRole('button', { name: 'Cancel the deletion' }))
    await question.findByText('The person asked for this deletion, so only they can cancel it.')
    await screen.findByText('The person asked for this deletion: only they can cancel it.')
  })

  it("offers nothing on another admin's account to an admin who is not the super admin", async () => {
    await account(record({ admin: 'admin' }), helper)
    expect(actions().getByText("Only the super admin can act on an admin's account.")).toBeTruthy()
    expect(actions().queryAllByRole('button')).toEqual([])
  })

  it('says why when the server refuses an admin, and reads the account again', async () => {
    const { user } = await account(eve, helper)
    server
      .on('POST /api/v1/admin/accounts/acc_eve/block', refused(403, 'super_admin_only'))
      .on('GET /api/v1/admin/accounts/acc_eve', ok(record({ admin: 'admin' })))
    await user.click(actions().getByRole('button', { name: 'Block account' }))
    const question = within(screen.getByRole('dialog', { name: 'Block this account?' }))
    await user.click(question.getByRole('button', { name: 'Block account' }))
    await question.findByText("Only the super admin can act on an admin's account.")
    await user.click(question.getByRole('button', { name: 'Not now' }))
    await actions().findByText("Only the super admin can act on an admin's account.")
  })

  it("offers nothing on the admin's own account, and leads to Settings", async () => {
    await account(record({ id: 'acc_boss', address: 'boss@example.org', admin: 'super' }))
    expect(actions().getByText(/This is your own account\./)).toBeTruthy()
    expect(actions().getByRole('link', { name: 'Settings' }).getAttribute('href')).toBe('/settings')
    expect(actions().queryAllByRole('button')).toEqual([])
  })

  it('leaves everything as it was when the person does not confirm', async () => {
    const { user } = await account()
    server.on('POST /api/v1/admin/accounts/acc_eve/block', confirmationNeeded)
    await user.click(actions().getByRole('button', { name: 'Block account' }))
    const question = within(screen.getByRole('dialog', { name: 'Block this account?' }))
    await user.click(question.getByRole('button', { name: 'Block account' }))
    await cancelConfirm(user)
    expect(question.getByRole('alert').textContent).toBe('')
    expect(
      question.getByRole('button', { name: 'Block account' }).getAttribute('aria-disabled'),
    ).toBe('false')
    expect(screen.getByRole('region', { name: 'The account' }).textContent).toContain('StateActive')
  })

  it('says when an account is not there, or could not load', async () => {
    asAdmin().on('GET /api/v1/admin/accounts/acc_gone', refused(404, 'not_found'))
    await open('/admin/accounts/acc_gone')
    await screen.findByRole('heading', { level: 1, name: 'Account not found' })
    expect(screen.getByText("That account isn't there any more.")).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Back to accounts' }).getAttribute('href')).toBe(
      '/admin/accounts',
    )
    await expectAccessible()

    server.on('GET /api/v1/admin/accounts/acc_eve', network)
    const { user } = await open('/admin/accounts/acc_eve')
    const alert = await screen.findByText(/This account couldn't load\./)
    await expectAccessible()
    server.on('GET /api/v1/admin/accounts/acc_eve', ok(eve))
    await user.click(
      within(alert.parentElement as HTMLElement).getByRole('button', { name: 'Try again' }),
    )
    await screen.findByRole('heading', { level: 1, name: 'eve@example.org' })
    await settle()
    expect(current()).toBe('/admin/accounts/acc_eve')
  })
})
