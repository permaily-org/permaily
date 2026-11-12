import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  answered,
  browserError,
  browserWithPasskeys,
  requestOptions,
} from '../../helpers/passkeys.ts'
import { meOf, network, ok, refused, server } from '../../helpers/server.ts'

const ceremony = ok({ ceremony: 'cer_secret_1', options: requestOptions })

describe('signing in with a passkey', () => {
  it('is not offered where the browser has no passkeys', async () => {
    server.signedOut()
    await open('/sign-in')
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(screen.queryByRole('button', { name: 'Sign in with a passkey' })).toBeNull()
  })

  it('is one action and no address, and the prompt opens only when the person asks', async () => {
    const { get } = browserWithPasskeys({
      get: async () => answered({ id: 'cred-1', response: { signature: 'sig_secret' } }),
    })
    server.signedOut().on('POST /api/v1/auth/passkey/start', ceremony)
    const { user } = await open('/sign-in')
    const action = await screen.findByRole('button', { name: 'Sign in with a passkey' })
    expect(get).not.toHaveBeenCalled()
    expect(server.asked('POST /api/v1/auth/passkey/start')).toHaveLength(0)
    // Not on the sign-up screen: a passkey is for an account that exists.
    await expectAccessible()

    server.on('POST /api/v1/auth/passkey/finish', ok(meOf()))
    await user.click(action)
    await screen.findByRole('navigation', { name: 'Main' })
    expect(get).toHaveBeenCalledTimes(1)
    expect(server.asked('POST /api/v1/auth/passkey/finish')[0].body).toEqual({
      ceremony: 'cer_secret_1',
      credential: { id: 'cred-1', response: { signature: 'sig_secret' } },
    })
    expectNotKept('cer_secret_1', 'sig_secret', 'cred-1')
  })

  it('is as tall as the providers next to it', async () => {
    browserWithPasskeys({})
    server
      .signedOut()
      .on(
        'GET /api/v1/auth/providers',
        ok({ providers: ['google', 'github', 'meta', 'microsoft'] }),
      )
    await open('/sign-in')
    const passkey = await screen.findByRole('button', { name: 'Sign in with a passkey' })
    await screen.findByRole('button', { name: 'Continue with Google' })
    const providers = screen
      .getAllByRole('button', { name: /^(Continue|Sign in) with / })
      .filter((button) => button !== passkey)
    expect(providers).toHaveLength(4)
    // jsdom lays nothing out, so the height is the class that fixes it: one, and the same on every button.
    const height = (button: HTMLElement) => [...button.classList].filter((name) => /^h-/.test(name))
    expect(height(passkey)).toHaveLength(1)
    for (const provider of providers) expect(height(provider)).toEqual(height(passkey))
  })

  it('is not offered on the sign-up screen', async () => {
    browserWithPasskeys()
    server.signedOut()
    await open('/sign-up')
    await screen.findByRole('heading', { name: 'Create your account' })
    await settle()
    expect(screen.queryByRole('button', { name: 'Sign in with a passkey' })).toBeNull()
  })

  it('says calmly that no passkey was used when the prompt was closed', async () => {
    browserWithPasskeys({
      get: async () => {
        throw browserError('NotAllowedError')
      },
    })
    server.signedOut().on('POST /api/v1/auth/passkey/start', ceremony)
    const { user } = await open('/sign-in')
    const action = await screen.findByRole('button', { name: 'Sign in with a passkey' })
    await user.click(action)
    const said = await screen.findByText('No passkey was used. You can try again.')
    // A status, not an error: a browser does not say whether it was closed or found no passkey.
    expect(said.getAttribute('role')).toBe('status')
    expect(server.asked('POST /api/v1/auth/passkey/finish')).toHaveLength(0)
    await waitFor(() => expect(document.activeElement).toBe(action))
    await expectAccessible()
  })

  it.each([
    [
      'SecurityError',
      "This browser can't use a passkey here. Sign in with your email address instead.",
    ],
    ['UnknownError', "The passkey couldn't be read. Try again."],
  ])('says as an error that the browser could not: %s', async (name, words) => {
    browserWithPasskeys({
      get: async () => {
        throw browserError(name)
      },
    })
    server.signedOut().on('POST /api/v1/auth/passkey/start', ceremony)
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in with a passkey' }))
    expect((await screen.findByText(words)).getAttribute('role')).toBe('alert')
  })

  it('says what may be so when the server refuses, and tells the authenticator nothing', async () => {
    // The wait may just have been too long: the passkey may be a good one, so it is not called unknown.
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({
      get: async () => answered({ id: 'cred-1' }),
      statics: { signalUnknownCredential },
    })
    server
      .signedOut()
      .on('POST /api/v1/auth/passkey/start', ceremony)
      .on('POST /api/v1/auth/passkey/finish', refused(400, 'invalid_passkey'))
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in with a passkey' }))
    const said = await screen.findByText(
      "That passkey didn't sign you in. It may have been removed from its account, or the wait was too long. Try again, or sign in another way.",
    )
    expect(said.getAttribute('role')).toBe('alert')
    expect(signalUnknownCredential).not.toHaveBeenCalled()
  })

  it('says when the server cannot be reached, before the prompt opens', async () => {
    const { get } = browserWithPasskeys()
    server.signedOut().on('POST /api/v1/auth/passkey/start', network)
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in with a passkey' }))
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    expect(get).not.toHaveBeenCalled()
  })

  it('closes a prompt that is still open when the person leaves the screen', async () => {
    let signal: AbortSignal | undefined
    browserWithPasskeys({
      get: (asked) => {
        signal = asked.signal ?? undefined
        return new Promise((_, reject) => {
          asked.signal?.addEventListener('abort', () => reject(browserError('AbortError')))
        })
      },
    })
    server.signedOut().on('POST /api/v1/auth/passkey/start', ceremony)
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in with a passkey' }))
    await waitFor(() => expect(signal).toBeDefined())
    expect(signal?.aborted).toBe(false)
    await user.click(screen.getByRole('link', { name: 'Create an account' }))
    await screen.findByRole('heading', { name: 'Create your account' })
    expect(signal?.aborted).toBe(true)
  })
})
