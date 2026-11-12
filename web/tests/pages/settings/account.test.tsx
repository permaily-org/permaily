import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, network, ok, refused, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

/** Opens Settings as Ana, and waits for the whole page. */
async function settings(account = ana) {
  server.signedIn(account).settings()
  const opened = await open('/settings')
  await screen.findByText('1 device signed in')
  return opened
}

function profile() {
  return within(screen.getByRole('region', { name: 'Profile' }))
}

describe('the profile', () => {
  it('shows the account as the server has it', async () => {
    await settings()
    expect(profile().getByText('Ana')).toBeTruthy()
    expect(profile().getByText('ana@example.org')).toBeTruthy()
    expect(profile().getByText("It can't be changed yet.")).toBeTruthy()
    expect(profile().getByText('English')).toBeTruthy()
    await expectAccessible()
  })

  it('changes the name in place, and everything that shows the account follows', async () => {
    const { user } = await settings()
    const edit = profile().getByRole('button', { name: named('Edit name') })
    await user.click(edit)
    const field = profile().getByLabelText('Name') as HTMLInputElement
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(field.value).toBe('Ana')
    await expectAccessible()

    server.on('PATCH /api/v1/account', ok({ ...ana, name: 'Ana María' }))
    await user.clear(field)
    await user.type(field, '  Ana María  ')
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(profile().getByText('Ana María')).toBeTruthy())
    expect(server.asked('PATCH /api/v1/account')[0].body).toEqual({ name: 'Ana María' })
    expect(profile().getByText('Name changed to Ana María').getAttribute('role')).toBe('status')
    // The account menu follows without a reload.
    expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain('Ana María')
    // Focus is back on the action that opened the field.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        profile().getByRole('button', { name: named('Edit name') }),
      ),
    )
  })

  it('asks nothing when the name is the same, or when the change is cancelled', async () => {
    const { user } = await settings()
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    await user.type(profile().getByLabelText('Name'), ' Other')
    await user.click(profile().getByRole('button', { name: 'Cancel' }))
    expect(profile().getByText('Ana')).toBeTruthy()
    expect(server.asked('PATCH /api/v1/account')).toHaveLength(0)
  })

  it('says before it is sent what the server would not keep', async () => {
    const { user } = await settings()
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    const field = profile().getByLabelText('Name')
    await user.clear(field)
    await user.click(field)
    await user.paste('x'.repeat(81))
    await user.click(profile().getByRole('button', { name: 'Save' }))
    expect(
      profile().getByText('That name has 81 characters. Use 80 or fewer.').getAttribute('role'),
    ).toBe('alert')
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await user.clear(field)
    await user.click(field)
    await user.paste('Ana\tMaría')
    await user.click(profile().getByRole('button', { name: 'Save' }))
    profile().getByText(
      "That name has a character Permaily can't keep, such as a tab. Type it again.",
    )
    expect(server.asked('PATCH /api/v1/account')).toHaveLength(0)
    await expectAccessible()
  })

  it.each([
    [
      refused(400, 'invalid_name'),
      "Permaily can't keep that name. Use one line of 80 characters or fewer.",
    ],
    [network, "Permaily can't be reached. Check your connection, then try again."],
  ])(
    'says under the name what the server answered, and keeps the field open',
    async (answer, words) => {
      const { user } = await settings()
      await user.click(profile().getByRole('button', { name: named('Edit name') }))
      server.on('PATCH /api/v1/account', answer)
      await user.type(profile().getByLabelText('Name'), ' M')
      await user.click(profile().getByRole('button', { name: 'Save' }))
      await waitFor(() => expect(profile().getByText(words)).toBeTruthy())
      await waitFor(() => expect(document.activeElement).toBe(profile().getByLabelText('Name')))
    },
  )

  it('takes the name away, by Remove or by saving it empty', async () => {
    const { user } = await settings()
    server.on('PATCH /api/v1/account', ok({ ...ana, name: '' }))
    await user.click(profile().getByRole('button', { name: named('Remove name') }))
    await waitFor(() => expect(profile().getByText('None')).toBeTruthy())
    expect(server.asked('PATCH /api/v1/account')[0].body).toEqual({ name: '' })
    expect(profile().getByText('Name removed')).toBeTruthy()
    // The action that took it away is gone: focus moves to the one that is left.
    const add = profile().getByRole('button', { name: named('Add a name') })
    await waitFor(() => expect(document.activeElement).toBe(add))
    await settle()
    expect(profile().queryByRole('button', { name: named('Remove name') })).toBeNull()

    server.on('PATCH /api/v1/account', ok({ ...ana, name: 'Ana' }))
    await user.click(add)
    await user.type(profile().getByLabelText('Name'), 'Ana')
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(profile().getByText('Ana')).toBeTruthy())
    server.on('PATCH /api/v1/account', ok({ ...ana, name: '' }))
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    await user.clear(profile().getByLabelText('Name'))
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(profile().getByText('None')).toBeTruthy())
  })

  it('says in the row why the name was not taken away', async () => {
    const { user } = await settings()
    server.on('PATCH /api/v1/account', network)
    await user.click(profile().getByRole('button', { name: named('Remove name') }))
    await waitFor(() =>
      expect(
        profile().getByText("Permaily can't be reached. Check your connection, then try again."),
      ).toBeTruthy(),
    )
    expect(profile().getByText('Ana')).toBeTruthy()
  })

  it('shows only that the account is closing when a change finds that out', async () => {
    // Deleted from another tab or device: this tab learns of it from the refusal.
    const { user } = await settings()
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    await user.type(profile().getByLabelText('Name'), ' M')
    server
      .on('PATCH /api/v1/account', refused(409, 'account_closing'))
      .signedIn({ ...ana, state: 'closing', closes_at: '2026-11-18T14:32:00Z' })
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    expect(current()).toBe('/settings')
  })

  it('leads to the sign-in screen when a change finds the session ended', async () => {
    const { user } = await settings()
    await user.click(profile().getByRole('button', { name: named('Edit name') }))
    await user.type(profile().getByLabelText('Name'), ' M')
    server.on('PATCH /api/v1/account', refused(401, 'unauthorized')).signedOut()
    await user.click(profile().getByRole('button', { name: 'Save' }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toEqual({ from: '/settings' })
  })
})

describe('deleting the account', () => {
  /** Opens the dialog and returns it. */
  async function ask(user: User) {
    await user.click(screen.getByRole('button', { name: 'Delete my account' }))
    return within(screen.getByRole('dialog', { name: 'Delete your account?' }))
  }

  it('asks first, and says what happens before the server hears anything', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-11T15:00:00Z') })
    const { user } = await settings()
    const dialog = await ask(user)
    // Focus opens on the question: nothing here is the default.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        dialog.getByRole('heading', { name: 'Delete your account?' }),
      ),
    )
    expect(dialog.getByText(/stops working as soon as you confirm/)).toBeTruthy()
    expect(dialog.getByText('Wednesday, 18 November 2026').getAttribute('datetime')).toBe(
      '2026-11-18T15:00:00.000Z',
    )
    expect(dialog.getByText(/nothing can bring it back/)).toBeTruthy()
    // Keeping the account comes before deleting it.
    expect(dialog.getAllByRole('button').map((button) => button.textContent)).toEqual([
      'Keep my account',
      'Delete my account',
    ])
    expect(server.asked('POST /api/v1/account/delete')).toHaveLength(0)
    await expectAccessible()
  })

  it('deletes nothing with Enter, and nothing without the address of the account', async () => {
    const { user } = await settings()
    const dialog = await ask(user)
    const field = dialog.getByLabelText('Address of this account')
    await user.click(field)
    await user.keyboard('{Enter}')
    dialog.getByText('Type the address of this account to delete it.')
    await user.type(field, 'eve@example.org{Enter}')
    dialog.getByText("That isn't this account's address. Type it as it is written above.")
    await user.click(dialog.getByRole('button', { name: 'Delete my account' }))
    await waitFor(() => expect(document.activeElement).toBe(field))
    // Even the right address, with Enter, only checks what was typed.
    await user.clear(field)
    await user.type(field, 'ana@example.org{Enter}')
    expect(dialog.queryByText(/isn't this account's address/)).toBeNull()
    expect(server.asked('POST /api/v1/account/delete')).toHaveLength(0)
    await expectAccessible()
  })

  it('closes the account once the address is typed, in any case, and shows only that', async () => {
    const { user } = await settings()
    const dialog = await ask(user)
    server.on(
      'POST /api/v1/account/delete',
      ok({ ...ana, state: 'closing', closes_at: '2026-11-18T14:32:00Z' }),
    )
    await user.type(dialog.getByLabelText('Address of this account'), ' Ana@Example.ORG ')
    await user.click(dialog.getByRole('button', { name: 'Delete my account' }))
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    expect(server.asked('POST /api/v1/account/delete')).toHaveLength(1)
    expect(current()).toBe('/settings')
    await settle()
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
  })

  it('says what is and is not known when the server gives no answer, or refuses', async () => {
    const { user } = await settings()
    const dialog = await ask(user)
    await user.type(dialog.getByLabelText('Address of this account'), 'ana@example.org')
    server.on('POST /api/v1/account/delete', network)
    await user.click(dialog.getByRole('button', { name: 'Delete my account' }))
    await waitFor(() =>
      dialog.getByText(
        "Permaily can't be reached, so it isn't known whether your account is being deleted. Check your connection, then try again.",
      ),
    )
    server.on('POST /api/v1/account/delete', refused(429, 'too_many_requests'))
    await user.click(dialog.getByRole('button', { name: 'Delete my account' }))
    await waitFor(() =>
      dialog.getByText(
        "Your account isn't being deleted. Too many attempts. Wait a few minutes, then try again.",
      ),
    )
  })

  it('keeps the account when the person says so, and starts clean the next time', async () => {
    const { user } = await settings()
    let dialog = await ask(user)
    await user.type(dialog.getByLabelText('Address of this account'), 'eve')
    await user.click(dialog.getByRole('button', { name: 'Keep my account' }))
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    dialog = await ask(user)
    expect((dialog.getByLabelText('Address of this account') as HTMLInputElement).value).toBe('')
    expect(server.asked('POST /api/v1/account/delete')).toHaveLength(0)
  })
})
