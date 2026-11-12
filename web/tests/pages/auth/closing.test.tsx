import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { blockStorage, current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, network, ok, refused, server } from '../../helpers/server.ts'

const closesAt = '2026-11-18T14:32:00Z'
const closing = { ...ana, state: 'closing' as const, closes_at: closesAt }
const kept = { 'permaily-closes-at': '2026-11-18T14:32:00.000Z' }
const note =
  'An account used in this browser was deleted on Wednesday, 18 November 2026, unless that was cancelled from another device.'

/** The clock of the browser, at a moment a test chooses. Only the date is faked: timers run as they do. */
function at(when: string) {
  vi.useFakeTimers({ toFake: ['Date'], now: Date.parse(when) })
}

describe('an account that is closing', () => {
  it('shows that it is being deleted, the day with its zone, and the two things left to do', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing)
    await open('/rules')
    const title = await screen.findByRole('heading', { name: 'Your account is being deleted' })
    await waitFor(() => expect(document.activeElement).toBe(title))
    await waitFor(() => expect(document.title).toBe('Your account is being deleted · Permaily'))
    expect(screen.getByText('Wednesday, 18 November 2026').getAttribute('datetime')).toBe(
      '2026-11-18T14:32:00.000Z',
    )
    expect(screen.getByText('14:32 UTC, in 7 days')).toBeTruthy()
    expect(screen.getByText(/has stopped working/).textContent).toBe(
      'The account of ana@example.org has stopped working.',
    )
    expect(screen.getAllByRole('button').map((button) => button.textContent)).toEqual([
      'Cancel the deletion',
      'Sign out',
    ])
    await expectAccessible()
  })

  it('makes no day up when the server gave none', async () => {
    server.signedIn({ ...ana, state: 'closing' })
    await open('/')
    await screen.findByText('a day within the next 7 days')
    expect({ ...localStorage }).toEqual({})
  })

  it('goes back to the page the person was on when the deletion is cancelled', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing).settings().on('POST /api/v1/account/delete/cancel', ok(ana))
    const { user } = await open('/settings')
    await user.click(await screen.findByRole('button', { name: 'Cancel the deletion' }))
    await screen.findByRole('heading', { level: 1, name: 'Settings' })
    expect(current()).toBe('/settings')
    expect(
      screen.getByText('Deletion cancelled. Your account works again.').getAttribute('role'),
    ).toBe('status')
  })

  it('says where it was asked when cancelling or signing out got no answer', async () => {
    at('2026-11-11T15:00:00Z')
    server
      .signedIn(closing)
      .on('POST /api/v1/account/delete/cancel', network)
      .on('POST /api/v1/auth/sign-out', network)
    const { user } = await open('/')
    const keep = await screen.findByRole('button', { name: 'Cancel the deletion' })
    await user.click(keep)
    await screen.findByText(
      "The deletion wasn't cancelled: Permaily can't be reached. Check your connection, then try again.",
    )
    await waitFor(() => expect(document.activeElement).toBe(keep))
    await expectAccessible()

    server.on('POST /api/v1/account/delete/cancel', refused(429, 'too_many_requests'))
    await user.click(keep)
    await screen.findByText(
      "The deletion wasn't cancelled. Too many attempts. Wait a few minutes, then try again.",
    )

    const leave = screen.getByRole('button', { name: 'Sign out' })
    await user.click(leave)
    await screen.findByText(
      "Couldn't sign out: Permaily can't be reached. Check your connection, then try again.",
    )
    await waitFor(() => expect(document.activeElement).toBe(leave))
    server.on('POST /api/v1/auth/sign-out', refused(500, 'internal'))
    await user.click(leave)
    await screen.findByText("Couldn't sign out. Try again in a moment.")
  })

  it('leads to the sign-in screen when the day came while cancelling', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing).on('POST /api/v1/account/delete/cancel', () => {
      server.signedOut()
      return refused(401, 'unauthorized')
    })
    const { user } = await open('/')
    await user.click(await screen.findByRole('button', { name: 'Cancel the deletion' }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('signs out on request', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing).on('POST /api/v1/auth/sign-out', ok())
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('button', { name: 'Sign out' }))
    server.signedOut()
    await screen.findByRole('heading', { name: 'Sign in' })
    // Left on purpose: nothing takes the person back.
    expect(window.history.state.usr).toBeNull()
  })
})

describe('after the day of a deletion', () => {
  it('keeps only the day while the account is seen closing: no address, id or name', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing)
    await open('/')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    await waitFor(() => expect({ ...localStorage }).toEqual(kept))
    expect({ ...sessionStorage }).toEqual({})
    expectNotKept('ana@example.org', 'acc_ana', 'Ana')
  })

  it('says once on the sign-in screen why the person is there, and forgets', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing)
    await open('/rules')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    await waitFor(() => expect({ ...localStorage }).toEqual(kept))

    // A later visit: the day has passed, and the server answers as for any session that ended.
    vi.setSystemTime(Date.parse('2026-11-20T09:00:00Z'))
    server.signedOut()
    await open('/rules')
    await screen.findByRole('heading', { name: 'Sign in' })
    const said = await screen.findByText(
      (_, element) => element?.id === 'deleted-account' && element.textContent === note,
    )
    expect(said.querySelector('time')?.getAttribute('datetime')).toBe('2026-11-18T14:32:00.000Z')
    // It names nobody: whoever reads it may not be whose account it was.
    expect(said.textContent).not.toContain('ana')
    // The address field reads it when it takes focus.
    expect(screen.getByLabelText('Email address').getAttribute('aria-describedby')).toBe(
      'deleted-account',
    )
    expect({ ...localStorage }).toEqual({})
    await expectAccessible()

    // Once: a reload says nothing.
    await open(current())
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
  })

  it('says it when a closing screen left open reaches the day', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    vi.setSystemTime(Date.parse('2026-11-18T14:31:30Z'))
    server.signedIn(closing)
    await open('/rules')
    await screen.findByText('14:32 UTC, today')
    // Only the server's answer signs anyone out: this clock alone does not.
    const asked = server.asked('GET /api/v1/me').length
    server.signedOut()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(server.asked('GET /api/v1/me').length).toBeGreaterThan(asked)
    await screen.findByRole('heading', { name: 'Sign in' })
    await waitFor(() => expect(document.getElementById('deleted-account')?.textContent).toBe(note))
    expect({ ...localStorage }).toEqual({})
  })

  it('says nothing before the day, and keeps the day for later', async () => {
    at('2026-11-12T09:00:00Z')
    localStorage.setItem('permaily-closes-at', kept['permaily-closes-at'])
    server.signedOut()
    await open('/')
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
    expect({ ...localStorage }).toEqual(kept)
  })

  it('forgets when the deletion is cancelled', async () => {
    at('2026-11-11T15:00:00Z')
    server.signedIn(closing).on('POST /api/v1/account/delete/cancel', ok(ana))
    const { user } = await open('/')
    await waitFor(() => expect({ ...localStorage }).toEqual(kept))
    await user.click(await screen.findByRole('button', { name: 'Cancel the deletion' }))
    await screen.findByRole('navigation', { name: 'Main' })
    await waitFor(() => expect({ ...localStorage }).toEqual({}))
  })

  it('forgets when an account that works is seen, and says nothing after', async () => {
    at('2026-11-20T09:00:00Z')
    localStorage.setItem('permaily-closes-at', kept['permaily-closes-at'])
    server.signedIn()
    await open('/')
    await screen.findByRole('navigation', { name: 'Main' })
    await waitFor(() => expect({ ...localStorage }).toEqual({}))
    server.signedOut()
    await open('/')
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
  })

  it('is not said on the sign-up screen, and gives way to nothing else that is said', async () => {
    at('2026-11-20T09:00:00Z')
    localStorage.setItem('permaily-closes-at', kept['permaily-closes-at'])
    server.signedOut()
    const { user } = await open('/sign-in#provider_error=provider_failed')
    await screen.findByRole('heading', { name: "That sign-in didn't work" })
    await waitFor(() => expect(document.getElementById('deleted-account')?.textContent).toBe(note))
    expect(screen.getByLabelText('Email address').getAttribute('aria-describedby')).toBe(
      'provider-problem deleted-account',
    )
    await expectAccessible()
    await user.click(screen.getByRole('link', { name: 'Create an account' }))
    await screen.findByRole('heading', { name: 'Create your account' })
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
  })

  it('shows only a date this app wrote: anything else in storage says nothing', async () => {
    at('2026-11-20T09:00:00Z')
    localStorage.setItem('permaily-closes-at', '<img src=x onerror=alert(1)> ana@example.org')
    server.signedOut()
    const { container } = await open('/')
    await screen.findByRole('heading', { name: 'Sign in' })
    await waitFor(() => expect({ ...localStorage }).toEqual({}))
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
    expect(container.innerHTML).not.toContain('onerror')
  })

  it('works with storage unavailable: no note, and nothing fails', async () => {
    at('2026-11-11T15:00:00Z')
    blockStorage('localStorage')
    server.signedIn(closing).on('POST /api/v1/auth/sign-out', ok())
    const { user } = await open('/')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    vi.setSystemTime(Date.parse('2026-11-20T09:00:00Z'))
    await user.click(screen.getByRole('button', { name: 'Sign out' }))
    server.signedOut()
    await screen.findByRole('heading', { name: 'Sign in' })
    await settle()
    expect(document.getElementById('deleted-account')).toBeNull()
  })
})
