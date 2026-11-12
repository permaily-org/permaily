import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, expectNotKept, named, open, settle, watchLeaving } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  answered,
  b64url,
  browserError,
  browserWithPasskeys,
  creationOptions,
} from '../../helpers/passkeys.ts'
import { later, meOf, network, ok, refused, server } from '../../helpers/server.ts'

type Has = NonNullable<Parameters<typeof server.settings>[0]>

const phone = { id: 'cred-phone', name: 'Phone', synced: true }
const key = { id: 'cred-key', name: 'Security key' }
const here = meOf().session
const laptop = {
  ...here,
  id: 'ses_laptop',
  current: false,
  last_used_at: '2026-11-10T08:00:00Z',
  device:
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15',
}
const codes = [
  'AAAA-2222-BBBB-3333',
  'CCCC-4444-DDDD-5555',
  'EEEE-6666-FFFF-7777',
  'GGGG-8888-HHHH-9999',
  'JJJJ-2222-KKKK-3333',
  'MMMM-4444-NNNN-5555',
  'PPPP-6666-QQQQ-7777',
  'RRRR-8888-SSSS-9999',
  'TTTT-2222-UUUU-3333',
  'VVVV-4444-WWWW-5555',
]

/** Opens Settings as Ana with this much of a sign-in section, and waits for it. */
async function settings(has: Has = {}, address = '/settings') {
  server.signedIn().settings(has)
  const opened = await open(address)
  await screen.findByText(/devices? signed in$/)
  return opened
}

function section() {
  return within(screen.getByRole('region', { name: 'Sign-in' }))
}

describe('the sign-in section', () => {
  it('reads how the account is signed in to when it opens', async () => {
    const reading = later()
    server.signedIn().settings().on('GET /api/v1/passkeys', reading.answer)
    await open('/settings')
    await screen.findByRole('region', { name: 'Sign-in' })
    expect(section().getByText('Loading').getAttribute('role')).toBe('status')
    reading.give(ok({ passkeys: [], passkey_only: false, recovery_codes_left: 0 }))
    await section().findByText(
      "None. You sign in with a message sent to your email address. This browser can't make passkeys.",
    )
    await settle()
    expect(section().queryByRole('button', { name: 'Add a passkey' })).toBeNull()
    expect(section().getByText('Add a passkey to turn this on.')).toBeTruthy()
    await expectAccessible()
  })

  it('says when it cannot be read, and offers to try again', async () => {
    server.signedIn().settings().on('GET /api/v1/sessions', network)
    const { user } = await open('/settings')
    await screen.findByRole('region', { name: 'Sign-in' })
    const alert = await section().findByRole('alert')
    expect(alert.textContent).toContain(
      "Your sign-in settings couldn't load. Permaily can't be reached. Check your connection, then try again.",
    )
    await expectAccessible()
    server.settings()
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await section().findByText('1 device signed in')
    await settle()
    expect(section().queryByRole('alert')).toBeNull()
  })

  it('leads to the sign-in screen when the session ended', async () => {
    server
      .signedIn()
      .settings()
      .on('GET /api/v1/passkeys', () => {
        server.signedOut()
        return refused(401, 'unauthorized')
      })
    await open('/settings')
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toEqual({ from: '/settings' })
  })
})

describe('passkeys', () => {
  it('lists each with its name, the day it was made and whether it is synced', async () => {
    browserWithPasskeys()
    await settings({ passkeys: [phone, key] })
    section().getByText('Phone')
    section().getByText('Added 6 Mar 2026 · Synced')
    section().getByText('Security key')
    section().getByText('Added 6 Mar 2026 · Not synced')
    expect(section().getByRole('button', { name: 'Add a passkey' })).toBeTruthy()
    await expectAccessible()
  })

  it('tells the authenticator which passkeys the account has, once, where the browser can', async () => {
    const signalAllAcceptedCredentials = vi.fn(async () => {})
    browserWithPasskeys({ statics: { signalAllAcceptedCredentials } })
    const { user } = await settings({ passkeys: [phone, key], sessions: [here, laptop] })
    await waitFor(() =>
      expect(signalAllAcceptedCredentials).toHaveBeenCalledExactlyOnceWith({
        rpId: window.location.hostname,
        userId: b64url('acc_ana'),
        allAcceptedCredentialIds: ['cred-phone', 'cred-key'],
      }),
    )
    // Not again after a later reading: a passkey just made may still be on its way to the server.
    server.on('DELETE /api/v1/sessions/ses_laptop', ok())
    await user.click(section().getByRole('button', { name: named('Review sessions') }))
    server.settings({ passkeys: [phone, key] })
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    await section().findByText('1 device signed in')
    expect(signalAllAcceptedCredentials).toHaveBeenCalledTimes(1)
  })

  it('adds one: a name first, with one suggested, then the browser prompt', async () => {
    const { create } = browserWithPasskeys({
      create: async () =>
        answered({ id: 'cred-new', response: { attestationObject: 'att_secret' } }),
    })
    const { user } = await settings({ passkeys: [{ id: 'cred-old', name: 'Unknown device' }] })
    const add = section().getByRole('button', { name: 'Add a passkey' })
    await user.click(add)
    const name = section().getByLabelText('New passkey name') as HTMLInputElement
    // The browser and system, with a number when that name is taken.
    expect(name.value).toBe('Unknown device 2')
    await waitFor(() => expect(document.activeElement).toBe(name))
    expect(add.getAttribute('aria-expanded')).toBe('true')
    expect(create).not.toHaveBeenCalled()
    await expectAccessible()

    server
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_secret_1', options: creationOptions }))
      .on('POST /api/v1/passkeys/finish', () => {
        server.settings({
          passkeys: [
            { id: 'cred-old', name: 'Unknown device' },
            { id: 'cred-new', name: 'Laptop' },
          ],
        })
        return ok({ id: 'cred-new', name: 'Laptop' })
      })
    await user.clear(name)
    await user.type(name, 'Laptop')
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await section().findByText('Laptop')
    expect(create).toHaveBeenCalledTimes(1)
    expect(server.asked('POST /api/v1/passkeys/finish')[0].body).toEqual({
      ceremony: 'cer_secret_1',
      name: 'Laptop',
      credential: { id: 'cred-new', response: { attestationObject: 'att_secret' } },
    })
    section().getByText('Passkey added: Laptop')
    // The form is gone, and focus is back on the action that opened it.
    await waitFor(() => expect(section().queryByLabelText('New passkey name')).toBeNull())
    await waitFor(() =>
      expect(document.activeElement).toBe(section().getByRole('button', { name: 'Add a passkey' })),
    )
    expectNotKept('cer_secret_1', 'att_secret')
  })

  it('sends the same answer again when only the name was refused: no second passkey is made', async () => {
    const signalUnknownCredential = vi.fn(async () => {})
    const { create } = browserWithPasskeys({
      create: async () => answered({ id: 'cred-new' }),
      statics: { signalUnknownCredential },
    })
    const { user } = await settings()
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    server
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
      .on('POST /api/v1/passkeys/finish', refused(400, 'invalid_name'))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    const said = await section().findByText('Give it a name of 1 to 64 characters, on one line.')
    expect(said.getAttribute('role')).toBe('alert')
    server.on('POST /api/v1/passkeys/finish', network)
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await section().findByText("Permaily can't be reached. Check your connection, then try again.")
    server.on('POST /api/v1/passkeys/finish', ok({ id: 'cred-new', name: 'Unknown device' }))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await section().findByText('Passkey added: Unknown device')
    expect(create).toHaveBeenCalledTimes(1)
    expect(server.asked('POST /api/v1/passkeys/start')).toHaveLength(1)
    expect(server.asked('POST /api/v1/passkeys/finish')).toHaveLength(3)
    // The server may yet take it: the authenticator is told nothing.
    expect(signalUnknownCredential).not.toHaveBeenCalled()
  })

  it('says before the prompt what cannot be a name', async () => {
    const { create } = browserWithPasskeys()
    const { user } = await settings()
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    await user.clear(section().getByLabelText('New passkey name'))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    section().getByText('Give it a name of 1 to 64 characters, on one line.')
    expect(create).not.toHaveBeenCalled()
    expect(server.asked('POST /api/v1/passkeys/start')).toHaveLength(0)
    await user.click(section().getByRole('button', { name: 'Cancel' }))
    await settle()
    expect(section().queryByLabelText('New passkey name')).toBeNull()
  })

  it.each([
    ['NotAllowedError', 'No passkey was made. You can try again.', 'status'],
    [
      'InvalidStateError',
      'This device or password manager already has a passkey for this account, so it made no other.',
      'alert',
    ],
    ['SecurityError', "This browser can't make a passkey here.", 'alert'],
    ['UnknownError', "The passkey couldn't be made. Try again.", 'alert'],
  ])('says what the browser did when it gave no passkey: %s', async (name, words, role) => {
    browserWithPasskeys({
      create: async () => {
        throw browserError(name)
      },
    })
    const { user } = await settings()
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    server.on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    expect((await section().findByText(words)).getAttribute('role')).toBe(role)
    expect(server.asked('POST /api/v1/passkeys/finish')).toHaveLength(0)
    await waitFor(() =>
      expect(document.activeElement).toBe(section().getByLabelText('New passkey name')),
    )
  })

  it.each([
    [
      'invalid_passkey',
      400,
      "That passkey couldn't be added: the wait was too long, or the browser's answer didn't check. Try again.",
    ],
    [
      'too_many_passkeys',
      409,
      'This account has as many passkeys as it can have. Remove one first.',
    ],
  ])(
    'tells the authenticator of a passkey the server refused when it was made: %s',
    async (code, status, words) => {
      const signalUnknownCredential = vi.fn(async () => {})
      const { create } = browserWithPasskeys({
        create: async () => answered({ id: 'cred-refused' }),
        statics: { signalUnknownCredential },
      })
      const { user } = await settings()
      await user.click(section().getByRole('button', { name: 'Add a passkey' }))
      server
        .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
        .on('POST /api/v1/passkeys/finish', refused(status, code))
      await user.click(section().getByRole('button', { name: 'Continue' }))
      await section().findByText(words)
      // The authenticator made it and the server does not have it: it would sign nobody in.
      expect(signalUnknownCredential).toHaveBeenCalledExactlyOnceWith({
        rpId: 'app.permaily.test',
        credentialId: 'cred-refused',
      })
      // And the next try makes another: that answer is of no use any more.
      await user.click(section().getByRole('button', { name: 'Continue' }))
      await waitFor(() => expect(create).toHaveBeenCalledTimes(2))
    },
  )

  it('tells the authenticator nothing when the server own failure leaves it unknown', async () => {
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({
      create: async () => answered({ id: 'cred-new' }),
      statics: { signalUnknownCredential },
    })
    const { user } = await settings()
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    server
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
      .on('POST /api/v1/passkeys/finish', refused(500, 'internal'))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await section().findByText('Something went wrong on our side. Try again in a moment.')
    expect(signalUnknownCredential).not.toHaveBeenCalled()
  })

  it('adds one all the same in a browser without the signals', async () => {
    browserWithPasskeys({ create: async () => answered({ id: 'cred-refused' }) })
    const { user } = await settings()
    await user.click(section().getByRole('button', { name: 'Add a passkey' }))
    server
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
      .on('POST /api/v1/passkeys/finish', refused(400, 'invalid_passkey'))
    await user.click(section().getByRole('button', { name: 'Continue' }))
    await section().findByText(/That passkey couldn't be added/)
  })

  it('renames one in place', async () => {
    const { user } = await settings({ passkeys: [phone] })
    const rename = section().getByRole('button', { name: named('Rename passkey Phone') })
    await user.click(rename)
    const field = section().getByLabelText('Passkey name') as HTMLInputElement
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(field.value).toBe('Phone')
    await expectAccessible()

    await user.clear(field)
    await user.click(section().getByRole('button', { name: 'Save' }))
    section().getByText('Give it a name of 1 to 64 characters, on one line.')

    server.on('PATCH /api/v1/passkeys/cred-phone', () => {
      server.settings({ passkeys: [{ ...phone, name: 'Work phone' }] })
      return ok({ ...phone, name: 'Work phone' })
    })
    await user.type(field, 'Work phone')
    await user.click(section().getByRole('button', { name: 'Save' }))
    await section().findByText('Work phone')
    expect(server.asked('PATCH /api/v1/passkeys/cred-phone')[0].body).toEqual({
      name: 'Work phone',
    })
    section().getByText('Passkey renamed to Work phone')
    await waitFor(() =>
      expect(document.activeElement).toBe(
        section().getByRole('button', { name: named('Rename passkey Work phone') }),
      ),
    )
  })

  it('asks nothing for the same name, and says what the server refused', async () => {
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Rename passkey Phone') }))
    await user.click(section().getByRole('button', { name: 'Save' }))
    expect(server.asked('PATCH /api/v1/passkeys/cred-phone')).toHaveLength(0)

    await user.click(section().getByRole('button', { name: named('Rename passkey Phone') }))
    server.on('PATCH /api/v1/passkeys/cred-phone', refused(400, 'invalid_name'))
    await user.type(section().getByLabelText('Passkey name'), ' 2')
    await user.click(section().getByRole('button', { name: 'Save' }))
    await section().findByText('Give it a name of 1 to 64 characters, on one line.')
    server.on('PATCH /api/v1/passkeys/cred-phone', network)
    await user.click(section().getByRole('button', { name: 'Save' }))
    await section().findByText("Permaily can't be reached. Check your connection, then try again.")
    await user.click(section().getByRole('button', { name: 'Cancel' }))
    section().getByText('Phone')
  })

  it('says so when the passkey being renamed was removed from another device', async () => {
    const { user } = await settings({ passkeys: [phone, key] })
    await user.click(section().getByRole('button', { name: named('Rename passkey Phone') }))
    server.on('PATCH /api/v1/passkeys/cred-phone', () => {
      server.settings({ passkeys: [key] })
      return refused(404, 'not_found')
    })
    await user.type(section().getByLabelText('Passkey name'), ' 2')
    await user.click(section().getByRole('button', { name: 'Save' }))
    await section().findByText('That passkey was removed already')
    await waitFor(() => expect(section().queryByText('Phone')).toBeNull())
    // Its row is gone: focus moves to the section.
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Sign-in' })),
    )
  })

  it('removes one after asking, and tells the authenticator where the browser can', async () => {
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({ statics: { signalUnknownCredential } })
    const { user } = await settings({ passkeys: [phone, key] })
    await user.click(section().getByRole('button', { name: named('Remove passkey Phone') }))
    const dialog = within(screen.getByRole('dialog', { name: 'Remove this passkey?' }))
    await waitFor(() => expect(document.activeElement).toBe(dialog.getByRole('heading')))
    dialog.getByText('Phone')
    expect(dialog.getByText(/It stops signing you in to Permaily now\./).textContent).toBe(
      'It stops signing you in to Permaily now. It may stay listed on your device or in your password manager until you delete it there.',
    )
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(0)
    await expectAccessible()

    server.on('DELETE /api/v1/passkeys/cred-phone', () => {
      server.settings({ passkeys: [key] })
      return ok()
    })
    await user.click(dialog.getByRole('button', { name: 'Remove passkey' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(section().queryByText('Phone')).toBeNull())
    section().getByText('Passkey removed: Phone')
    expect(signalUnknownCredential).toHaveBeenCalledExactlyOnceWith({
      rpId: window.location.hostname,
      credentialId: 'cred-phone',
    })
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Sign-in' })),
    )
  })

  it('removes one all the same in a browser without the signals, or already removed elsewhere', async () => {
    const { user } = await settings({ passkeys: [phone, key] })
    await user.click(section().getByRole('button', { name: named('Remove passkey Phone') }))
    server.on('DELETE /api/v1/passkeys/cred-phone', () => {
      server.settings({ passkeys: [key] })
      return refused(404, 'not_found')
    })
    await user.click(screen.getByRole('button', { name: 'Remove passkey' }))
    await waitFor(() => expect(section().queryByText('Phone')).toBeNull())
  })

  it('says that mail signs in afterwards when it is the last one, and keeps it when asked', async () => {
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Remove passkey Phone') }))
    const dialog = within(screen.getByRole('dialog'))
    dialog.getByText(/After this, you sign in with a message sent to your email address\./)
    await user.click(dialog.getByRole('button', { name: 'Keep it' }))
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(server.asked('DELETE /api/v1/passkeys/cred-phone')).toHaveLength(0)
    // Focus is back on the action that asked.
    await waitFor(() =>
      expect(document.activeElement).toBe(
        section().getByRole('button', { name: named('Remove passkey Phone') }),
      ),
    )
  })

  it('says why a removal was refused, and tells the authenticator nothing', async () => {
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({ statics: { signalUnknownCredential } })
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Remove passkey Phone') }))
    // "Passkey only" was turned on meanwhile, from another device.
    server.on('DELETE /api/v1/passkeys/cred-phone', () => {
      server.settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 10 })
      return refused(409, 'last_passkey')
    })
    const dialog = within(screen.getByRole('dialog'))
    await user.click(dialog.getByRole('button', { name: 'Remove passkey' }))
    await dialog.findByText(
      'That is the only passkey of an account that signs in with passkeys only. Add another, or turn passkey only off first.',
    )
    expect(signalUnknownCredential).not.toHaveBeenCalled()
    server.on('DELETE /api/v1/passkeys/cred-phone', network)
    await user.click(dialog.getByRole('button', { name: 'Remove passkey' }))
    await dialog.findByText("Permaily can't be reached. Check your connection, then try again.")
  })

  it('has no Remove for the last passkey of an account that signs in with passkeys only', async () => {
    await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 10 })
    await settle()
    expect(section().queryByRole('button', { name: named('Remove passkey Phone') })).toBeNull()
    section().getByText("Can't be removed: it is your only passkey, and passkey only is on.")
    await expectAccessible()
  })
})

describe('"passkey only" and recovery codes', () => {
  it('asks first, then shows the codes once, in a dialog that must be answered', async () => {
    const { user } = await settings({ passkeys: [phone] })
    section().getByText('Off')
    await settle()
    expect(section().queryByText('Recovery codes')).toBeNull()
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    const question = within(screen.getByRole('dialog', { name: 'Sign in with passkeys only?' }))
    await waitFor(() => expect(document.activeElement).toBe(question.getByRole('heading')))
    expect(question.getByText(/will no longer sign you in/).textContent).toBe(
      'A message sent to ana@example.org will no longer sign you in. Only your passkeys will.',
    )
    question.getByText(/Permaily can't let you in either\./)
    expect(server.asked('POST /api/v1/account/passkey-only')).toHaveLength(0)
    await expectAccessible()

    server.on('POST /api/v1/account/passkey-only', () => {
      server.settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 10 })
      return ok({ passkey_only: true, recovery_codes: codes })
    })
    await user.click(question.getByRole('button', { name: 'Turn on passkey only' }))
    const shown = within(await screen.findByRole('dialog', { name: 'Keep your recovery codes' }))
    await waitFor(() => expect(document.activeElement).toBe(shown.getByRole('heading')))
    expect(server.asked('POST /api/v1/account/passkey-only')[0].body).toEqual({ on: true })
    expect(shown.getAllByRole('listitem').map((item) => item.lastChild?.textContent)).toEqual(codes)
    shown.getByText("These codes won't be shown again.")
    await expectAccessible()

    // Escape and a click outside leave it open.
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('dialog'))
    expect(screen.getByRole('dialog', { name: 'Keep your recovery codes' })).toBeTruthy()
    // Closing or reloading the tab meanwhile makes the browser ask first.
    const leaving = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(leaving)
    expect(leaving.defaultPrevented).toBe(true)

    // Done does nothing until the person says they kept them.
    await user.click(shown.getByRole('button', { name: 'Done' }))
    const box = shown.getByRole('checkbox', { name: "I've kept these codes" })
    expect(
      shown
        .getByText("Tick the box once you've kept the codes. They can't be shown again.")
        .getAttribute('role'),
    ).toBe('alert')
    await waitFor(() => expect(document.activeElement).toBe(box))
    expect(box.getAttribute('aria-invalid')).toBe('true')
    await expectAccessible()

    // They are nowhere but on screen while they are shown.
    expectNotKept(...codes)
    await user.click(box)
    await user.click(shown.getByRole('button', { name: 'Done' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    section().getByText('Recovery codes kept')
    section().getByText('On')
    section().getByText('10 left')
    // And gone from the page once the person said so.
    expect(document.body.textContent).not.toContain(codes[0])
    expectNotKept(...codes)
    const gone = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(gone)
    expect(gone.defaultPrevented).toBe(false)
  })

  it('copies all the codes, one a line, because the person asked', async () => {
    const { user } = await settings({ passkeys: [phone] })
    const written: string[] = []
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async (text: string) => void written.push(text) },
    })
    server.on(
      'POST /api/v1/account/passkey-only',
      ok({ passkey_only: true, recovery_codes: codes }),
    )
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    await user.click(screen.getByRole('button', { name: 'Turn on passkey only' }))
    await user.click(await screen.findByRole('button', { name: 'Copy all' }))
    expect((await screen.findByText('Copied 10 codes.')).getAttribute('role')).toBe('status')
    expect(written).toEqual([codes.join('\n')])

    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async () => {
          throw new Error('denied')
        },
      },
    })
    await user.click(screen.getByRole('button', { name: 'Copy all' }))
    await screen.findByText("Couldn't copy. Select the codes and copy them by hand.")
    Reflect.deleteProperty(navigator, 'clipboard')
  })

  it('is not offered without a passkey', async () => {
    await settings()
    await settle()
    expect(section().queryByRole('button', { name: named('Turn on passkey only') })).toBeNull()
    section().getByText('Add a passkey to turn this on.')
  })

  it('says why it was not turned on, and can be left as it is', async () => {
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    const question = within(screen.getByRole('dialog'))
    // The passkey was removed meanwhile, from another device.
    server.on('POST /api/v1/account/passkey-only', () => {
      server.settings()
      return refused(409, 'passkey_needed')
    })
    await user.click(question.getByRole('button', { name: 'Turn on passkey only' }))
    await question.findByText('Add a passkey before turning on passkey only.')
    await user.click(question.getByRole('button', { name: 'Not now' }))
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    await section().findByText('Add a passkey to turn this on.')
  })

  it('shows no codes when it was on already, turned on from another device', async () => {
    const { user } = await settings({ passkeys: [phone] })
    await user.click(section().getByRole('button', { name: named('Turn on passkey only') }))
    server.on('POST /api/v1/account/passkey-only', () => {
      server.settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 8 })
      return ok({ passkey_only: true, recovery_codes: [] })
    })
    await user.click(screen.getByRole('button', { name: 'Turn on passkey only' }))
    await section().findByText('Passkey only was on already')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await section().findByText('8 left')
  })

  it('turns off after asking: mail signs in again and the codes stop working', async () => {
    const { user } = await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 3 })
    await user.click(section().getByRole('button', { name: named('Turn off passkey only') }))
    const question = within(screen.getByRole('dialog', { name: 'Turn off passkey only?' }))
    expect(question.getByText(/will sign you in again/).textContent).toBe(
      'A message sent to ana@example.org will sign you in again, so whoever reads that mailbox can get in.',
    )
    question.getByText('Your recovery codes stop working. Your passkeys keep working.')
    question.getByRole('button', { name: 'Keep it on' })
    await expectAccessible()
    server.on('POST /api/v1/account/passkey-only', () => {
      server.settings({ passkeys: [phone] })
      return ok({ passkey_only: false })
    })
    await user.click(question.getByRole('button', { name: 'Turn off passkey only' }))
    await section().findByText('Passkey only is off')
    expect(server.asked('POST /api/v1/account/passkey-only')[0].body).toEqual({ on: false })
    await section().findByText('Off')
    await settle()
    expect(section().queryByText('Recovery codes')).toBeNull()
  })

  it.each([
    [
      3,
      'You get 10 new codes, shown once. The 3 codes you have left stop working as soon as they are made.',
      '3 left',
    ],
    [
      1,
      'You get 10 new codes, shown once. The code you have left stops working as soon as they are made.',
      '1 left',
    ],
    [0, 'You get 10 new codes, shown once.', 'None left'],
  ])('makes new codes after asking, saying how many stop working: %i', async (left, words, row) => {
    const { user } = await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: left })
    section().getByText(row)
    await user.click(section().getByRole('button', { name: 'Make new codes' }))
    const question = within(screen.getByRole('dialog', { name: 'Make new recovery codes?' }))
    question.getByText(words)
    question.getByRole('button', { name: 'Keep the codes I have' })
    server.on('POST /api/v1/account/recovery-codes', ok({ recovery_codes: codes }))
    await user.click(question.getByRole('button', { name: 'Make new codes' }))
    const shown = within(await screen.findByRole('dialog', { name: 'Keep your recovery codes' }))
    shown.getByText(/The codes you had before no longer work\./)
    expect(shown.getAllByRole('listitem')).toHaveLength(10)
  })

  it('says why no new codes were made', async () => {
    const { user } = await settings({ passkeys: [phone], passkeyOnly: true, codesLeft: 3 })
    await user.click(section().getByRole('button', { name: 'Make new codes' }))
    const question = within(screen.getByRole('dialog'))
    server.on('POST /api/v1/account/recovery-codes', network)
    await user.click(question.getByRole('button', { name: 'Make new codes' }))
    await question.findByText("Permaily can't be reached. Check your connection, then try again.")
    // Turned off meanwhile, from another device: the rows say how it is now.
    server.on('POST /api/v1/account/recovery-codes', () => {
      server.settings({ passkeys: [phone] })
      return refused(409, 'passkey_only_off')
    })
    await user.click(question.getByRole('button', { name: 'Make new codes' }))
    await question.findByText('There are recovery codes only while passkey only is on.')
    await user.click(question.getByRole('button', { name: 'Keep the codes I have' }))
    await section().findByText('Off')
  })
})

describe('providers', () => {
  const linkedGoogle = { provider: 'google', linked_at: '2026-03-06T10:00:00Z' }

  it('shows a row for each that is on or linked, in the fixed order, named in words', async () => {
    await settings({ on: ['microsoft', 'meta', 'google'], linked: [linkedGoogle] })
    section().getByText('Linked since 6 Mar 2026')
    expect(section().getAllByText('Not linked')).toHaveLength(2)
    section().getByRole('button', { name: named('Unlink Google') })
    // The identifier is meta; Settings lists it as Facebook.
    section().getByRole('button', { name: named('Link Facebook') })
    section().getByRole('button', { name: named('Link Microsoft') })
    await settle()
    expect(section().queryByText('Meta')).toBeNull()
    expect(section().queryByText('GitHub')).toBeNull()
    await expectAccessible()
  })

  it('shows nothing of providers when none is on or linked', async () => {
    await settings()
    await settle()
    expect(section().queryByText('Not linked')).toBeNull()
    expect(section().queryByText('Google')).toBeNull()
  })

  it('links by leaving for the address the server answered, to come back to Settings', async () => {
    const assign = watchLeaving()
    const { user } = await settings({ on: ['google', 'meta'] })
    const starting = later()
    server.on('POST /api/v1/providers/start', starting.answer)
    await user.click(section().getByRole('button', { name: named('Link Facebook') }))
    section().getByText('Taking you to Facebook')
    expect(
      (section().getByRole('button', { name: named('Link Google') }) as HTMLButtonElement).disabled,
    ).toBe(true)
    starting.give(ok({ url: 'https://www.facebook.com/dialog/oauth?state=x', expires_in: 600 }))
    await waitFor(() =>
      expect(assign).toHaveBeenCalledExactlyOnceWith(
        'https://www.facebook.com/dialog/oauth?state=x',
      ),
    )
    expect(server.asked('POST /api/v1/providers/start')[0].body).toEqual({
      provider: 'meta',
      return_to: '/settings#linked=meta',
    })
    // Linking keeps no invitation, and nothing else, for the trip.
    expect({ ...sessionStorage }).toEqual({})
  })

  it.each([
    [refused(409, 'provider_off'), "Signing in with Google isn't available right now."],
    [refused(409, 'provider_linked'), 'This account has a Google account linked already.'],
    [
      network,
      "Couldn't start linking Google. Permaily can't be reached. Check your connection, then try again.",
    ],
    [
      ok({ url: 'javascript:alert(1)' }),
      "Couldn't start linking Google. Something went wrong on our side. Try again in a moment.",
    ],
  ])('says why linking did not start', async (answer, words) => {
    const assign = watchLeaving()
    const { user } = await settings({ on: ['google'] })
    server.on('POST /api/v1/providers/start', answer)
    await user.click(section().getByRole('button', { name: named('Link Google') }))
    expect((await section().findByText(words)).getAttribute('role')).toBe('alert')
    expect(assign).not.toHaveBeenCalled()
  })

  it('says a provider is linked only when the server list agrees with the address', async () => {
    await settings({ on: ['google'], linked: [linkedGoogle] }, '/settings#linked=google')
    expect(current()).toBe('/settings')
    const said = await section().findByText('Google is linked. It signs you in from now on.')
    expect(said.getAttribute('role')).toBe('status')
    await waitFor(() => expect(document.activeElement).toBe(said))
    await expectAccessible()

    // Anyone can write an address: this one says what the server does not.
    await settings({ on: ['google', 'meta'], linked: [linkedGoogle] }, '/settings#linked=meta')
    expect(current()).toBe('/settings')
    await settle()
    expect(section().queryByText(/is linked\./)).toBeNull()
    section().getByRole('button', { name: named('Link Facebook') })
  })

  it('says a linked provider signs nobody in while passkey only is on', async () => {
    await settings(
      {
        on: ['google'],
        linked: [linkedGoogle],
        passkeys: [phone],
        passkeyOnly: true,
        codesLeft: 10,
      },
      '/settings#linked=google',
    )
    await section().findByText("Google is linked. It won't sign you in while passkey only is on.")
    section().getByText("Doesn't sign you in while passkey only is on.")
  })

  it.each([
    [
      'provider_in_use',
      '',
      'Nothing was linked: that account belongs with another Permaily account.',
    ],
    [
      'provider_in_use',
      '&provider=meta',
      'Nothing was linked: that Facebook account belongs with another Permaily account.',
    ],
    [
      'provider_linked',
      '',
      'Nothing was linked: this account has an account of that provider linked already. Unlink it first.',
    ],
    [
      'provider_linked',
      '&provider=google',
      'Nothing was linked: this account has another Google account linked already. Unlink it first.',
    ],
    [
      'provider_unavailable',
      '',
      "Nothing was linked: that isn't working right now. Try again later.",
    ],
    [
      'provider_unavailable',
      '&provider=github',
      "Nothing was linked: GitHub isn't working right now. Try again later.",
    ],
    ['provider_failed', '', "Nothing was linked: that didn't work. Try again."],
    [
      'account_blocked',
      '&provider=google',
      'Nothing was linked: that account is blocked. To ask about it, write to support@permaily.com.',
    ],
    [
      'provider_failed',
      '&provider=microsoft',
      "Nothing was linked: signing in with Microsoft didn't work. Try again.",
    ],
    ['invitation_needed', '&provider=apple', "Nothing was linked: that didn't work. Try again."],
    [
      '%3Cb%3Ehacked%3C%2Fb%3E',
      '&provider=%3Cb%3E',
      "Nothing was linked: that didn't work. Try again.",
    ],
  ])('says in fixed words why nothing was linked: %s%s', async (reason, provider, words) => {
    await settings({ on: ['google'] }, `/settings#provider_error=${reason}${provider}`)
    expect(current()).toBe('/settings')
    const said = await section().findByText(words)
    expect(said.getAttribute('role')).toBe('alert')
    await waitFor(() => expect(document.activeElement).toBe(said))
    expect(document.body.innerHTML).not.toContain('hacked')
  })

  it('shows the closing screen when linking found the account being deleted', async () => {
    server.settings({ on: ['google'] }).on('GET /api/v1/me', () => {
      // The first answer is from before the deletion was asked for elsewhere.
      server.signedIn({ ...meOf().account, state: 'closing', closes_at: '2026-11-18T14:32:00Z' })
      return ok(meOf())
    })
    await open('/settings#provider_error=account_closing&provider=google')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
  })

  it('unlinks after asking, and says Permaily may stay listed at the provider', async () => {
    const { user } = await settings({ on: ['google', 'meta'], linked: [linkedGoogle] })
    await user.click(section().getByRole('button', { name: named('Unlink Google') }))
    const dialog = within(screen.getByRole('dialog', { name: 'Unlink Google?' }))
    await waitFor(() => expect(document.activeElement).toBe(dialog.getByRole('heading')))
    dialog.getByText('Google stops signing you in to Permaily now. You can link it again later.')
    dialog.getByText('Permaily may stay listed in your Google account until you remove it there.')
    await expectAccessible()
    server.on('DELETE /api/v1/providers/google', () => {
      server.settings({ on: ['google', 'meta'] })
      return ok()
    })
    await user.click(dialog.getByRole('button', { name: 'Unlink Google' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await section().findByRole('button', { name: named('Link Google') })
    section().getByText('Google unlinked')
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Sign-in' })),
    )
  })

  it('takes one unlinked already for done, says a refusal in the question, and keeps on request', async () => {
    const { user } = await settings({ on: ['google'], linked: [linkedGoogle] })
    await user.click(section().getByRole('button', { name: named('Unlink Google') }))
    let dialog = within(screen.getByRole('dialog'))
    await user.click(dialog.getByRole('button', { name: 'Keep it' }))
    expect(server.asked('DELETE /api/v1/providers/google')).toHaveLength(0)

    await user.click(section().getByRole('button', { name: named('Unlink Google') }))
    dialog = within(screen.getByRole('dialog'))
    server.on('DELETE /api/v1/providers/google', network)
    await user.click(dialog.getByRole('button', { name: 'Unlink Google' }))
    await dialog.findByText("Permaily can't be reached. Check your connection, then try again.")
    server.on('DELETE /api/v1/providers/google', () => {
      server.settings({ on: ['google'] })
      return refused(404, 'not_found')
    })
    await user.click(dialog.getByRole('button', { name: 'Unlink Google' }))
    await section().findByRole('button', { name: named('Link Google') })
  })

  it('has no Unlink for the only way left to sign in, and says why', async () => {
    // Passkey only, no passkey that works here, and one provider: nothing else signs in.
    const { user } = await settings({
      on: ['google'],
      linked: [linkedGoogle, { provider: 'meta', linked_at: '2026-04-01T10:00:00Z' }],
      passkeyOnly: true,
    })
    // Two are linked, so either can go; and one that is off says so.
    section().getByText('Signing in with Facebook is off right now.')
    await user.click(section().getByRole('button', { name: named('Unlink Facebook') }))
    const dialog = within(screen.getByRole('dialog'))
    server.on('DELETE /api/v1/providers/meta', () => {
      server.settings({ on: ['google'], linked: [linkedGoogle], passkeyOnly: true })
      return refused(409, 'last_way_in')
    })
    await user.click(dialog.getByRole('button', { name: 'Unlink Facebook' }))
    await dialog.findByText(
      'That is the only way left to sign in to this account. Add another first.',
    )
    await user.click(dialog.getByRole('button', { name: 'Keep it' }))
    await section().findByText("Can't be unlinked: it is the only way left to sign in.")
    await settle()
    expect(section().queryByRole('button', { name: named('Unlink Google') })).toBeNull()
    await expectAccessible()
  })
})

describe('sessions', () => {
  it('counts the devices, and lists each on request: this browser first', async () => {
    const tablet = {
      ...laptop,
      id: 'ses_tablet',
      last_used_at: '2026-11-08T08:00:00Z',
      device:
        'Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Safari/604.1',
    }
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-10T10:30:00Z') })
    const { user } = await settings({ sessions: [tablet, laptop, here] })
    section().getByText('3 devices signed in')
    const review = section().getByRole('button', { name: named('Review sessions') })
    expect(review.getAttribute('aria-expanded')).toBe('false')
    await user.click(review)
    expect(
      section()
        .getAllByRole('listitem')
        .map((item) => item.firstChild?.textContent),
    ).toEqual([
      'Firefox on Linux · This browser',
      // To the hour at best, which is all the server keeps.
      'Safari on Mac · Last used 2 hours ago',
      'Safari on iPad · Last used 2 days ago',
    ])
    // This browser is signed out from the account menu, not from here.
    expect(section().getAllByRole('button', { name: /^Sign out/ })).toHaveLength(2)
    await expectAccessible()
    await user.click(section().getByRole('button', { name: named('Done sessions') }))
    await settle()
    expect(section().queryByRole('list')).toBeNull()
  })

  it('signs another device out', async () => {
    const { user } = await settings({ sessions: [here, laptop] })
    await user.click(section().getByRole('button', { name: named('Review sessions') }))
    server.on('DELETE /api/v1/sessions/ses_laptop', () => {
      server.settings()
      return ok()
    })
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    await section().findByText('1 device signed in')
    section().getByText('Signed out Safari on Mac')
    await settle()
    expect(section().queryByRole('button', { name: /^Sign out/ })).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(
        section().getByRole('button', { name: named('Done sessions') }),
      ),
    )
  })

  it('says why a device was not signed out, and takes one signed out already for done', async () => {
    const { user } = await settings({ sessions: [here, laptop] })
    await user.click(section().getByRole('button', { name: named('Review sessions') }))
    server.on('DELETE /api/v1/sessions/ses_laptop', network)
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    await section().findByText(
      "Couldn't sign Safari on Mac out: Permaily can't be reached. Check your connection, then try again.",
    )
    server.on('DELETE /api/v1/sessions/ses_laptop', refused(429, 'too_many_requests'))
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    await section().findByText(
      "Couldn't sign Safari on Mac out. Too many attempts. Wait a few minutes, then try again.",
    )
    server.on('DELETE /api/v1/sessions/ses_laptop', () => {
      server.settings()
      return refused(404, 'not_found')
    })
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    await section().findByText('1 device signed in')
  })

  it('says when a later reading failed, with what it has still on screen', async () => {
    const { user } = await settings({ sessions: [here, laptop] })
    await user.click(section().getByRole('button', { name: named('Review sessions') }))
    server.on('DELETE /api/v1/sessions/ses_laptop', () => {
      server.on('GET /api/v1/sessions', network)
      return ok()
    })
    await user.click(section().getByRole('button', { name: /^Sign out/ }))
    const alert = await section().findByText(/This couldn't be brought up to date\./)
    expect(alert.textContent).toBe(
      "This couldn't be brought up to date. Permaily can't be reached. Check your connection, then try again.",
    )
    section().getByText('2 devices signed in')
  })
})
