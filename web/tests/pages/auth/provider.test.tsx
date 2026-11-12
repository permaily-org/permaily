import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import {
  blockStorage,
  current,
  expectNotKept,
  open,
  settle,
  watchLeaving,
} from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { later, network, ok, refused, server } from '../../helpers/server.ts'

const all = ok({ providers: ['microsoft', 'meta', 'github', 'google'] })
const away = 'https://accounts.google.com/o/oauth2/v2/auth?state=st_secret_1'

/** Nobody is signed in, and these providers are on. */
function guest(on: unknown = all) {
  server.signedOut().on('GET /api/v1/auth/providers', on as never)
}

describe('the providers on the sign-in screens', () => {
  it('are the ones that are on, in one fixed order, each named in words', async () => {
    guest()
    await open('/sign-in')
    const first = await screen.findByRole('button', { name: 'Continue with Google' })
    const offered = screen.getAllByRole('button', { name: /^Continue with / })
    expect(offered.map((button) => button.textContent)).toEqual([
      'Continue with Google',
      'Continue with GitHub',
      // The identifier is meta; to a person it is Facebook.
      'Continue with Facebook',
      'Continue with Microsoft',
    ])
    await settle()
    expect(screen.queryByText(/Meta/)).toBeNull()
    // No mark of a provider is drawn or loaded.
    expect(first.querySelector('img, svg')).toBeNull()
    expect(document.querySelector('img')).toBeNull()
    await expectAccessible()
  })

  it.each([
    ['none is on', ok({ providers: [] })],
    ['the server gives no answer', network],
    ['the answer is not a list', ok({ providers: 'google' })],
  ])('show nothing when %s', async (_, answer) => {
    guest(answer)
    await open('/sign-in')
    await screen.findByRole('heading', { name: 'Sign in' })
    await waitFor(() => expect(server.asked('GET /api/v1/auth/providers')).toHaveLength(1))
    await settle()
    expect(screen.queryByRole('button', { name: /^Continue with / })).toBeNull()
  })

  it('leave the app only for the address the server answered, with where the person was going', async () => {
    guest()
    const assign = watchLeaving()
    const starting = later()
    server.on('POST /api/v1/auth/provider/start', starting.answer)
    const { user } = await open('/rules?state=empty')
    await user.click(await screen.findByRole('button', { name: 'Continue with Facebook' }))
    // While the browser leaves, every action of the group waits.
    expect(screen.getByText('Taking you to Facebook').getAttribute('role')).toBe('status')
    for (const button of screen.getAllByRole('button', { name: /^Continue with / })) {
      expect((button as HTMLButtonElement).disabled).toBe(true)
    }
    starting.give(ok({ url: away, expires_in: 600 }))
    await waitFor(() => expect(assign).toHaveBeenCalledExactlyOnceWith(away))
    expect(server.asked('POST /api/v1/auth/provider/start')[0].body).toEqual({
      provider: 'meta',
      return_to: '/rules?state=empty',
    })
    // Nothing is kept for the trip when there is no invitation.
    expect({ ...sessionStorage }).toEqual({})
    expect({ ...localStorage }).toEqual({})
    expectNotKept('st_secret_1')

    // Back with the browser's Back button: the page is shown as it was left, and its actions work again.
    act(() => {
      window.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }))
    })
    expect(
      (screen.getByRole('button', { name: 'Continue with Google' }) as HTMLButtonElement).disabled,
    ).toBe(false)
  })

  it.each([
    'javascript:alert(document.cookie)',
    'http://accounts.google.com/',
    '/sign-in#provider_confirm',
    'data:text/html,hello',
  ])('go nowhere when the server answers an address like %s', async (url) => {
    guest()
    const assign = watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url, expires_in: 600 }))
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await screen.findByText('Something went wrong on our side. Try again in a moment.')
    expect(assign).not.toHaveBeenCalled()
    expect({ ...sessionStorage }).toEqual({})
  })

  it('never go to an address read from the address bar', async () => {
    guest()
    const assign = watchLeaving()
    await open('/sign-in?next=https://evil.example/#url=https://evil.example/&provider_error=x')
    await screen.findByRole('button', { name: 'Continue with Google' })
    expect(assign).not.toHaveBeenCalled()
    expect(server.asked('POST /api/v1/auth/provider/start')).toHaveLength(0)
  })

  it('stop offering one the server turned off meanwhile', async () => {
    guest()
    server.on('POST /api/v1/auth/provider/start', () => {
      server.on('GET /api/v1/auth/providers', ok({ providers: ['google'] }))
      return refused(409, 'provider_off')
    })
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Continue with Facebook' }))
    const said = await screen.findByText(
      "Signing in with Facebook isn't available right now. Sign in another way.",
    )
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Continue with Facebook' })).toBeNull(),
    )
    // Its action is gone, and focus with it: the message takes it.
    await waitFor(() => expect(document.activeElement).toBe(said))
    await expectAccessible()
  })

  it('say when starting failed', async () => {
    guest()
    server.on('POST /api/v1/auth/provider/start', network)
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Continue with GitHub' }))
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
  })
})

describe('starting with a provider from the sign-up screen', () => {
  it('sends the invitation typed, and starts without one all the same', async () => {
    guest()
    watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up')
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await waitFor(() => expect(server.asked('POST /api/v1/auth/provider/start')).toHaveLength(1))
    expect(server.asked('POST /api/v1/auth/provider/start')[0].body).toEqual({ provider: 'google' })
    expect({ ...sessionStorage }).toEqual({})
  })

  it('says in its field what cannot be an invitation, or one the server refuses, and starts nothing', async () => {
    guest()
    const assign = watchLeaving()
    const { user } = await open('/sign-up')
    await user.type(await screen.findByLabelText('Invitation'), 'abcd')
    await user.click(screen.getByRole('button', { name: 'Continue with Google' }))
    await screen.findByText(
      'An invitation has 12 letters and digits. Check it against the one you got.',
    )
    expect(server.asked('POST /api/v1/auth/provider/start')).toHaveLength(0)

    server.on('POST /api/v1/auth/provider/start', refused(400, 'invalid_invitation'))
    await user.type(screen.getByLabelText('Invitation'), '2345efgh')
    await user.click(screen.getByRole('button', { name: 'Continue with Google' }))
    await screen.findByText(
      "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
    )
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Invitation')))
    expect(assign).not.toHaveBeenCalled()
    // The trip did not happen: nothing waits for it.
    expect({ ...sessionStorage }).toEqual({})
  })
})

describe('an invitation across the trip to a provider', () => {
  it('is kept only while the browser is away, and fills the sign-up screen in again', async () => {
    guest()
    watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up?invitation=ABCD-2345-EFGH')
    await screen.findByRole('button', { name: 'Continue with Google' })
    // Not before the browser is about to leave.
    expect({ ...sessionStorage }).toEqual({})
    await user.click(screen.getByRole('button', { name: 'Continue with Google' }))
    await waitFor(() =>
      expect({ ...sessionStorage }).toEqual({ 'permaily-invitation': 'abcd2345efgh' }),
    )
    expect({ ...localStorage }).toEqual({})
    expect(server.asked('POST /api/v1/auth/provider/start')[0].body).toEqual({
      provider: 'google',
      invitation: 'abcd2345efgh',
    })

    // The provider's account has no Permaily account yet, and the invitation was used meanwhile or lost:
    // the server sends the browser back, the page loads again, and the invitation is there.
    await open('/sign-in#provider_error=invitation_needed&provider=google')
    await screen.findByRole('heading', { name: 'An invitation is needed' })
    expect(current()).toBe('/sign-up')
    const invitation = screen.getByLabelText('Invitation') as HTMLInputElement
    expect(invitation.value).toBe('ABCD-2345-EFGH')
    await waitFor(() => expect(document.activeElement).toBe(invitation))
    expect({ ...sessionStorage }).toEqual({})
    expectNotKept('abcd2345efgh', 'ABCD-2345-EFGH')
    await expectAccessible()
  })

  it('crosses from the sign-in screen too, when one arrived by link', async () => {
    guest()
    watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up?invitation=ABCD-2345-EFGH')
    await user.click(await screen.findByRole('link', { name: 'Sign in' }))
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await waitFor(() =>
      expect({ ...sessionStorage }).toEqual({ 'permaily-invitation': 'abcd2345efgh' }),
    )
    // The sign-in screen starts without it: only the sign-up screen makes accounts.
    expect(server.asked('POST /api/v1/auth/provider/start')[0].body).toEqual({ provider: 'google' })
  })

  it('leaves storage as soon as the app loads again, whatever the trip came to', async () => {
    guest()
    watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up?invitation=ABCD-2345-EFGH')
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await waitFor(() => expect(sessionStorage.length).toBe(1))
    // It signed the person in: the app opens, and nothing is left behind.
    server.signedIn()
    await open('/')
    await screen.findByRole('navigation', { name: 'Main' })
    expect({ ...sessionStorage }).toEqual({})
    expectNotKept('abcd2345efgh')
  })

  it('is forgotten when the person comes back with the Back button', async () => {
    guest()
    watchLeaving()
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up?invitation=ABCD-2345-EFGH')
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await waitFor(() => expect(sessionStorage.length).toBe(1))
    act(() => {
      window.dispatchEvent(Object.assign(new Event('pageshow'), { persisted: true }))
    })
    expect({ ...sessionStorage }).toEqual({})
    // The page is as it was left, invitation and all.
    expect((screen.getByLabelText('Invitation') as HTMLInputElement).value).toBe('ABCD-2345-EFGH')
  })

  it('is typed again where the storage of the tab is unavailable, and the trip still starts', async () => {
    guest()
    const assign = watchLeaving()
    blockStorage('sessionStorage')
    server.on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const { user } = await open('/sign-up?invitation=ABCD-2345-EFGH')
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith(away))
  })
})

describe('coming back from a provider with nobody signed in', () => {
  it.each([
    [
      'provider_failed',
      "That sign-in didn't work",
      'Nobody was signed in. Try again, or sign in another way.',
    ],
    [
      'provider_unavailable',
      "That sign-in isn't working right now",
      'Nobody was signed in. Try again later, or sign in another way.',
    ],
    [
      'provider_in_use',
      "That sign-in didn't work",
      'That account belongs with another Permaily account. Nobody was signed in.',
    ],
    [
      'provider_linked',
      "That sign-in didn't work",
      'Your Permaily account has another account of that provider linked already. Sign in with that one, or sign in another way and unlink it in Settings.',
    ],
    [
      'account_closing',
      'That account is being deleted',
      'Nothing was linked to it, and nobody was signed in. Sign in another way to cancel the deletion.',
    ],
  ])('says %s in fixed words when the server named no provider', async (reason, title, text) => {
    guest()
    await open(`/sign-in#provider_error=${reason}`)
    await screen.findByRole('heading', { name: title })
    expect(current()).toBe('/sign-in')
    expect(screen.getByText(text)).toBeTruthy()
    await waitFor(() => expect(document.title).toBe(`${title} · Permaily`))
    // The address field takes focus, and reads the reason with it.
    const address = screen.getByLabelText('Email address')
    await waitFor(() => expect(document.activeElement).toBe(address))
    expect(address.getAttribute('aria-describedby')).toBe('provider-problem')
    await expectAccessible()
  })

  it.each([
    [
      'provider_failed',
      'meta',
      "Signing in with Facebook didn't work",
      'Nobody was signed in. Try again, or sign in another way.',
    ],
    [
      'provider_unavailable',
      'github',
      "Signing in with GitHub isn't working right now",
      'Nobody was signed in. Try again later, or sign in another way.',
    ],
    [
      'provider_in_use',
      'google',
      "Signing in with Google didn't work",
      'That Google account belongs with another Permaily account. Nobody was signed in.',
    ],
    [
      'provider_linked',
      'microsoft',
      "Signing in with Microsoft didn't work",
      'Your Permaily account has another Microsoft account linked already. Sign in with that one, or sign in another way and unlink it in Settings.',
    ],
    [
      'account_closing',
      'google',
      'That account is being deleted',
      'Nothing was linked to it, and nobody was signed in. Sign in another way to cancel the deletion.',
    ],
  ])(
    'names the provider for %s when the server said it was %s',
    async (reason, provider, title, text) => {
      guest()
      await open(`/sign-in#provider_error=${reason}&provider=${provider}`)
      await screen.findByRole('heading', { name: title })
      expect(current()).toBe('/sign-in')
      expect(screen.getByText(text)).toBeTruthy()
    },
  )

  it.each([
    'provider_failed&provider=apple',
    'provider_failed&provider=%3Cb%3EGoogle%3C%2Fb%3E',
    'something_new',
    'something_new&provider=Ana%20at%20ana%40example.org',
    '%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E',
    'Your%20account%20was%20deleted.%20Call%20555-0100',
  ])(
    'turns anything else in the address (%s) into the fixed words that say least',
    async (fragment) => {
      guest()
      const { container } = await open(`/sign-in#provider_error=${fragment}`)
      await screen.findByRole('heading', { name: "That sign-in didn't work" })
      expect(current()).toBe('/sign-in')
      // Nothing of the address is on the page.
      const page = container.innerHTML
      for (const written of [
        'apple',
        '<b>',
        'something_new',
        'ana@example.org',
        'onerror',
        '555-0100',
      ]) {
        expect(page).not.toContain(written)
      }
    },
  )

  it('says what is about an invitation on the sign-up screen, where one is typed', async () => {
    guest()
    await open('/sign-in#provider_error=invitation_needed')
    await screen.findByRole('heading', { name: 'An invitation is needed' })
    expect(current()).toBe('/sign-up')
    expect(
      screen.getByText(
        'There is no Permaily account for your address yet, and making one takes an invitation. Enter yours, then continue the same way.',
      ),
    ).toBeTruthy()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Invitation')))
    // The providers are still offered.
    await screen.findByRole('button', { name: 'Continue with Google' })
    await expectAccessible()

    await open('/sign-in#provider_error=invalid_invitation')
    await screen.findByRole('heading', { name: 'Create your account' })
    expect(current()).toBe('/sign-up')
    // An invitation the server refused is said in its field.
    const said = screen.getByText(
      "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
    )
    expect(said.id).toBe('invitation-error')
    await expectAccessible()
  })

  it('keeps the words until the person tries something else, and not across a reload', async () => {
    guest()
    const { user } = await open('/sign-in#provider_error=provider_in_use&provider=google')
    await screen.findByRole('heading', { name: "Signing in with Google didn't work" })
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByRole('heading', { name: 'Sign in' })

    await open('/sign-in#provider_error=provider_in_use&provider=google')
    await screen.findByRole('heading', { name: "Signing in with Google didn't work" })
    await open(current())
    await screen.findByRole('heading', { name: 'Sign in' })
    expect({ ...localStorage }).toEqual({})
    expect({ ...sessionStorage }).toEqual({})
  })

  it('says the newest one when another arrives over the open screen', async () => {
    guest()
    await open('/sign-in#provider_error=provider_failed')
    await screen.findByRole('heading', { name: "That sign-in didn't work" })
    act(() => {
      window.history.pushState(null, '', '/sign-in#provider_error=account_closing')
      window.dispatchEvent(new Event('hashchange'))
    })
    await screen.findByRole('heading', { name: 'That account is being deleted' })
    expect(current()).toBe('/sign-in')
  })
})
