import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectNotKept, named, open, settle } from '../helpers/app.tsx'
import { expectAccessible } from '../helpers/axe.ts'
import { answered, browserError, browserWithPasskeys, requestOptions } from '../helpers/passkeys.ts'
import {
  ana,
  confirmationNeeded,
  later,
  meOf,
  network,
  ok,
  once,
  refused,
  server,
} from '../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const phone = { id: 'cred-phone', name: 'Phone' }
const key = { id: 'cred-key', name: 'Security key' }
const until = '2026-11-12T10:10:00Z'
const confirmedMe = meOf(ana, { confirmed_until: until })

/**
 * Opens Settings as Ana with two passkeys, and asks to remove one: the server wants a fresh proof first, and
 * removes it once it has one.
 */
async function removeAsking(ways: string[] = ['passkey', 'email'], also?: () => void) {
  server
    .signedIn()
    .settings({ passkeys: [phone, key] })
    .on('DELETE /api/v1/passkeys/cred-phone', once(confirmationNeeded, ok()))
    .on('GET /api/v1/auth/confirm', ok({ ways }))
    .on(
      'POST /api/v1/auth/confirm/passkey/start',
      ok({ ceremony: 'cer_confirm', options: requestOptions }),
    )
  also?.()
  const opened = await open('/settings')
  await screen.findByText('1 device signed in')
  const { user } = opened
  await user.click(screen.getByRole('button', { name: named('Remove passkey Phone') }))
  await user.click(screen.getByRole('button', { name: 'Remove passkey' }))
  const dialog = await screen.findByRole('dialog', { name: "Confirm it's you" })
  return { user, dialog: within(dialog), element: dialog }
}

/** The removal went through: the passkey's dialog closed, and the server was asked twice. */
async function removed() {
  await waitFor(() => expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(2))
  await waitFor(() =>
    expect(screen.queryByRole('dialog', { name: 'Remove this passkey?' })).toBeNull(),
  )
}

async function typeCode(user: User, dialog: ReturnType<typeof within>, code = '123456') {
  const field = await dialog.findByLabelText('Code from the message')
  await user.clear(field)
  await user.type(field, code)
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
}

describe('confirming it is you', () => {
  it('opens when a change answers confirmation_needed, and says why, with the ways the account has', async () => {
    browserWithPasskeys()
    const { dialog, element } = await removeAsking()
    await dialog.findByRole('button', { name: 'Use a passkey' })
    expect(
      dialog.getByText(
        "This change decides who can get into your account, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
      ),
    ).toBeTruthy()
    expect(dialog.getByRole('button', { name: 'Get a code by email instead' })).toBeTruthy()
    expect(element.textContent).toContain('It goes to ana@example.org.')
    // Focus is on the question, not on a way: Enter answers nothing unread.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        dialog.getByRole('heading', { name: "Confirm it's you" }),
      ),
    )
    await expectAccessible()
  })

  it('opens the passkey prompt straight from the click, with options asked for before it', async () => {
    const { get } = browserWithPasskeys({
      get: async () => answered({ id: 'cred-key', response: { signature: 'sig_secret' } }),
    })
    const { user, dialog } = await removeAsking()
    await waitFor(() =>
      expect(server.asked('POST /api/v1/auth/confirm/passkey/start')).toHaveLength(1),
    )
    server.on('POST /api/v1/auth/confirm/passkey/finish', ok(confirmedMe))
    await settle()
    await user.click(dialog.getByRole('button', { name: 'Use a passkey' }))
    await removed()
    // No request came between the click and the prompt.
    expect(server.asked('POST /api/v1/auth/confirm/passkey/start')).toHaveLength(1)
    expect(get).toHaveBeenCalledTimes(1)
    expect(server.asked('POST /api/v1/auth/confirm/passkey/finish')[0].body).toEqual({
      ceremony: 'cer_confirm',
      credential: { id: 'cred-key', response: { signature: 'sig_secret' } },
    })
    expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull()
    await screen.findByText('Passkey removed: Phone')
    expectNotKept('cer_confirm', 'sig_secret')
  })

  it('asks for options at the click when the ones asked for before are stale or failed', async () => {
    browserWithPasskeys({ get: async () => answered({ id: 'cred-key' }) })
    const now = Date.now()
    const { user, dialog } = await removeAsking(undefined, () =>
      server.on('POST /api/v1/auth/confirm/passkey/start', network),
    )
    await dialog.findByRole('button', { name: 'Use a passkey' })
    server
      .on(
        'POST /api/v1/auth/confirm/passkey/start',
        ok({ ceremony: 'cer_late', options: requestOptions }),
      )
      .on('POST /api/v1/auth/confirm/passkey/finish', refused(400, 'invalid_passkey'))
    await user.click(dialog.getByRole('button', { name: 'Use a passkey' }))
    await dialog.findByText(
      "That passkey didn't confirm it's you. Use one of this account's passkeys, then try again.",
    )
    // Options were asked for again after the try. Three minutes later they are too old to use.
    await waitFor(() =>
      expect(server.asked('POST /api/v1/auth/confirm/passkey/start')).toHaveLength(3),
    )
    await settle()
    vi.spyOn(Date, 'now').mockReturnValue(now + 180_000)
    server.on('POST /api/v1/auth/confirm/passkey/finish', ok(confirmedMe))
    await user.click(dialog.getByRole('button', { name: 'Use a passkey' }))
    await removed()
    expect(server.asked('POST /api/v1/auth/confirm/passkey/start')).toHaveLength(4)
  })

  it('says calmly that no passkey was used, and what a browser that cannot use one should do', async () => {
    const answers = [
      browserError('NotAllowedError'),
      browserError('SecurityError'),
      browserError('UnknownError'),
    ]
    browserWithPasskeys({
      get: async () => {
        throw answers.shift()
      },
    })
    const { user, dialog } = await removeAsking()
    const use = await dialog.findByRole('button', { name: 'Use a passkey' })
    await user.click(use)
    const quiet = await dialog.findByText('No passkey was used. You can try again.')
    expect(quiet.getAttribute('role')).toBe('status')
    expect(document.activeElement).toBe(use)
    await user.click(use)
    const loud = await dialog.findByText(
      "This browser can't use a passkey here. Get a code by email instead.",
    )
    expect(loud.getAttribute('role')).toBe('alert')
    await user.click(use)
    await dialog.findByText("The passkey couldn't be read. Try again.")
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(1)
    await expectAccessible()
  })

  it('drops the passkey when the account has none any more', async () => {
    browserWithPasskeys()
    const { user, dialog } = await removeAsking(undefined, () =>
      server.on('POST /api/v1/auth/confirm/passkey/start', refused(409, 'passkey_needed')),
    )
    await user.click(await dialog.findByRole('button', { name: 'Use a passkey' }))
    const said = await dialog.findByText('This account has no passkey any more.')
    await waitFor(() => expect(document.activeElement).toBe(said))
    expect(dialog.queryByRole('button', { name: 'Use a passkey' })).toBeNull()
    expect(dialog.getByRole('button', { name: 'Email me a code' })).toBeTruthy()
  })

  it('confirms with a code by mail, and reads a wrong code, too many tries and a code that ran out', async () => {
    const { user, dialog } = await removeAsking(['email'])
    // A browser without passkeys, and an account that confirms by mail: the code is the one way.
    server.on(
      'POST /api/v1/auth/confirm/email/start',
      ok({ attempt: 'att_confirm', expires_in: 0 }),
    )
    await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
    await dialog.findByText(/A message with a code is on its way to/)
    expect(dialog.getByText(/It has expired by now\./)).toBeTruthy()
    await waitFor(() =>
      expect(document.activeElement).toBe(dialog.getByLabelText('Code from the message')),
    )
    await expectAccessible()

    server.on('POST /api/v1/auth/confirm/email/finish', refused(400, 'invalid_code'))
    await typeCode(user, dialog, '111111')
    await dialog.findByText(
      "That code isn't right, or it has expired. Check it, or send another message.",
    )
    server.on('POST /api/v1/auth/confirm/email/finish', refused(429, 'too_many_requests'))
    await typeCode(user, dialog, '222222')
    await dialog.findByText('Too many attempts. Wait a few minutes, then try again.')

    server.on(
      'POST /api/v1/auth/confirm/email/start',
      ok({ attempt: 'att_again', expires_in: 600 }),
    )
    await user.click(dialog.getByRole('button', { name: 'Send another message' }))
    await dialog.findByText(/It works for 10 minutes\./)
    server.on('POST /api/v1/auth/confirm/email/finish', ok(confirmedMe))
    await typeCode(user, dialog, '333333')
    await removed()
    expect(server.asked('POST /api/v1/auth/confirm/email/finish').at(-1)?.body).toEqual({
      attempt: 'att_again',
      code: '333333',
    })
    expectNotKept('att_confirm', 'att_again', '333333')
  })

  it('goes back from the code to the ways, and says when no message was sent', async () => {
    browserWithPasskeys()
    const { user, dialog } = await removeAsking()
    server.on('POST /api/v1/auth/confirm/email/start', refused(429, 'too_many_requests'))
    const instead = await dialog.findByRole('button', { name: 'Get a code by email instead' })
    await user.click(instead)
    await dialog.findByText(
      'No message was sent. Too many attempts. Wait a few minutes, then try again.',
    )
    expect(document.activeElement).toBe(instead)
    server.on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
    await user.click(instead)
    await dialog.findByLabelText('Code from the message')
    await user.click(dialog.getByRole('button', { name: 'Back' }))
    await dialog.findByRole('button', { name: 'Use a passkey' })
    await waitFor(() =>
      expect(document.activeElement).toBe(
        dialog.getByRole('heading', { name: "Confirm it's you" }),
      ),
    )
  })

  it('says when the account confirms with a passkey only, before or after the message', async () => {
    browserWithPasskeys()
    const { user, dialog } = await removeAsking()
    server.on('POST /api/v1/auth/confirm/email/start', refused(409, 'passkey_required'))
    await user.click(await dialog.findByRole('button', { name: 'Get a code by email instead' }))
    const said = await dialog.findByText('This account confirms with a passkey, not with a code.')
    await waitFor(() => expect(document.activeElement).toBe(said))
    expect(dialog.queryByRole('button', { name: 'Get a code by email instead' })).toBeNull()
  })

  it('goes back to the ways when passkey only was turned on after the message was sent', async () => {
    browserWithPasskeys()
    const { user, dialog } = await removeAsking()
    server
      .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on('POST /api/v1/auth/confirm/email/finish', refused(409, 'passkey_required'))
    await user.click(await dialog.findByRole('button', { name: 'Get a code by email instead' }))
    await typeCode(user, dialog)
    await dialog.findByText('This account confirms with a passkey, not with a code.')
    expect(dialog.getByRole('button', { name: 'Use a passkey' })).toBeTruthy()
  })

  it('offers nothing it cannot do: a passkey-only account in a browser without passkeys', async () => {
    const { dialog } = await removeAsking(['passkey'])
    await dialog.findByText(
      "There's no way to confirm it's you in this browser. Try again in a browser that can use your passkeys.",
    )
    expect(dialog.queryByRole('button', { name: 'Use a passkey' })).toBeNull()
    await expectAccessible()
  })

  it('leaves everything as it was when cancelled, with the button or with Escape', async () => {
    const { user, dialog } = await removeAsking(['email'])
    await user.click(await dialog.findByRole('button', { name: 'Cancel' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull(),
    )
    // The question of the passkey is still open, as it was, with nothing said in it.
    const question = screen.getByRole('dialog', { name: 'Remove this passkey?' })
    expect(within(question).getByRole('alert').textContent).toBe('')
    const remove = within(question).getByRole('button', { name: 'Remove passkey' })
    await waitFor(() => expect(document.activeElement).toBe(remove))
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(1)

    server.on('DELETE /api/v1/passkeys/cred-phone', confirmationNeeded)
    await user.click(remove)
    const asking = await screen.findByRole('dialog', { name: "Confirm it's you" })
    // Escape, as the browser tells a dialog of it.
    asking.dispatchEvent(new Event('cancel', { cancelable: true }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull(),
    )
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(2)
    expect(within(question).getByRole('alert').textContent).toBe('')
  })

  it('says when the ways cannot be read, and tries again', async () => {
    const { user, dialog } = await removeAsking(undefined, () =>
      server.on('GET /api/v1/auth/confirm', network),
    )
    const alert = await dialog.findByRole('alert')
    expect(alert.textContent).toContain(
      "This couldn't be checked. Permaily can't be reached. Check your connection, then try again.",
    )
    await expectAccessible()
    server.on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await dialog.findByRole('button', { name: 'Email me a code' })
  })

  it('asks nothing when the proof was made meanwhile, such as in another tab', async () => {
    server.on('DELETE /api/v1/passkeys/cred-phone', once(confirmationNeeded, ok()))
    server.signedIn().settings({ passkeys: [phone, key] })
    server.on(
      'GET /api/v1/auth/confirm',
      ok({ ways: ['email'], confirmed_until: new Date(Date.now() + 60_000).toISOString() }),
    )
    const { user } = await open('/settings')
    await user.click(await screen.findByRole('button', { name: named('Remove passkey Phone') }))
    await user.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await removed()
  })

  it('runs the change again once only: a second refusal is said, not asked again', async () => {
    const { user, dialog } = await removeAsking(['email'])
    server
      .on('DELETE /api/v1/passkeys/cred-phone', confirmationNeeded)
      .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on('POST /api/v1/auth/confirm/email/finish', ok(confirmedMe))
    await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
    await typeCode(user, dialog)
    const question = await screen.findByRole('dialog', { name: 'Remove this passkey?' })
    await within(question).findByText(
      "Nothing changed: Permaily needs you to confirm it's you first. Try again.",
    )
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(2)
  })

  it('leads to the sign-in screen when the session ended meanwhile', async () => {
    const { user, dialog } = await removeAsking(['email'])
    server.on('POST /api/v1/auth/confirm/email/start', () => {
      server.signedOut()
      return refused(401, 'unauthorized')
    })
    await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('cannot be closed while a passkey is being checked', async () => {
    browserWithPasskeys({ get: async () => answered({ id: 'cred-key' }) })
    const checking = later()
    const { user, dialog } = await removeAsking()
    server.on('POST /api/v1/auth/confirm/passkey/finish', checking.answer)
    await settle()
    await user.click(await dialog.findByRole('button', { name: 'Use a passkey' }))
    await dialog.findByText('Waiting for your browser')
    screen
      .getByRole('dialog', { name: "Confirm it's you" })
      .dispatchEvent(new Event('cancel', { cancelable: true }))
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('dialog', { name: "Confirm it's you" })).toBeTruthy()
    checking.give(ok(confirmedMe))
    await removed()
  })
})
