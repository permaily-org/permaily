import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { later, meOf, network, ok, refused, server } from '../../helpers/server.ts'

const waiting = ok({
  provider: 'meta',
  address: 'ana@facebook.example',
  return_to: '/accounts',
  expires_in: 900,
})
const sent = ok({ attempt: 'att_confirm_1', expires_in: 600 })

/** Nobody is signed in, and a sign-in with a provider waits for its address. */
function pending(answer = waiting) {
  server.signedOut().on('GET /api/v1/auth/provider/pending', answer)
}

describe('confirming an address for a provider', () => {
  it('asks the server what waits, and says the provider did not vouch for an address', async () => {
    const asking = later()
    server.signedOut().on('GET /api/v1/auth/provider/pending', asking.answer)
    await open('/sign-in#provider_confirm')
    expect(current()).toBe('/sign-in')
    expect(screen.getByRole('status').textContent).toBe('Loading')
    asking.give(waiting)

    const title = await screen.findByRole('heading', { name: 'Confirm your email address' })
    await waitFor(() => expect(document.activeElement).toBe(title))
    expect(
      screen.getByText('Facebook said who you are there, but not that an email address is yours.'),
    ).toBeTruthy()
    // The address the provider gave is only the default: it proves nothing.
    const address = screen.getByLabelText('Your email address') as HTMLInputElement
    expect(address.value).toBe('ana@facebook.example')
    expect(screen.getByText("Facebook gave this one. Change it if it isn't yours.")).toBeTruthy()
    // That a confirmation was asked for, and where the person was going, ride in the history's state.
    expect(window.history.state.usr).toEqual({ providerConfirm: true, from: '/accounts' })
    expectNotKept('ana@facebook.example')
    await expectAccessible()
  })

  it('sends the message, asks for its code, links and opens the app where the person was going', async () => {
    pending()
    // Where the person was going: the list of mailboxes, which asks for them.
    server.on('GET /api/v1/mailboxes', ok({ mailboxes: [], limit: 5 }))
    const { user } = await open('/sign-in#provider_confirm')
    const address = await screen.findByLabelText('Your email address')
    await user.clear(address)
    await user.type(address, 'ana@example.org')
    server.on('POST /api/v1/auth/provider/confirm/start', sent)
    await user.click(screen.getByRole('button', { name: 'Send the message' }))

    await screen.findByRole('heading', { name: 'Check your mail' })
    expect(screen.getByText(/can sign in, a message is on its way/).textContent).toBe(
      "If ana@example.org can sign in, a message is on its way. It works for 10 minutes. Its code links Facebook to that address's account, and signs you in.",
    )
    expect(
      screen.getByText("No code in the message? Its link signs you in, but doesn't link Facebook."),
    ).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Create an account' })).toBeTruthy()
    await expectAccessible()

    server.on('POST /api/v1/auth/provider/confirm/finish', ok(meOf())).settings()
    await user.type(screen.getByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Confirm and sign in' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/accounts')
    expect(server.asked('POST /api/v1/auth/provider/confirm/start')[0].body).toEqual({
      address: 'ana@example.org',
    })
    expect(server.asked('POST /api/v1/auth/provider/confirm/finish')[0].body).toEqual({
      attempt: 'att_confirm_1',
      code: '123456',
    })
    expectNotKept('123456', 'att_confirm_1', 'ana@example.org', 'ana@facebook.example')
  })

  it('asks again after a reload: what waits is the server own', async () => {
    pending()
    await open('/sign-in#provider_confirm')
    await screen.findByRole('heading', { name: 'Confirm your email address' })
    await open(current())
    await screen.findByRole('heading', { name: 'Confirm your email address' })
    expect(server.asked('GET /api/v1/auth/provider/pending')).toHaveLength(2)
  })

  it('offers no default when the provider gave no address', async () => {
    pending(ok({ provider: 'github', address: '', return_to: '/', expires_in: 900 }))
    const { user } = await open('/sign-in#provider_confirm')
    const address = (await screen.findByLabelText('Your email address')) as HTMLInputElement
    expect(address.value).toBe('')
    await settle()
    expect(screen.queryByText(/gave this one/)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Send the message' }))
    await screen.findByText('Enter your email address.')
    await waitFor(() => expect(document.activeElement).toBe(address))
  })

  it('says when nothing waits any more, and offers to start again', async () => {
    pending(refused(409, 'provider_failed'))
    const { user } = await open('/sign-in#provider_confirm')
    const title = await screen.findByRole('heading', { name: 'That sign-in is no longer waiting' })
    await waitFor(() => expect(document.activeElement).toBe(title))
    expect(screen.getByText(/Nobody was signed in\./)).toBeTruthy()
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: 'Start again' }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toBeNull()
  })

  it('says so when its time passes while the screen is open', async () => {
    pending(ok({ provider: 'meta', address: '', return_to: '/', expires_in: 0 }))
    await open('/sign-in#provider_confirm')
    await screen.findByRole('heading', { name: 'That sign-in is no longer waiting' })
  })

  it.each([
    [
      network,
      "Permaily can't be reached",
      'Check your connection, then try again. Your sign-in is still waiting.',
    ],
    [refused(500, 'internal'), 'Something went wrong on our side', 'Try again in a moment.'],
    [
      refused(429, 'too_many_requests'),
      'Something went wrong on our side',
      'Too many attempts. Wait a few minutes, then try again.',
    ],
  ])(
    'says when the server gives no answer about what waits, and tries again',
    async (answer, title, text) => {
      pending(answer)
      const { user } = await open('/sign-in#provider_confirm')
      const heading = await screen.findByRole('heading', { name: title })
      expect(screen.getByText(text)).toBeTruthy()
      await waitFor(() => expect(document.activeElement).toBe(heading))
      await expectAccessible()
      // A second try that fails puts focus back on the action.
      const again = screen.getByRole('button', { name: 'Try again' })
      await user.click(again)
      await waitFor(() => expect(server.asked('GET /api/v1/auth/provider/pending')).toHaveLength(2))
      await waitFor(() =>
        expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Try again' })),
      )
      server.on('GET /api/v1/auth/provider/pending', waiting)
      await user.click(screen.getByRole('button', { name: 'Try again' }))
      await screen.findByRole('heading', { name: 'Confirm your email address' })
    },
  )

  it('says what is wrong with the address, or why no message was sent', async () => {
    pending()
    const { user } = await open('/sign-in#provider_confirm')
    await screen.findByLabelText('Your email address')
    server.on('POST /api/v1/auth/provider/confirm/start', refused(400, 'invalid_address'))
    await user.click(screen.getByRole('button', { name: 'Send the message' }))
    await screen.findByText("Permaily can't use that address. Check it for typos.")
    server.on('POST /api/v1/auth/provider/confirm/start', network)
    await user.click(screen.getByRole('button', { name: 'Send the message' }))
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    await expectAccessible()
  })

  it('goes back from the code to change the address, with what was typed', async () => {
    pending()
    server.on('POST /api/v1/auth/provider/confirm/start', sent)
    const { user } = await open('/sign-in#provider_confirm')
    const address = await screen.findByLabelText('Your email address')
    await user.clear(address)
    await user.type(address, 'ana@example.org')
    await user.click(screen.getByRole('button', { name: 'Send the message' }))
    await screen.findByLabelText('Code from the message')
    await user.click(screen.getByRole('button', { name: 'Send another message' }))
    await screen.findByText(/Asked for another message/)
    expect(server.asked('POST /api/v1/auth/provider/confirm/start')).toHaveLength(2)
    await user.click(screen.getByRole('button', { name: 'Use another address' }))
    const again = (await screen.findByLabelText('Your email address')) as HTMLInputElement
    expect(again.value).toBe('ana@example.org')
    await waitFor(() => expect(document.activeElement).toBe(again))
  })

  it.each([
    [
      'provider_linked',
      "Signing in with Facebook didn't work",
      'Your Permaily account has another Facebook account linked already. Sign in with that one, or sign in another way and unlink it in Settings.',
    ],
    [
      'provider_in_use',
      "Signing in with Facebook didn't work",
      'That Facebook account belongs with another Permaily account. Nobody was signed in.',
    ],
    [
      'account_closing',
      'That account is being deleted',
      'Nothing was linked to it, and nobody was signed in. Sign in another way to cancel the deletion.',
    ],
  ])(
    'leads to the sign-in screen with the reason when the server will not link: %s',
    async (code, title, text) => {
      pending()
      server
        .on('POST /api/v1/auth/provider/confirm/start', sent)
        .on('POST /api/v1/auth/provider/confirm/finish', refused(409, code))
      const { user } = await open('/sign-in#provider_confirm')
      await user.click(await screen.findByRole('button', { name: 'Send the message' }))
      await user.type(await screen.findByLabelText('Code from the message'), '123456')
      await user.click(screen.getByRole('button', { name: 'Confirm and sign in' }))
      await screen.findByRole('heading', { name: title })
      expect(screen.getByText(text)).toBeTruthy()
      expect(window.history.state.usr).toBeNull()
    },
  )

  it('leads to the sign-up screen when the invitation was refused', async () => {
    pending()
    server.on('POST /api/v1/auth/provider/confirm/start', refused(400, 'invalid_invitation'))
    const { user } = await open('/sign-in#provider_confirm')
    await user.click(await screen.findByRole('button', { name: 'Send the message' }))
    await screen.findByText(
      "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
    )
    expect(current()).toBe('/sign-up')
  })

  it('says so when what waited ended between the message and its code', async () => {
    pending()
    server
      .on('POST /api/v1/auth/provider/confirm/start', sent)
      .on('POST /api/v1/auth/provider/confirm/finish', refused(409, 'provider_failed'))
    const { user } = await open('/sign-in#provider_confirm')
    await user.click(await screen.findByRole('button', { name: 'Send the message' }))
    await user.type(await screen.findByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Confirm and sign in' }))
    await screen.findByRole('heading', { name: 'That sign-in is no longer waiting' })
  })

  it('is left for good with "Sign in another way"', async () => {
    pending()
    const { user } = await open('/sign-in#provider_confirm')
    await user.click(await screen.findByRole('button', { name: 'Sign in another way' }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toBeNull()
  })
})
