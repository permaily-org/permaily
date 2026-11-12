import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { expectNotKept, named, open, settle, watchLeaving } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { answered, browserWithPasskeys, creationOptions } from '../../helpers/passkeys.ts'
import { ana, confirmationNeeded, meOf, ok, once, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']
type Has = NonNullable<Parameters<typeof server.settings>[0]>

const phone = { id: 'cred-phone', name: 'Phone' }
const key = { id: 'cred-key', name: 'Security key' }
const codes = Array.from({ length: 10 }, (_, n) => `CODE-${n}000-AAAA-BBB${n}`)

/** Opens Settings as Ana, with an account that confirms by mail. */
async function settings(has: Has = {}) {
  server
    .signedIn()
    .settings(has)
    .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_confirm', expires_in: 600 }))
    .on(
      'POST /api/v1/auth/confirm/email/finish',
      ok(meOf(ana, { confirmed_until: '2026-11-12T10:10:00Z' })),
    )
  const opened = await open('/settings')
  await screen.findByText(/devices? signed in$/)
  return opened
}

function section() {
  return within(screen.getByRole('region', { name: 'Sign-in' }))
}

/** Confirms with the code of the message, in the dialog that asks for it. */
async function confirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
  await user.type(await dialog.findByLabelText('Code from the message'), '123456')
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull())
}

/** Cancels the dialog that asks the person to confirm. */
async function cancel(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  await user.click(await dialog.findByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull())
}

describe('every change that decides who gets in asks for a fresh proof, and then works', () => {
  it('adding a passkey: confirming comes before the browser prompt, and nothing typed is asked again', async () => {
    const { create } = browserWithPasskeys({ create: async () => answered({ id: 'cred-new' }) })
    server.on(
      'POST /api/v1/passkeys/start',
      once(confirmationNeeded, ok({ ceremony: 'cer_1', options: creationOptions })),
    )
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    const name = section().getByLabelText('New passkey name')
    await user.clear(name)
    await user.type(name, 'Laptop')
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await screen.findByRole('dialog', { name: "Confirm it's you" })
    expect(create).not.toHaveBeenCalled()
    await expectAccessible()
    server.on('POST /api/v1/passkeys/finish', ok({ id: 'cred-new', name: 'Laptop' }))
    await confirm(user)
    await section().findByText('Passkey added: Laptop')
    expect(create).toHaveBeenCalledTimes(1)
    expect(server.asked('POST /api/v1/passkeys/start')).toHaveLength(2)
    expect(server.asked('POST /api/v1/passkeys/finish')[0].body).toMatchObject({ name: 'Laptop' })
  })

  it('adding a passkey, cancelled: the form stays as it was, with nothing said', async () => {
    const { create } = browserWithPasskeys()
    server.on('POST /api/v1/passkeys/start', confirmationNeeded)
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await cancel(user)
    const continued = section().getByRole('button', { name: 'Continue' }) as HTMLButtonElement
    await waitFor(() => expect(continued.disabled).toBe(false))
    // Focus is back on what the person clicked: a browser gives it back only to what can take it.
    await waitFor(() => expect(document.activeElement).toBe(continued))
    expect(section().getByLabelText('New passkey name')).toBeTruthy()
    expect(section().queryByRole('alert', { name: '' })?.textContent ?? '').toBe('')
    expect(create).not.toHaveBeenCalled()
  })

  it('removing a passkey', async () => {
    server.on('DELETE /api/v1/passkeys/cred-phone', once(confirmationNeeded, ok()))
    const { user } = await settings({ passkeys: [phone, key] })
    await user.click(section().getByRole('button', { name: named('Remove passkey Phone') }))
    await user.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await confirm(user)
    await section().findByText('Passkey removed: Phone')
  })

  it('turning passkey only on, and the codes are shown once', async () => {
    server.on(
      'POST /api/v1/account/passkey-only',
      once(confirmationNeeded, ok({ passkey_only: true, recovery_codes: codes })),
    )
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    await user.click(screen.getByRole('button', { name: 'Turn on passkey only' }))
    await confirm(user)
    await screen.findByRole('heading', { name: 'Keep your recovery codes' })
    expect(server.asked('POST /api/v1/account/passkey-only')).toHaveLength(2)
    expectNotKept(codes[0], 'att_confirm')
  })

  it('turning passkey only off', async () => {
    server.on(
      'POST /api/v1/account/passkey-only',
      once(confirmationNeeded, ok({ passkey_only: false })),
    )
    const { user } = await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 10 })
    await user.click(section().getByRole('button', { name: named('Turn off passkey only') }))
    await user.click(screen.getByRole('button', { name: 'Turn off passkey only' }))
    await confirm(user)
    await section().findByText('Passkey only is off')
  })

  it('turning passkey only on, cancelled: the question stays, with nothing said', async () => {
    server.on('POST /api/v1/account/passkey-only', confirmationNeeded)
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    await user.click(screen.getByRole('button', { name: 'Turn on passkey only' }))
    await cancel(user)
    const question = screen.getByRole('dialog', { name: 'Sign in with passkeys only?' })
    expect(within(question).getByRole('alert').textContent).toBe('')
    await waitFor(() =>
      expect(document.activeElement).toBe(
        within(question).getByRole('button', { name: 'Turn on passkey only' }),
      ),
    )
    expect(
      (within(question).getByRole('button', { name: 'Turn on passkey only' }) as HTMLButtonElement)
        .disabled,
    ).toBe(false)
  })

  it('making new recovery codes', async () => {
    server.on(
      'POST /api/v1/account/recovery-codes',
      once(confirmationNeeded, ok({ recovery_codes: codes })),
    )
    const { user } = await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 3 })
    await user.click(section().getByRole('button', { name: 'Make new codes' }))
    const question = within(screen.getByRole('dialog', { name: 'Make new recovery codes?' }))
    await user.click(question.getByRole('button', { name: 'Make new codes' }))
    await confirm(user)
    await screen.findByRole('heading', { name: 'Keep your recovery codes' })
  })

  it('linking a provider: confirming comes before the page is left', async () => {
    const assign = watchLeaving()
    server.on(
      'POST /api/v1/providers/start',
      once(
        confirmationNeeded,
        ok({ url: 'https://accounts.google.com/o?state=s', expires_in: 600 }),
      ),
    )
    const { user } = await settings({ on: ['google'] })
    await user.click(section().getByRole('button', { name: named('Link Google') }))
    await screen.findByRole('dialog', { name: "Confirm it's you" })
    expect(assign).not.toHaveBeenCalled()
    await confirm(user)
    await waitFor(() =>
      expect(assign).toHaveBeenCalledExactlyOnceWith('https://accounts.google.com/o?state=s'),
    )
  })

  it('linking a provider, cancelled: nothing is left, and the action is there again', async () => {
    const assign = watchLeaving()
    server.on('POST /api/v1/providers/start', confirmationNeeded)
    const { user } = await settings({ on: ['google'] })
    await user.click(section().getByRole('button', { name: named('Link Google') }))
    await cancel(user)
    const link = section().getByRole('button', { name: named('Link Google') }) as HTMLButtonElement
    await waitFor(() => expect(link.disabled).toBe(false))
    await waitFor(() => expect(document.activeElement).toBe(link))
    expect(section().getByText('Not linked')).toBeTruthy()
    await settle()
    expect(assign).not.toHaveBeenCalled()
  })

  it('unlinking a provider', async () => {
    server.on('DELETE /api/v1/providers/google', once(confirmationNeeded, ok()))
    const { user } = await settings({
      passkeys: [phone],
      on: ['google'],
      linked: [{ provider: 'google', linked_at: '2026-03-06T10:00:00Z' }],
    })
    await user.click(section().getByRole('button', { name: named('Unlink Google') }))
    await user.click(screen.getByRole('button', { name: 'Unlink Google' }))
    await confirm(user)
    await section().findByText('Google unlinked')
  })

  it('unlinking a provider, cancelled: the question stays', async () => {
    server.on('DELETE /api/v1/providers/google', confirmationNeeded)
    const { user } = await settings({
      passkeys: [phone],
      on: ['google'],
      linked: [{ provider: 'google', linked_at: '2026-03-06T10:00:00Z' }],
    })
    await user.click(section().getByRole('button', { name: named('Unlink Google') }))
    await user.click(screen.getByRole('button', { name: 'Unlink Google' }))
    await cancel(user)
    const question = screen.getByRole('dialog', { name: 'Unlink Google?' })
    expect(within(question).getByRole('alert').textContent).toBe('')
    await waitFor(() =>
      expect(document.activeElement).toBe(
        within(question).getByRole('button', { name: 'Unlink Google' }),
      ),
    )
  })

  it('changing the contact address, and sending another message, without typing the address again', async () => {
    server.on(
      'POST /api/v1/account/address/start',
      once(confirmationNeeded, ok({ attempt: 'att_change', expires_in: 600 })),
    )
    const { user } = await settings()
    await user.click(screen.getByRole('button', { name: named('Change contact email') }))
    const asking = within(screen.getByRole('dialog', { name: 'Change your contact email' }))
    await user.type(asking.getByLabelText('New email address'), 'ana@new.example')
    await user.click(asking.getByRole('button', { name: 'Send the code' }))
    await confirm(user)
    const code = within(await screen.findByRole('dialog', { name: 'Check your mail' }))
    expect(server.asked('POST /api/v1/account/address/start').map((one) => one.body)).toEqual([
      { address: 'ana@new.example' },
      { address: 'ana@new.example' },
    ])

    // The proof ran out meanwhile: sending another message asks again. Cancelled, it says nothing.
    server.on('POST /api/v1/account/address/start', confirmationNeeded)
    await user.click(code.getByRole('button', { name: 'Send another message' }))
    await cancel(user)
    await settle()
    expect(code.queryByText(/Asked for another message/)).toBeNull()
    expect(
      code
        .getAllByRole('alert')
        .map((one) => one.textContent)
        .join(''),
    ).toBe('')

    server.on(
      'POST /api/v1/account/address/start',
      once(confirmationNeeded, ok({ attempt: 'att_change_2', expires_in: 600 })),
    )
    await user.click(code.getByRole('button', { name: 'Send another message' }))
    await confirm(user)
    await code.findByText('Asked for another message. Use the code or the link of the newest one.')
    expectNotKept('ana@new.example', 'att_change_2')
  })

  it('changing the contact address, cancelled: the address typed is still there', async () => {
    server.on('POST /api/v1/account/address/start', confirmationNeeded)
    const { user } = await settings()
    await user.click(screen.getByRole('button', { name: named('Change contact email') }))
    const asking = within(screen.getByRole('dialog', { name: 'Change your contact email' }))
    await user.type(asking.getByLabelText('New email address'), 'ana@new.example')
    await user.click(asking.getByRole('button', { name: 'Send the code' }))
    await cancel(user)
    expect((asking.getByLabelText('New email address') as HTMLInputElement).value).toBe(
      'ana@new.example',
    )
    await waitFor(() =>
      expect(document.activeElement).toBe(asking.getByRole('button', { name: 'Send the code' })),
    )
    expect(
      asking
        .getAllByRole('alert')
        .map((one) => one.textContent)
        .join(''),
    ).toBe('')
  })

  it('deleting the account', async () => {
    server.on(
      'POST /api/v1/account/delete',
      once(confirmationNeeded, ok({ ...ana, state: 'closing', closes_at: '2026-11-19T10:00:00Z' })),
    )
    const { user } = await settings()
    await user.click(screen.getByRole('button', { name: 'Delete my account' }))
    const question = within(screen.getByRole('dialog', { name: 'Delete your account?' }))
    await user.type(question.getByLabelText('Address of this account'), 'ana@example.org')
    await user.click(question.getByRole('button', { name: 'Delete my account' }))
    await confirm(user)
    await screen.findByText(/It will be deleted for good on/)
  })

  it('deleting the account, cancelled: nothing is deleted and nothing is said', async () => {
    server.on('POST /api/v1/account/delete', confirmationNeeded)
    const { user } = await settings()
    await user.click(screen.getByRole('button', { name: 'Delete my account' }))
    const question = within(screen.getByRole('dialog', { name: 'Delete your account?' }))
    await user.type(question.getByLabelText('Address of this account'), 'ana@example.org')
    await user.click(question.getByRole('button', { name: 'Delete my account' }))
    await cancel(user)
    expect(
      question.getByRole('button', { name: 'Delete my account' }).getAttribute('aria-disabled'),
    ).toBe('false')
    await waitFor(() =>
      expect(document.activeElement).toBe(
        question.getByRole('button', { name: 'Delete my account' }),
      ),
    )
    expect(
      question
        .getAllByRole('alert')
        .map((one) => one.textContent)
        .join(''),
    ).toBe('')
  })
})
