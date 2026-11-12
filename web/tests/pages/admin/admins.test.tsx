import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { asAdmin, boss, confirm, helper, record } from '../../helpers/admin.ts'
import { expectNotKept, named, open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { confirmationNeeded, network, ok, once, refused, server } from '../../helpers/server.ts'

const list = 'GET /api/v1/admin/admins'
const both = [
  { id: 'acc_boss', address: 'boss@example.org', name: 'Bea', super: true },
  {
    id: 'acc_helper',
    address: 'helper@example.org',
    name: '',
    super: false,
    since: '2026-10-01T10:00:00Z',
  },
]

async function admins(as = boss) {
  asAdmin(as)
  const opened = await open('/admin/admins')
  await screen.findByRole('heading', { level: 1, name: 'Admins' })
  return opened
}

function who() {
  return within(screen.getByRole('region', { name: 'Who is an admin' }))
}

function adding() {
  return within(screen.getByRole('region', { name: 'Add an admin' }))
}

describe('admins', () => {
  it('lists them, and the super admin can remove one who is not', async () => {
    server
      .on(list, ok({ admins: both }))
      .on(
        'DELETE /api/v1/admin/admins/acc_helper',
        once(confirmationNeeded, ok({ admins: [both[0]] })),
      )
    const { user } = await admins()
    await who().findByText('Admin since 1 Oct 2026')
    const text = screen.getByRole('region', { name: 'Who is an admin' }).textContent
    expect(text).toContain('Super adminboss@example.orgBea')
    expect(text).toContain('Adminhelper@example.orgAdmin since 1 Oct 2026')
    expect(
      who()
        .getAllByRole('button')
        .map((one) => one.textContent),
    ).toEqual(['Remove helper@example.org'])
    await expectAccessible()

    await user.click(who().getByRole('button', { name: named('Remove helper@example.org') }))
    const question = within(screen.getByRole('dialog', { name: 'Remove this admin?' }))
    expect(question.getByText('helper@example.org')).toBeTruthy()
    expect(
      question.getByText('They keep their account, and can no longer use the admin panel.'),
    ).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Remove admin' }))
    await confirm(user)
    await who().findByText('helper@example.org is no longer an admin.')
    // The list is the server's answer to the change: it is not asked for again.
    expect(who().queryByText('Admin since 1 Oct 2026')).toBeNull()
    expect(server.asked(list)).toHaveLength(1)
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Who is an admin' })),
    )
  })

  it('says when the admin to remove is not there', async () => {
    server
      .on(list, ok({ admins: both }))
      .on('DELETE /api/v1/admin/admins/acc_helper', refused(404, 'not_found'))
    const { user } = await admins()
    await user.click(await who().findByRole('button', { name: named('Remove helper@example.org') }))
    const question = within(screen.getByRole('dialog', { name: 'Remove this admin?' }))
    await user.click(question.getByRole('button', { name: 'Remove admin' }))
    await question.findByText("That account isn't there any more.")
  })

  it('shows an admin who is not the super admin the list only', async () => {
    server.on(list, ok({ admins: both }))
    await admins(helper)
    await who().findByText('Admin since 1 Oct 2026')
    expect(who().getByText('Only the super admin can add or remove admins.')).toBeTruthy()
    expect(who().queryAllByRole('button')).toEqual([])
    expect(screen.queryByRole('region', { name: 'Add an admin' })).toBeNull()
  })

  it('says when there are none, or the list could not load', async () => {
    server.on(list, ok({ admins: [] }))
    await admins()
    await who().findByText('There are no admins.')
    server.on(list, network)
    const { user } = await admins()
    const alert = await who().findByRole('alert')
    expect(alert.textContent).toContain("The admins couldn't load.")
    await expectAccessible()
    server.on(list, ok({ admins: both }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await who().findByText('Admin since 1 Oct 2026')
  })

  it('adds one by finding an account by its address', async () => {
    server
      .on(list, ok({ admins: both }))
      .on(
        'GET /api/v1/admin/accounts?address=eve%40example.org',
        ok({ accounts: [record()], unreadable: 0 }),
      )
      .on(
        'PUT /api/v1/admin/admins/acc_eve',
        once(
          confirmationNeeded,
          ok({
            admins: [
              ...both,
              { id: 'acc_eve', address: 'eve@example.org', name: 'Eve', super: false },
            ],
          }),
        ),
      )
    const { user } = await admins()
    const field = adding().getByLabelText('The address of their account')
    await user.type(field, 'eve')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await adding().findByText('Enter the whole address of the account.')
    await user.clear(field)
    await user.type(field, 'eve@example.org')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await adding().findByRole('button', { name: 'Make admin' })
    expect(adding().getByText('Eve')).toBeTruthy()
    await expectAccessible()
    expectNotKept('eve@example.org')

    await user.click(adding().getByRole('button', { name: 'Make admin' }))
    const question = within(screen.getByRole('dialog', { name: 'Make this account an admin?' }))
    expect(
      question.getByText(
        'They can block, delete and sign out accounts and make invitations, while their own account signs in with passkeys only.',
      ),
    ).toBeTruthy()
    await user.click(question.getByRole('button', { name: 'Make admin' }))
    await confirm(user)
    await adding().findByText('eve@example.org is an admin now.')
    expect((field as HTMLInputElement).value).toBe('')
    await who().findByRole('button', { name: named('Remove eve@example.org') })
    expect(server.asked('PUT /api/v1/admin/admins/acc_eve')).toHaveLength(2)
    expect(server.asked(list)).toHaveLength(1)
  })

  it('says when no account has the address, when it is an admin already, and when the search failed', async () => {
    server
      .on(list, ok({ admins: both }))
      .on(
        'GET /api/v1/admin/accounts?address=nobody%40example.org',
        ok({ accounts: [], unreadable: 0 }),
      )
      .on(
        'GET /api/v1/admin/accounts?address=helper%40example.org',
        ok({
          accounts: [record({ id: 'acc_helper', address: 'helper@example.org', admin: 'admin' })],
          unreadable: 0,
        }),
      )
      .on('GET /api/v1/admin/accounts?address=x%40example.org', network)
    const { user } = await admins()
    const field = adding().getByLabelText('The address of their account')
    await user.type(field, 'nobody@example.org')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await adding().findByText(/No account has the address/)
    await user.clear(field)
    await user.type(field, 'helper@example.org')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await adding().findByText('That account is an admin already.')
    expect(adding().queryByRole('button', { name: 'Make admin' })).toBeNull()
    await user.clear(field)
    await user.type(field, 'x@example.org')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await adding().findByText(
      "Couldn't look for it. Permaily can't be reached. Check your connection, then try again.",
    )
  })

  it('says why when making an admin is refused', async () => {
    server
      .on(list, ok({ admins: both }))
      .on(
        'GET /api/v1/admin/accounts?address=eve%40example.org',
        ok({ accounts: [record()], unreadable: 0 }),
      )
      .on('PUT /api/v1/admin/admins/acc_eve', refused(404, 'not_found'))
    const { user } = await admins()
    await user.type(adding().getByLabelText('The address of their account'), 'eve@example.org')
    await user.click(adding().getByRole('button', { name: 'Find' }))
    await user.click(await adding().findByRole('button', { name: 'Make admin' }))
    const question = within(screen.getByRole('dialog', { name: 'Make this account an admin?' }))
    await user.click(question.getByRole('button', { name: 'Make admin' }))
    await question.findByText("That account isn't there any more.")
  })
})
