import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, expectNotKept, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, later, network, ok, refused, server } from '../../helpers/server.ts'

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
    expect(profile().getByRole('button', { name: named('Change contact email') })).toBeTruthy()
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

describe('the contact address', () => {
  /** What the account menu shows: the dialog, while it asks for the new address, names the old one too. */
  function menuAddress() {
    return screen.getByRole('button', { name: /Account menu/ }).textContent
  }

  const started = ok({ attempt: 'att_change_1', expires_in: 600 })
  const moved = { ...ana, address: 'ana@new.example' }
  const confirmation =
    'Contact email changed. Permaily sent a notice to the old address and to the new one.'

  /** Opens the dialog and returns it. */
  async function ask(user: User) {
    await user.click(profile().getByRole('button', { name: named('Change contact email') }))
    return within(screen.getByRole('dialog', { name: 'Change your contact email' }))
  }

  /** Opens the dialog, asks for the code and returns the step that takes it. */
  async function sent(user: User, address = 'ana@new.example') {
    server.on('POST /api/v1/account/address/start', started)
    const dialog = await ask(user)
    await user.type(dialog.getByLabelText('New email address'), address)
    await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    await screen.findByRole('dialog', { name: 'Check your mail' })
    return within(screen.getByRole('dialog', { name: 'Check your mail' }))
  }

  it('says before anything is asked that a message goes to the new address', async () => {
    const { user } = await settings()
    const dialog = await ask(user)
    const field = dialog.getByLabelText('New email address') as HTMLInputElement
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(field.value).toBe('')
    expect(dialog.getByText(/Your contact email is/).textContent).toBe(
      'Your contact email is ana@example.org. Permaily writes to you there, and it is the address you sign in with.',
    )
    // The field reads it when it takes focus.
    const how = dialog.getByText(
      'Permaily sends a message with a code to the new address, to check that it is yours. Your contact email changes only when you enter that code.',
    )
    expect(field.getAttribute('aria-describedby')).toBe(how.id)
    expect(server.asked('POST /api/v1/account/address/start')).toHaveLength(0)
    await expectAccessible()
  })

  it('changes it with the code of the message, everywhere the app shows it, and says both were told', async () => {
    const { user } = await settings()
    const change = profile().getByRole('button', { name: named('Change contact email') })
    const step = await sent(user, '  ana@new.example ')
    expect(server.asked('POST /api/v1/account/address/start')[0].body).toEqual({
      address: 'ana@new.example',
    })
    expect(step.getByText(/A message with a code is on its way to/).textContent).toBe(
      'A message with a code is on its way to ana@new.example. It works for 10 minutes. Enter the code to make that address your contact email.',
    )
    const code = step.getByLabelText('Code from the message') as HTMLInputElement
    await waitFor(() => expect(document.activeElement).toBe(code))
    expect(code.getAttribute('autocomplete')).toBe('one-time-code')
    // Nothing has changed yet.
    expect(profile().getByText('ana@example.org')).toBeTruthy()
    await expectAccessible()

    server.on('POST /api/v1/account/address/finish', ok(moved))
    await user.click(code)
    await user.paste('123 456')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(server.asked('POST /api/v1/account/address/finish')[0].body).toEqual({
      attempt: 'att_change_1',
      code: '123456',
    })
    // The row, the account menu and the question that deleting asks all have the new address.
    expect(profile().getByText('ana@new.example')).toBeTruthy()
    expect(profile().queryByText('ana@example.org')).toBeNull()
    expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain(
      'ana@new.example',
    )
    expect(profile().getByText(confirmation).getAttribute('role')).toBe('status')
    // Focus is back on the action that opened the dialog.
    await waitFor(() => expect(document.activeElement).toBe(change))
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: 'Delete my account' }))
    expect(
      within(screen.getByRole('dialog', { name: 'Delete your account?' })).getByText(
        /To go on, type the address of this account/,
      ).textContent,
    ).toContain('ana@new.example')

    // Nothing of it is anywhere but the page's memory.
    expectNotKept('123456', 'att_change_1', 'ana@new.example', 'ana@example.org')
    expect({ ...localStorage }).toEqual({})
    expect({ ...sessionStorage }).toEqual({})
    expect(current()).toBe('/settings')
  })

  it('says the confirmation once: not after a reload, and not once another change starts', async () => {
    const { user } = await settings()
    const step = await sent(user)
    server.on('POST /api/v1/account/address/finish', ok(moved))
    await user.type(step.getByLabelText('Code from the message'), '123456')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    await waitFor(() => expect(profile().getByText(confirmation)).toBeTruthy())
    const dialog = await ask(user)
    expect(profile().queryByText(confirmation)).toBeNull()
    expect(dialog.getByText(/Your contact email is/).textContent).toContain('ana@new.example')

    await settings(moved)
    await settle()
    expect(profile().queryByText(confirmation)).toBeNull()
    expect(profile().getByText('ana@new.example')).toBeTruthy()
  })

  it('says what cannot be the new address before anything is asked', async () => {
    const { user } = await settings()
    const dialog = await ask(user)
    const field = dialog.getByLabelText('New email address')
    await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    expect(dialog.getByText('Enter the new email address.').getAttribute('role')).toBe('alert')
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await waitFor(() => expect(document.activeElement).toBe(field))
    // The address the account has already, in any case.
    await user.type(field, ' Ana@Example.ORG{Enter}')
    dialog.getByText('That is your contact email already.')
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(server.asked('POST /api/v1/account/address/start')).toHaveLength(0)
    await expectAccessible()
  })

  it.each([
    [
      refused(400, 'invalid_address'),
      "Permaily can't use that address. Check it for typos, and that it isn't the one this account has already.",
      'new-address-error',
    ],
    [
      refused(429, 'too_many_requests'),
      'No message was sent. Too many attempts. Wait a few minutes, then try again.',
      '',
    ],
    [
      network,
      "No message was sent: Permaily can't be reached. Check your connection, then try again.",
      '',
    ],
    [
      refused(500, 'internal'),
      'No message was sent. Something went wrong on our side. Try again in a moment.',
      '',
    ],
  ])('says why no code was sent, and keeps what was typed', async (answer, words, id) => {
    const { user } = await settings()
    const dialog = await ask(user)
    server.on('POST /api/v1/account/address/start', answer)
    const field = dialog.getByLabelText('New email address') as HTMLInputElement
    await user.type(field, 'ana@new.example')
    await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    const said = await dialog.findByText(words)
    expect(said.getAttribute('role')).toBe('alert')
    expect(said.id).toBe(id)
    expect(field.value).toBe('ana@new.example')
    await waitFor(() => expect(document.activeElement).toBe(field))
    // The form can be sent again.
    expect(
      (dialog.getByRole('button', { name: 'Send the code' }) as HTMLButtonElement).disabled,
    ).toBe(false)
    expect(menuAddress()).toContain('ana@example.org')
    await expectAccessible()
  })

  it('handles a wrong code, too many tries and no answer as signing in does', async () => {
    const { user } = await settings()
    const step = await sent(user)
    const code = step.getByLabelText('Code from the message')
    await user.type(code, '123')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    step.getByText('Enter the 6 digits of the code.')
    expect(server.asked('POST /api/v1/account/address/finish')).toHaveLength(0)

    server.on('POST /api/v1/account/address/finish', refused(400, 'invalid_code'))
    await user.type(code, '456')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    const wrong = await step.findByText(
      "That code isn't right, or it has expired. Check it, or send another message.",
    )
    expect(wrong.getAttribute('role')).toBe('alert')
    await waitFor(() => expect(document.activeElement).toBe(code))
    await expectAccessible()

    server.on('POST /api/v1/account/address/finish', refused(429, 'too_many_requests'))
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    await step.findByText('Too many attempts. Wait a few minutes, then try again.')

    server.on('POST /api/v1/account/address/finish', network)
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    await step.findByText("Permaily can't be reached. Check your connection, then try again.")
    // The address is the one it was.
    expect(profile().getByText('ana@example.org')).toBeTruthy()
    expect(profile().queryByText(confirmation)).toBeNull()
    await expectAccessible()
  })

  it('says when the code ran out, and sends another message to the same address', async () => {
    const { user } = await settings()
    server.on('POST /api/v1/account/address/start', ok({ attempt: 'att_old', expires_in: 0 }))
    const dialog = await ask(user)
    await user.type(dialog.getByLabelText('New email address'), 'ana@new.example')
    await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    const step = within(await screen.findByRole('dialog', { name: 'Check your mail' }))
    step.getByText(/It has expired by now\./)
    await expectAccessible()

    server.on('POST /api/v1/account/address/start', ok({ attempt: 'att_new', expires_in: 60 }))
    await user.click(step.getByRole('button', { name: 'Send another message' }))
    const note = await step.findByText(
      'Asked for another message. Use the code or the link of the newest one.',
    )
    expect(note.getAttribute('role')).toBe('status')
    step.getByText(/It works for 1 minute\./)
    expect(server.asked('POST /api/v1/account/address/start').map((call) => call.body)).toEqual([
      { address: 'ana@new.example' },
      { address: 'ana@new.example' },
    ])
    // The code is entered against the newest message.
    server.on('POST /api/v1/account/address/finish', ok(moved))
    await user.type(step.getByLabelText('Code from the message'), '654321')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    await waitFor(() => expect(profile().getByText(confirmation)).toBeTruthy())
    expect(server.asked('POST /api/v1/account/address/finish')[0].body).toEqual({
      attempt: 'att_new',
      code: '654321',
    })

    // Another message that cannot be sent is said in the step, which stays.
    const again = await sent(user, 'ana@third.example')
    server.on('POST /api/v1/account/address/start', refused(429, 'too_many_requests'))
    await user.click(again.getByRole('button', { name: 'Send another message' }))
    await again.findByText('Too many attempts. Wait a few minutes, then try again.')
    expect(again.getByLabelText('Code from the message')).toBeTruthy()
  })

  it('says only after the code that the address belongs to another account', async () => {
    const { user } = await settings()
    const step = await sent(user, 'eve@example.org')
    server.on('POST /api/v1/account/address/finish', refused(409, 'address_in_use'))
    await user.type(step.getByLabelText('Code from the message'), '123456')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    // Back where the address is typed, with what was typed, to use another.
    const dialog = within(await screen.findByRole('dialog', { name: 'Change your contact email' }))
    const said = dialog.getByText(
      "That address belongs to another Permaily account, so it can't be this one's contact email. Use another address.",
    )
    expect(said.getAttribute('role')).toBe('alert')
    const field = dialog.getByLabelText('New email address') as HTMLInputElement
    expect(field.value).toBe('eve@example.org')
    expect(field.getAttribute('aria-invalid')).toBe('true')
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(menuAddress()).toContain('ana@example.org')
    await expectAccessible()
    expectNotKept('123456', 'att_change_1', 'eve@example.org')
  })

  it('goes back from the code for another address, and starts clean the next time', async () => {
    const { user } = await settings()
    const step = await sent(user)
    await user.click(step.getByRole('button', { name: 'Use another address' }))
    let dialog = within(await screen.findByRole('dialog', { name: 'Change your contact email' }))
    const field = dialog.getByLabelText('New email address') as HTMLInputElement
    expect(field.value).toBe('ana@new.example')
    await waitFor(() => expect(document.activeElement).toBe(field))
    await user.click(dialog.getByRole('button', { name: 'Keep this address' }))
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(server.asked('POST /api/v1/account/address/finish')).toHaveLength(0)
    dialog = await ask(user)
    expect((dialog.getByLabelText('New email address') as HTMLInputElement).value).toBe('')
    expect(menuAddress()).toContain('ana@example.org')
  })

  it('cannot be closed while the code is on its way to the server', async () => {
    const { user } = await settings()
    const step = await sent(user)
    const finishing = later()
    server.on('POST /api/v1/account/address/finish', finishing.answer)
    await user.type(step.getByLabelText('Code from the message'), '123456')
    await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    const dialog = screen.getByRole('dialog', { name: 'Check your mail' })
    // Escape leaves it open: the answer is still to come.
    dialog.dispatchEvent(new Event('cancel', { cancelable: true }))
    await settle()
    expect(screen.getByRole('dialog', { name: 'Check your mail' })).toBeTruthy()
    finishing.give(ok(moved))
    await waitFor(() => expect(profile().getByText(confirmation)).toBeTruthy())
  })

  it.each([
    ['asking for the code', 'POST /api/v1/account/address/start'],
    ['the code', 'POST /api/v1/account/address/finish'],
  ])('shows only that the account is closing when %s finds that out', async (_, route) => {
    const { user } = await settings()
    const closing = { ...ana, state: 'closing' as const, closes_at: '2026-11-18T14:32:00Z' }
    const refusal = () => {
      server.signedIn(closing)
      return refused(409, 'account_closing')
    }
    if (route.endsWith('start')) {
      const dialog = await ask(user)
      server.on(route, refusal)
      await user.type(dialog.getByLabelText('New email address'), 'ana@new.example')
      await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    } else {
      const step = await sent(user)
      server.on(route, refusal)
      await user.type(step.getByLabelText('Code from the message'), '123456')
      await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    }
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    expect(current()).toBe('/settings')
    expectNotKept('ana@new.example')
  })

  it.each([
    ['asking for the code', 'POST /api/v1/account/address/start'],
    ['the code', 'POST /api/v1/account/address/finish'],
    ['another message', 'again'],
  ])('leads to the sign-in screen when %s finds the session ended', async (_, route) => {
    const { user } = await settings()
    const refusal = () => {
      server.signedOut()
      return refused(401, 'unauthorized')
    }
    if (route.endsWith('start')) {
      const dialog = await ask(user)
      server.on(route, refusal)
      await user.type(dialog.getByLabelText('New email address'), 'ana@new.example')
      await user.click(dialog.getByRole('button', { name: 'Send the code' }))
    } else if (route === 'again') {
      const step = await sent(user)
      server.on('POST /api/v1/account/address/start', refusal)
      await user.click(step.getByRole('button', { name: 'Send another message' }))
    } else {
      const step = await sent(user)
      server.on(route, refusal)
      await user.type(step.getByLabelText('Code from the message'), '123456')
      await user.click(step.getByRole('button', { name: 'Confirm new address' }))
    }
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toEqual({ from: '/settings' })
    expectNotKept('ana@new.example', 'att_change_1', '123456')
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
