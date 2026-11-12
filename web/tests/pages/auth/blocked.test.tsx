import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { agree, current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { answered, browserWithPasskeys, requestOptions } from '../../helpers/passkeys.ts'
import { ana, meOf, ok, refused, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

/** What the server answers a sign-in that proved who it is and ends at an account the operator blocked. */
const blocked = refused(403, 'account_blocked')
const title = 'That account is blocked'
const words =
  'Nobody was signed in. To ask about it, write to support@permaily.com. You can still sign in to another account.'
const eve = { ...ana, id: 'acc_eve', address: 'eve@example.org', name: 'Eve' }

/**
 * The sign-in screen says that the account is blocked and where to write, in fixed words, and is ready for
 * another account: nothing on it offers to try the same again.
 */
async function saysBlocked() {
  await screen.findByRole('heading', { level: 1, name: title })
  expect(document.getElementById('account-blocked')?.textContent).toBe(words)
  // The address to write to opens the person's own mail app: the app itself sends nothing.
  const support = screen.getByRole('link', { name: 'support@permaily.com' })
  expect(support.getAttribute('href')).toBe('mailto:support@permaily.com')
  expect(support.hasAttribute('target')).toBe(false)
  // It is not an error to try again: nothing here would only fail the same way.
  for (const alert of screen.queryAllByRole('alert')) expect(alert.textContent).toBe('')
  expect(screen.queryByRole('button', { name: /try again|send another/i })).toBeNull()
  // Another account can sign in from here: the address field takes focus, and reads the words with it.
  const address = screen.getByLabelText('Email address') as HTMLInputElement
  expect(address.getAttribute('aria-describedby')).toBe('account-blocked')
  expect(address.value).toBe('')
  await waitFor(() => expect(document.activeElement).toBe(address))
  expect(current()).toBe('/sign-in')
  await waitFor(() => expect(document.title).toBe(`${title} · Permaily`))
  expect({ ...localStorage }).toEqual({})
  expect({ ...sessionStorage }).toEqual({})
  await expectAccessible()
}

/** Asks for the message of a sign-in by mail and enters its code. */
async function codeOf(user: User, address = 'ana@example.org') {
  await user.type(await screen.findByLabelText('Email address'), address)
  await agree(user)
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(await screen.findByLabelText('Code from the message'), '123456')
}

describe('a sign-in that ends at a blocked account', () => {
  it('says so after the code of a message, and another account can still sign in', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/start', ok({ attempt: 'att_secret_1', expires_in: 600 }))
      .on('POST /api/v1/auth/email/finish', blocked)
    const { user } = await open('/sign-in')
    await codeOf(user)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await saysBlocked()
    expectNotKept('123456', 'att_secret_1', 'ana@example.org')

    // The words stay until the person tries something else.
    server.on('POST /api/v1/auth/email/finish', ok(meOf(eve)))
    await codeOf(user, 'eve@example.org')
    await settle()
    expect(screen.queryByText(/blocked/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain(
      'eve@example.org',
    )
  })

  it('says so on the sign-in screen when it was the code of a sign-up', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on('POST /api/v1/auth/email/finish', blocked)
    const { user } = await open('/sign-up?invitation=abcd2345efgh')
    await codeOf(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    await saysBlocked()
  })

  it('says so after the link of a message, once the person said yes', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/look', ok({ address: 'ana@example.org' }))
      .on('POST /api/v1/auth/email/finish', blocked)
    const { user } = await open('/sign-in#attempt=att_link_1&token=tok_secret_1')
    // Nothing says so before the proof: the question is the same as for any account.
    await screen.findByRole('heading', { name: 'Sign in to this account?' })
    await settle()
    expect(screen.queryByText(/blocked/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await saysBlocked()
    expectNotKept('tok_secret_1', 'att_link_1', 'ana@example.org')
    // Not across a reload.
    await open(current())
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('tells a browser that is in another account that it still is', async () => {
    server
      .signedIn(eve)
      .on('POST /api/v1/auth/email/look', ok({ address: 'ana@example.org' }))
      .on('POST /api/v1/auth/email/finish', blocked)
    const { user } = await open('/sign-in#attempt=att_link_1&token=tok_secret_1')
    await user.click(await screen.findByRole('button', { name: 'Switch account' }))
    const ended = await screen.findByRole('heading', { name: title })
    await waitFor(() => expect(document.activeElement).toBe(ended))
    expect(document.getElementById('link-ended')?.textContent).toBe(
      'Nobody was signed in. To ask about it, write to support@permaily.com. This browser is still signed in as eve@example.org.',
    )
    expect(screen.getByRole('link', { name: 'support@permaily.com' }).getAttribute('href')).toBe(
      'mailto:support@permaily.com',
    )
    // The one action opens the app as it was: nothing tries the link again.
    expect(screen.getAllByRole('button').map((button) => button.textContent)).toEqual([
      'Open Permaily',
    ])
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: 'Open Permaily' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain(
      'eve@example.org',
    )
  })

  it('says so after a passkey, as often as it is tried, and tells the authenticator nothing', async () => {
    const signalUnknownCredential = vi.fn(async () => {})
    browserWithPasskeys({
      get: async () => answered({ id: 'cred-1', response: { signature: 'sig_secret' } }),
      statics: { signalUnknownCredential },
    })
    server
      .signedOut()
      .on(
        'POST /api/v1/auth/passkey/start',
        ok({ ceremony: 'cer_secret_1', options: requestOptions }),
      )
      .on('POST /api/v1/auth/passkey/finish', blocked)
    const { user } = await open('/sign-in')
    const passkey = await screen.findByRole('button', { name: 'Sign in with a passkey' })
    await user.click(passkey)
    await saysBlocked()
    // Nothing is said under the action, as an error or otherwise, and it works again for another account.
    expect(screen.queryByText(/passkey didn't sign you in|Try again/)).toBeNull()
    expect((passkey as HTMLButtonElement).disabled).toBe(false)
    // The passkey was a good one: the authenticator is not told to forget it.
    expect(signalUnknownCredential).not.toHaveBeenCalled()
    expectNotKept('cer_secret_1', 'sig_secret', 'cred-1')

    // The same passkey again reads the same, and focus is where the person goes on.
    await user.click(passkey)
    await waitFor(() => expect(server.asked('POST /api/v1/auth/passkey/finish')).toHaveLength(2))
    await saysBlocked()
  })

  it('says so after a recovery code, and only then reads differently from every refusal', async () => {
    server.signedOut().on('POST /api/v1/auth/recovery', blocked)
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Use a recovery code' }))
    await user.type(screen.getByLabelText('Email address'), 'ana@example.org')
    await user.type(screen.getByLabelText('Recovery code'), 'abcd2345efgh6789')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await saysBlocked()
    expect(screen.queryByText(/didn't sign you in/)).toBeNull()
    expectNotKept('abcd2345efgh6789', 'ABCD-2345-EFGH-6789', 'ana@example.org')
  })

  it('says so after the code that confirms an address for a provider', async () => {
    server
      .signedOut()
      .on(
        'GET /api/v1/auth/provider/pending',
        ok({
          provider: 'meta',
          address: 'ana@facebook.example',
          return_to: '/accounts',
          expires_in: 900,
        }),
      )
      .on(
        'POST /api/v1/auth/provider/confirm/start',
        ok({ attempt: 'att_confirm_1', expires_in: 600 }),
      )
      .on('POST /api/v1/auth/provider/confirm/finish', blocked)
    const { user } = await open('/sign-in#provider_confirm')
    await user.click(await screen.findByRole('button', { name: 'Send the message' }))
    await user.type(await screen.findByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Confirm and sign in' }))
    await saysBlocked()
    // What waited is left for good, with where the person was going.
    expect(window.history.state.usr).toBeNull()
    expectNotKept('123456', 'att_confirm_1', 'ana@facebook.example')
  })

  it.each([
    'account_blocked',
    'account_blocked&provider=google',
    'account_blocked&provider=%3Cb%3EAna%20at%20ana%40example.org%3C%2Fb%3E',
  ])(
    'says so when a provider came back with %s, and takes it out of the address',
    async (fragment) => {
      server.signedOut().on('GET /api/v1/auth/providers', ok({ providers: ['google'] }))
      const { container } = await open(`/sign-in#provider_error=${fragment}`)
      await saysBlocked()
      // The reason is a word of the address, which anyone can write: only the fixed words are shown, and
      // they name neither a provider nor a person.
      const page = container.innerHTML
      for (const written of ['account_blocked', '<b>', 'ana@example.org']) {
        expect(page).not.toContain(written)
      }
      // The providers are still offered, for another account.
      expect(screen.getByRole('button', { name: 'Continue with Google' })).toBeTruthy()
    },
  )

  it('keeps the words of a provider until the person tries something else, and not across a reload', async () => {
    server.signedOut()
    const { user } = await open('/sign-in#provider_error=account_blocked&provider=github')
    await saysBlocked()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByRole('heading', { name: 'Sign in' })

    await open('/sign-in#provider_error=account_blocked')
    await saysBlocked()
    await open(current())
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(document.getElementById('account-blocked')).toBeNull()
  })

  it('says in Settings that nothing was linked when the person turns out to be signed in', async () => {
    // Another tab signed in to another account while the browser was away at the provider.
    server.signedIn(eve).settings({ on: ['google'] })
    await open('/sign-in#provider_error=account_blocked&provider=google')
    const said = await screen.findByText(
      'Nothing was linked: that account is blocked. To ask about it, write to support@permaily.com.',
    )
    expect(current()).toBe('/settings')
    expect(said.getAttribute('role')).toBe('alert')
    await expectAccessible()
  })
})
