import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, open, settle } from '../helpers/app.tsx'
import { expectAccessible } from '../helpers/axe.ts'
import { ana, later, meOf, network, ok, refused, server } from '../helpers/server.ts'

const closing = { ...ana, state: 'closing' as const, closes_at: '2026-11-18T14:32:00Z' }

/** Signs in by mail from the sign-in screen that is showing. */
async function signIn(user: Awaited<ReturnType<typeof open>>['user']) {
  server
    .on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
    .on('POST /api/v1/auth/email/finish', ok(meOf()))
  await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
  await user.click(screen.getByRole('button', { name: 'Continue' }))
  await user.type(await screen.findByLabelText('Code from the message'), '123456')
  await user.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('where focus starts', () => {
  it('is the heading of the page after a sign-in', async () => {
    server.signedOut().settings()
    const { user } = await open('/settings')
    await signIn(user)
    const heading = await screen.findByRole('heading', { level: 1, name: 'Settings' })
    await waitFor(() => expect(document.activeElement).toBe(heading))
    expect(heading.getAttribute('tabindex')).toBe('-1')
  })

  it('is the heading of the page after a deletion is cancelled', async () => {
    server.signedIn(closing).settings()
    server.on('POST /api/v1/account/delete/cancel', ok(ana))
    const { user } = await open('/settings')
    await user.click(await screen.findByRole('button', { name: 'Cancel the deletion' }))
    const heading = await screen.findByRole('heading', { level: 1, name: 'Settings' })
    await waitFor(() => expect(document.activeElement).toBe(heading))
    expect(screen.getByText('Deletion cancelled. Your account works again.')).toBeTruthy()
  })

  it('moves to the heading of the next page when the page changes', async () => {
    server.signedIn().settings()
    const { user } = await open('/accounts')
    await screen.findByRole('navigation', { name: 'Main' })
    await user.click(screen.getByRole('link', { name: 'Settings' }))
    const heading = await screen.findByRole('heading', { level: 1, name: 'Settings' })
    await waitFor(() => expect(document.activeElement).toBe(heading))
  })

  it('stays where the browser put it when the app opens straight on a page', async () => {
    server.signedIn().settings()
    await open('/settings')
    await screen.findByRole('heading', { level: 1, name: 'Settings' })
    await screen.findByText('1 device signed in')
    await settle()
    expect(document.activeElement).toBe(document.body)
  })

  it('is not taken from something on the page that asked for it', async () => {
    server.signedIn().settings({ on: ['google'] })
    // Linking came back with nothing linked: the section says so, and that takes focus.
    await open('/settings#provider_error=provider_in_use')
    const said = await screen.findByText(/^Nothing was linked/)
    await waitFor(() => expect(document.activeElement).toBe(said))
    await expectAccessible()
  })
})

describe('someone signed in who comes back from a provider to the sign-in screen', () => {
  it('lands in Settings, which says that nothing was linked', async () => {
    server.signedIn().settings({ on: ['google'] })
    await open('/sign-in#provider_error=provider_failed')
    const said = await screen.findByText("Nothing was linked: that didn't work. Try again.")
    expect(current()).toBe('/settings')
    await waitFor(() => expect(document.activeElement).toBe(said))
    expect(said.getAttribute('role')).toBe('alert')
    await expectAccessible()
  })

  it('is said once: a reload says nothing', async () => {
    server.signedIn().settings({ on: ['google'] })
    await open('/sign-in#provider_error=provider_failed')
    await screen.findByText(/^Nothing was linked/)
    await open(current())
    await screen.findByText('1 device signed in')
    await settle()
    expect(screen.queryByText(/^Nothing was linked/)).toBeNull()
  })

  it('does not follow someone who was told on the sign-in screen and then signed in', async () => {
    server.signedOut().settings()
    const { user } = await open('/sign-in#provider_error=provider_failed')
    await screen.findByRole('heading', { name: "That sign-in didn't work" })
    // A passkey, a link or another tab signs in without the form: here, another tab did.
    server.signedIn()
    window.dispatchEvent(new Event('focus'))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/')
    await user.click(screen.getByRole('link', { name: 'Settings' }))
    await screen.findByText('1 device signed in')
    await settle()
    expect(screen.queryByText(/^Nothing was linked/)).toBeNull()
  })

  it('does not follow them either when a link pasted over the sign-in screen finds them signed in', async () => {
    server.signedOut().settings()
    const { user } = await open('/sign-in#provider_error=provider_failed')
    await screen.findByRole('heading', { name: "That sign-in didn't work" })
    // Signed in from another tab meanwhile, to the account the link is for: the app just opens.
    server.signedIn().on('POST /api/v1/auth/email/look', ok({ address: 'ana@example.org' }))
    act(() => {
      window.history.pushState(null, '', '/sign-in#attempt=att_1&token=tok_1')
      window.dispatchEvent(new Event('hashchange'))
    })
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/')
    await user.click(screen.getByRole('link', { name: 'Settings' }))
    await screen.findByText('1 device signed in')
    await settle()
    expect(screen.queryByText(/^Nothing was linked/)).toBeNull()
  })

  it('lands in Settings too when the answer arrives over a screen they are signed in on', async () => {
    // Signed in to another account, looking at the question a link asks: the sign-in screens are open.
    const eve = { ...ana, id: 'acc_eve', address: 'eve@example.org' }
    server
      .signedIn(eve)
      .settings({ on: ['google'] })
      .on('POST /api/v1/auth/email/look', ok({ address: 'ana@example.org' }))
    const { user } = await open('/sign-in#attempt=att_1&token=tok_1')
    await screen.findByRole('heading', { name: 'Switch to this account?' })
    act(() => {
      window.history.pushState(null, '', '/sign-in#provider_error=provider_in_use&provider=google')
      window.dispatchEvent(new Event('hashchange'))
    })
    expect(current()).toBe('/sign-in')
    // The link still waits for its answer, and is asked about again.
    await user.click(await screen.findByRole('button', { name: "Don't switch" }))
    await screen.findByText(
      'Nothing was linked: that Google account belongs with another Permaily account.',
    )
    expect(current()).toBe('/settings')
  })

  it('still sees only the app when the address asked for an address to confirm', async () => {
    server.signedIn()
    await open('/sign-in#provider_confirm')
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/')
  })
})

describe('the gate', () => {
  it('leads every route to the sign-in screen without a session, and back after', async () => {
    server.signedOut().settings()
    const { user } = await open('/settings?state=x')
    await screen.findByRole('heading', { name: 'Sign in' })
    // Where the person was going rides in the history's state, never in the address.
    expect(current()).toBe('/sign-in')
    expect(window.history.state.usr).toEqual({ from: '/settings?state=x' })
    await expectAccessible()
    await signIn(user)
    await screen.findByRole('heading', { level: 1, name: 'Settings' })
    expect(current()).toBe('/settings?state=x')
  })

  it('opens the app for someone signed in who opens the sign-in screen', async () => {
    server.signedIn()
    await open('/sign-up')
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/')
  })

  it('sends a route that does not exist home', async () => {
    server.signedIn()
    await open('/nowhere')
    await screen.findByRole('navigation', { name: 'Main' })
    expect(current()).toBe('/')
  })

  it('says so when the server gives no answer, and asks again', async () => {
    server.on('GET /api/v1/me', network)
    const { user } = await open('/settings')
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain("Permaily can't be reached")
    await waitFor(() => expect(document.title).toBe("Can't reach Permaily · Permaily"))
    // No answer signs nobody out: the address stays where the person was going.
    expect(current()).toBe('/settings')
    await expectAccessible()
    server.signedIn().settings()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: 'Settings' })
  })

  it('says it is loading while the server is asked', async () => {
    const me = later()
    server.on('GET /api/v1/me', me.answer)
    await open('/settings')
    expect(screen.getByRole('status').textContent).toBe('Loading')
    await expectAccessible()
    me.give(refused(401, 'unauthorized'))
    server.on('GET /api/v1/auth/providers', ok({ providers: [] }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('shows only that the account is closing, on every route, and keeps the address', async () => {
    server.signedIn(closing)
    await open('/connections')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    expect(current()).toBe('/connections')
    await settle()
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
    await expectAccessible()
  })

  it('does not take someone back who signed out on purpose', async () => {
    server.signedIn().settings()
    server.on('POST /api/v1/auth/sign-out', ok())
    const { user } = await open('/settings')
    await user.click(await screen.findByRole('button', { name: /Account menu/ }))
    server.signedOut()
    await user.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toBeNull()
  })
})
