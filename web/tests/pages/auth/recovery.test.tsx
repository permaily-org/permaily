import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { answered, browserWithPasskeys, creationOptions } from '../../helpers/passkeys.ts'
import { meOf, network, ok, refused, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const refusal =
  "That address and recovery code didn't sign you in. Check both: each code works once. After 5 wrong tries, wait an hour."

/** Opens the step and fills it in. */
async function recover(user: User, code = 'abcd2345efgh6789') {
  await user.click(await screen.findByRole('button', { name: 'Use a recovery code' }))
  await user.type(screen.getByLabelText('Email address'), 'ana@example.org')
  await user.type(screen.getByLabelText('Recovery code'), code)
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('signing in with a recovery code', () => {
  it('asks for the address and one code, written in groups as it is typed', async () => {
    server.signedOut()
    const { user } = await open('/sign-in')
    await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
    await user.click(screen.getByRole('button', { name: 'Use a recovery code' }))
    await screen.findByRole('heading', { name: 'Use a recovery code' })
    // The address typed before is kept, so the first thing left to fill in takes focus.
    const code = screen.getByLabelText('Recovery code') as HTMLInputElement
    expect((screen.getByLabelText('Email address') as HTMLInputElement).value).toBe(
      'ana@example.org',
    )
    await waitFor(() => expect(document.activeElement).toBe(code))
    await user.type(code, 'abcd-2345 efgh6789')
    expect(code.value).toBe('ABCD-2345-EFGH-6789')
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: 'Sign in another way' }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('says what cannot be an address or a code before anything is asked', async () => {
    server.signedOut()
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Use a recovery code' }))
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Email address')))
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('Enter your email address.')
    await user.type(screen.getByLabelText('Email address'), 'ana@example.org')
    await user.type(screen.getByLabelText('Recovery code'), 'abcd')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText(
      'A recovery code has 16 letters and digits. Check it against the ones you kept.',
    )
    expect(server.asked('POST /api/v1/auth/recovery')).toHaveLength(0)
    await expectAccessible()
  })

  it.each([
    ['invalid_code', 400],
    ['too_many_requests', 429],
    ['not_found', 404],
    ['passkey_only_off', 409],
    ['invalid_address', 400],
  ])(
    'answers every refusal (%s) with the same words: it never says whether an address has an account',
    async (code, status) => {
      server.signedOut().on('POST /api/v1/auth/recovery', refused(status, code))
      const { user } = await open('/sign-in')
      await recover(user)
      expect((await screen.findByText(refusal)).getAttribute('role')).toBe('alert')
      await waitFor(() =>
        expect(document.activeElement).toBe(screen.getByLabelText('Recovery code')),
      )
    },
  )

  it('reads no answer differently, since nothing was refused', async () => {
    server.signedOut().on('POST /api/v1/auth/recovery', network)
    const { user } = await open('/sign-in')
    await recover(user)
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    server.on('POST /api/v1/auth/recovery', refused(500, 'internal'))
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('Something went wrong on our side. Try again in a moment.')
  })

  it('leads to the sign-in section of Settings, which invites the person to add a passkey', async () => {
    browserWithPasskeys()
    server
      .signedOut()
      .settings({
        passkeys: [{ id: 'cred-lost', name: 'Lost phone' }],
        passkeyOnly: true,
        codesLeft: 9,
      })
      .on('POST /api/v1/auth/recovery', ok(meOf()))
    // They were going somewhere else: a recovery code was just used up, so Settings comes first.
    const { user } = await open('/accounts')
    await recover(user)
    const invited = await screen.findByText(
      "You signed in with a recovery code, and it is used up. Add a passkey, so you don't need another.",
    )
    expect(current()).toBe('/settings')
    expect(invited.getAttribute('role')).toBe('status')
    await waitFor(() => expect(document.activeElement).toBe(invited))
    expect(invited.closest('section')?.getAttribute('aria-labelledby')).toBe('sign-in')
    expect(screen.getByRole('button', { name: 'Add a passkey' })).toBeTruthy()
    expect(server.asked('POST /api/v1/auth/recovery')[0].body).toEqual({
      address: 'ana@example.org',
      code: 'abcd2345efgh6789',
    })
    expectNotKept('abcd2345efgh6789', 'ABCD-2345-EFGH-6789', 'ana@example.org')
    await expectAccessible()

    // It is said once: not after a reload.
    server.signedIn()
    await open(current())
    await screen.findByText('9 left')
    await settle()
    expect(screen.queryByText(/You signed in with a recovery code/)).toBeNull()
  })

  it('stops inviting once a passkey is added', async () => {
    browserWithPasskeys({ create: async () => answered({ id: 'cred-new' }) })
    server
      .signedOut()
      .settings({ passkeys: [{ id: 'cred-lost', name: 'Lost phone' }], passkeyOnly: true })
      .on('POST /api/v1/auth/recovery', ok(meOf()))
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: creationOptions }))
      .on('POST /api/v1/passkeys/finish', () => {
        server.settings({
          passkeys: [
            { id: 'cred-lost', name: 'Lost phone' },
            { id: 'cred-new', name: 'New laptop' },
          ],
          passkeyOnly: true,
        })
        return ok({ id: 'cred-new', name: 'New laptop' })
      })
    const { user } = await open('/sign-in')
    await recover(user)
    await screen.findByText(/You signed in with a recovery code/)
    await user.click(screen.getByRole('button', { name: 'Add a passkey' }))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByText('New laptop')
    await waitFor(() => expect(screen.queryByText(/You signed in with a recovery code/)).toBeNull())
  })

  it('says where to add one when this browser cannot make passkeys', async () => {
    server
      .signedOut()
      .settings({ passkeys: [{ id: 'cred-lost', name: 'Lost phone' }], passkeyOnly: true })
      .on('POST /api/v1/auth/recovery', ok(meOf()))
    const { user } = await open('/sign-in')
    await recover(user)
    await screen.findByText(
      "You signed in with a recovery code, and it is used up. This browser can't make passkeys: add one from a browser that can.",
    )
    await settle()
    expect(screen.queryByRole('button', { name: 'Add a passkey' })).toBeNull()
  })
})
