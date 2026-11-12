import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TERMS_VERSION } from '../../../src/lib/terms.ts'
import { agree, current, open, settle, termsBox, watchLeaving } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, later, meOf, network, ok, refused, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const started = ok({ attempt: 'att_secret_1', expires_in: 600 })
const away = 'https://accounts.google.com/o/oauth2/v2/auth?state=st_secret_1'
const unticked = 'Tick the box to agree to the terms of service and the privacy policy.'

/** Fills in the sign-up screen, without the box. */
async function fill(user: User) {
  await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
  await user.type(screen.getByLabelText('Invitation'), 'abcd2345efgh')
}

describe('agreeing to the terms at sign-up', () => {
  it('asks the person to tick the box, which starts empty, before any message is sent', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-up')
    await fill(user)
    const box = termsBox()
    expect(box.checked).toBe(false)
    const sentence = box.closest('label')
    expect(sentence?.textContent).toBe("I agree to Permaily's terms of service and privacy policy.")
    // Both documents, in a new tab, so nothing typed here is lost by reading them.
    const links = within(sentence as HTMLElement).getAllByRole('link')
    expect(
      links.map((link) => [
        link.textContent,
        link.getAttribute('href'),
        link.getAttribute('target'),
      ]),
    ).toEqual([
      ['terms of service', 'https://permaily.com/terms/', '_blank'],
      ['privacy policy', 'https://permaily.com/privacy/', '_blank'],
    ])
    await expectAccessible()

    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByText(unticked)
    await waitFor(() => expect(document.activeElement).toBe(termsBox()))
    expect(termsBox().getAttribute('aria-invalid')).toBe('true')
    expect(server.asked('POST /api/v1/auth/email/start')).toHaveLength(0)
    await expectAccessible()

    // Ticking it takes the words away, and the sign-up carries the version of the terms.
    await user.click(termsBox())
    expect(screen.queryByText(unticked)).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByLabelText('Code from the message')
    expect(server.asked('POST /api/v1/auth/email/start')[0].body).toEqual({
      address: 'ana@example.org',
      invitation: 'abcd2345efgh',
      terms: TERMS_VERSION,
    })
    // Coming back to the address finds the box as the person left it.
    await user.click(screen.getByRole('button', { name: 'Use another address' }))
    expect(termsBox().checked).toBe(true)
  })

  it('has no box on the sign-in screen, which sends no version', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-in')
    await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
    expect(screen.queryByRole('checkbox')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByLabelText('Code from the message')
    expect(server.asked('POST /api/v1/auth/email/start')[0].body).toEqual({
      address: 'ana@example.org',
    })
  })

  it('does not let the browser leave for a provider before the box is ticked', async () => {
    server
      .signedOut()
      .on('GET /api/v1/auth/providers', ok({ providers: ['google'] }))
      .on('POST /api/v1/auth/provider/start', ok({ url: away, expires_in: 600 }))
    const assign = watchLeaving()
    const { user } = await open('/sign-up')
    await fill(user)
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await screen.findByText(unticked)
    await waitFor(() => expect(document.activeElement).toBe(termsBox()))
    await settle()
    expect(server.asked('POST /api/v1/auth/provider/start')).toHaveLength(0)
    expect(assign).not.toHaveBeenCalled()

    await agree(user)
    await user.click(screen.getByRole('button', { name: 'Continue with Google' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith(away))
    expect(server.asked('POST /api/v1/auth/provider/start')[0].body).toEqual({
      provider: 'google',
      invitation: 'abcd2345efgh',
      terms: TERMS_VERSION,
    })
  })

  it('says by the box when the server refuses the agreement, at either step', async () => {
    const changed =
      "Permaily's terms changed since this page opened. Reload it to read them, then agree to go on."
    server.signedOut().on('POST /api/v1/auth/email/start', refused(400, 'terms_needed'))
    const { user } = await open('/sign-up')
    await fill(user)
    await agree(user)
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByText(changed)
    await waitFor(() => expect(document.activeElement).toBe(termsBox()))

    // The terms changed between the message and its code: back to the box, empty again.
    server
      .on('POST /api/v1/auth/email/start', started)
      .on('POST /api/v1/auth/email/finish', refused(400, 'terms_needed'))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await user.type(await screen.findByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    await screen.findByText(changed)
    expect(termsBox().checked).toBe(false)
    await waitFor(() => expect(document.activeElement).toBe(termsBox()))
  })

  it('says by the box when the server refuses the agreement before the browser leaves', async () => {
    server
      .signedOut()
      .on('GET /api/v1/auth/providers', ok({ providers: ['google'] }))
      .on('POST /api/v1/auth/provider/start', refused(400, 'terms_needed'))
    const assign = watchLeaving()
    const { user } = await open('/sign-up')
    await fill(user)
    await agree(user)
    await user.click(await screen.findByRole('button', { name: 'Continue with Google' }))
    await screen.findByText(
      "Permaily's terms changed since this page opened. Reload it to read them, then agree to go on.",
    )
    expect(assign).not.toHaveBeenCalled()
  })
})

/** Ana, who agreed to `agreed`, while the server says `TERMS_VERSION` is in force. */
function agreedTo(agreed?: string) {
  server.on(
    'GET /api/v1/me',
    ok(meOf({ ...ana, ...(agreed ? { terms: agreed } : {}) }, {}, TERMS_VERSION)),
  )
}

describe('when the terms changed', () => {
  it('shows one screen, and nothing else of the app, until the person agrees', async () => {
    agreedTo('2026-01-01')
    const { user } = await open('/rules')
    const heading = await screen.findByRole('heading', { level: 1, name: 'The terms changed' })
    await waitFor(() => expect(document.activeElement).toBe(heading))
    expect(document.title).toBe('The terms changed · Permaily')
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
    expect(
      screen.getByText(/^Permaily's terms of service and privacy policy changed/).textContent,
    ).toBe(
      "Permaily's terms of service and privacy policy changed since you agreed to them. Read them, and agree to keep using your account.",
    )
    expect(termsBox().checked).toBe(false)
    expect(screen.getByRole('button', { name: 'Delete my account' })).toBeTruthy()
    await expectAccessible()

    await user.click(screen.getByRole('button', { name: 'Agree and continue' }))
    await screen.findByText(unticked)
    await waitFor(() => expect(document.activeElement).toBe(termsBox()))
    expect(server.asked('POST /api/v1/account/terms')).toHaveLength(0)

    server.on('POST /api/v1/account/terms', ok({ ...ana, terms: TERMS_VERSION }))
    await user.click(termsBox())
    await user.click(screen.getByRole('button', { name: 'Agree and continue' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(server.asked('POST /api/v1/account/terms')[0].body).toEqual({ version: TERMS_VERSION })
    // The person is where they were going, and a screen reader is told.
    expect(current()).toBe('/rules')
    await waitFor(() => expect(screen.getByText('Thanks. You agreed to the terms.')).toBeTruthy())
  })

  it('asks an account made before there were terms the same way', async () => {
    agreedTo(undefined)
    await open('/')
    await screen.findByRole('heading', { level: 1, name: 'The terms changed' })
    expect(screen.getByText(/^Permaily now has terms/).textContent).toBe(
      'Permaily now has terms of service and a privacy policy. Read them, and agree to keep using your account.',
    )
  })

  it('asks nothing of an account that agreed to the terms in force, nor when the server says none', async () => {
    agreedTo(TERMS_VERSION)
    await open('/')
    await screen.findByRole('navigation', { name: 'Main' })
    // A server from before says nothing of the terms in force.
    server.on('GET /api/v1/me', ok(meOf(ana)))
    await open('/')
    await screen.findByRole('navigation', { name: 'Main' })
  })

  it('says when the agreement could not be saved, and lets the person sign out instead', async () => {
    agreedTo('2026-01-01')
    server.on('POST /api/v1/account/terms', network)
    const { user } = await open('/')
    await screen.findByRole('heading', { level: 1, name: 'The terms changed' })
    await user.click(termsBox())
    const agreeButton = screen.getByRole('button', { name: 'Agree and continue' })
    await user.click(agreeButton)
    await screen.findByText(
      "Your agreement wasn't saved: Permaily can't be reached. Check your connection, then try again.",
    )
    await waitFor(() => expect(document.activeElement).toBe(agreeButton))

    server.on('POST /api/v1/account/terms', refused(400, 'terms_needed'))
    await user.click(agreeButton)
    await settle()

    server.on('POST /api/v1/auth/sign-out', ok()).signedOut()
    await user.click(screen.getByRole('button', { name: 'Sign out' }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })

  it('says when the server refused the agreement for another reason, and sends it once at a time', async () => {
    agreedTo('2026-01-01')
    const { user } = await open('/')
    await screen.findByRole('heading', { level: 1, name: 'The terms changed' })
    await user.click(termsBox())
    const waiting = later()
    server.on('POST /api/v1/account/terms', waiting.answer)
    const agreeButton = screen.getByRole('button', { name: 'Agree and continue' })
    await user.click(agreeButton)
    await user.click(agreeButton)
    expect(server.asked('POST /api/v1/account/terms')).toHaveLength(1)
    waiting.give(refused(500, 'other'))
    await screen.findByText(
      "Your agreement wasn't saved. Something went wrong on our side. Try again in a moment.",
    )
  })

  it('says when signing out failed', async () => {
    agreedTo('2026-01-01')
    server.on('POST /api/v1/auth/sign-out', network)
    const { user } = await open('/')
    await user.click(await screen.findByRole('button', { name: 'Sign out' }))
    await screen.findByText(
      "Couldn't sign out: Permaily can't be reached. Check your connection, then try again.",
    )
    server.on('POST /api/v1/auth/sign-out', refused(500, 'other'))
    await user.click(screen.getByRole('button', { name: 'Sign out' }))
    await screen.findByText("Couldn't sign out. Try again in a moment.")
  })

  it('lets the person delete the account from there', async () => {
    agreedTo('2026-01-01')
    server.on(
      'POST /api/v1/account/delete',
      ok({ ...ana, terms: '2026-01-01', state: 'closing', closes_at: '2026-11-19T10:00:00Z' }),
    )
    const { user } = await open('/')
    await user.click(await screen.findByRole('button', { name: 'Delete my account' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Delete your account?' }))
    await user.type(dialog.getByLabelText('Address of this account'), 'ana@example.org')
    await user.click(dialog.getByRole('button', { name: 'Delete my account' }))
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
  })

  it('shows the screen when a request finds out that the terms changed', async () => {
    agreedTo(TERMS_VERSION)
    server.settings()
    const { user } = await open('/settings')
    await screen.findByText('1 device signed in')
    // A new release meanwhile: the next change is refused, and the app asks who this is again.
    server
      .on('PATCH /api/v1/account', refused(400, 'terms_needed'))
      .on('GET /api/v1/me', ok(meOf({ ...ana, terms: TERMS_VERSION }, {}, '2027-01-15')))
    const profile = within(screen.getByRole('region', { name: 'Profile' }))
    await user.click(profile.getByRole('button', { name: /^Edit\s*name$/ }))
    await user.clear(profile.getByLabelText('Name'))
    await user.type(profile.getByLabelText('Name'), 'Ana B')
    await user.click(profile.getByRole('button', { name: 'Save' }))
    await screen.findByRole('heading', { level: 1, name: 'The terms changed' })
  })
})
