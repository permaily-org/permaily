import { act, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, expectNotKept, open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, later, meOf, network, ok, refused, server } from '../../helpers/server.ts'

const link = '/sign-in#attempt=att_link_1&token=tok_secret_1'
const eve = { ...ana, id: 'acc_eve', address: 'eve@example.org', name: 'Eve' }
const whose = ok({ address: 'ana@example.org' })

describe('the link of a sign-in message', () => {
  it('signs nobody in by being opened: it asks first, with the address the server gave', async () => {
    const looking = later()
    server.signedOut().on('POST /api/v1/auth/email/look', looking.answer)
    const { user } = await open(link)
    // Out of the address bar before anything renders.
    expect(current()).toBe('/sign-in')
    expect(screen.getByRole('status').textContent).toBe('Checking your link')
    await waitFor(() => expect(document.title).toBe('Checking your link · Permaily'))
    looking.give(whose)

    const question = await screen.findByRole('heading', { name: 'Sign in to this account?' })
    // Focus is on the question, not on an action: Enter signs nobody in unread.
    await waitFor(() => expect(document.activeElement).toBe(question))
    expect(screen.getByText('ana@example.org')).toBeTruthy()
    expect(server.asked('POST /api/v1/auth/email/look')[0].body).toEqual({
      attempt: 'att_link_1',
      token: 'tok_secret_1',
    })
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(0)
    // Neither action is styled as the one to take.
    const yes = screen.getByRole('button', { name: 'Sign in' })
    const no = screen.getByRole('button', { name: "Don't sign in" })
    expect(yes.className).toBe(no.className)
    await expectAccessible()

    server.on('POST /api/v1/auth/email/finish', ok(meOf()))
    await user.click(yes)
    await screen.findByRole('navigation', { name: 'Main' })
    expect(server.asked('POST /api/v1/auth/email/finish')[0].body).toEqual({
      attempt: 'att_link_1',
      token: 'tok_secret_1',
    })
    expectNotKept('tok_secret_1', 'att_link_1', 'ana@example.org')
  })

  it('is forgotten unused when the person says no', async () => {
    server.signedOut().on('POST /api/v1/auth/email/look', whose)
    const { user } = await open(link)
    await user.click(await screen.findByRole('button', { name: "Don't sign in" }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(0)
    expectNotKept('tok_secret_1')
  })

  it('just opens the app in a browser already in that account', async () => {
    server.signedIn().on('POST /api/v1/auth/email/look', whose)
    await open(link)
    await screen.findByRole('navigation', { name: 'Main' })
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(0)
  })

  it('says so in a browser signed in to another account, with both addresses', async () => {
    server.signedIn(eve).on('POST /api/v1/auth/email/look', whose)
    const { user } = await open(link)
    await screen.findByRole('heading', { name: 'Switch to this account?' })
    expect(screen.getAllByText('eve@example.org')).toHaveLength(2)
    expect(screen.getByText('ana@example.org')).toBeTruthy()
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: "Don't switch" }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain(
      'eve@example.org',
    )
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(0)
  })

  it('switches accounts only when the person says so', async () => {
    server
      .signedIn(eve)
      .on('POST /api/v1/auth/email/look', whose)
      .on('POST /api/v1/auth/email/finish', ok(meOf()))
    const { user } = await open(link)
    await user.click(await screen.findByRole('button', { name: 'Switch account' }))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Account menu/ }).textContent).toContain(
        'ana@example.org',
      ),
    )
  })

  it.each([
    [refused(400, 'invalid_code'), 'That link no longer works'],
    [refused(429, 'too_many_requests'), "That link couldn't sign you in"],
  ])('says that it no longer works, with the form to ask for another', async (answer, title) => {
    server.signedOut().on('POST /api/v1/auth/email/look', answer)
    await open(link)
    await screen.findByRole('heading', { name: title })
    expect(screen.getByText(/Enter your address to ask for a new one\./)).toBeTruthy()
    expect(screen.getByLabelText('Email address').getAttribute('aria-describedby')).toBe(
      'link-problem',
    )
    await expectAccessible()
  })

  it('says nothing of a link that no longer works to someone who is signed in', async () => {
    server.signedIn().on('POST /api/v1/auth/email/look', refused(400, 'invalid_code'))
    await open(link)
    await screen.findByRole('navigation', { name: 'Main' })
  })

  it('knows a link that a mail app cut short', async () => {
    server.signedOut()
    await open('/sign-in#attempt=att_link_1')
    await screen.findByRole('heading', { name: 'That link no longer works' })
    expect(current()).toBe('/sign-in')
  })

  it('is kept when the server gives no answer, before the question and after it', async () => {
    server.on('POST /api/v1/auth/email/look', network)
    const { user } = await open(link)
    expect((await screen.findByRole('alert')).textContent).toContain("Permaily can't be reached")
    server.signedOut().on('POST /api/v1/auth/email/look', whose)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    const yes = await screen.findByRole('button', { name: 'Sign in' })

    server.on('POST /api/v1/auth/email/finish', network)
    await user.click(yes)
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    await waitFor(() => expect(document.activeElement).toBe(yes))
    server.on('POST /api/v1/auth/email/finish', ok(meOf()))
    await user.click(yes)
    await screen.findByRole('navigation', { name: 'Main' })
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(2)
  })

  it('says so when it ended between the question and the answer', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/look', whose)
      .on('POST /api/v1/auth/email/finish', refused(400, 'invalid_code'))
    const { user } = await open(link)
    await user.click(await screen.findByRole('button', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: 'That link no longer works' })
    expect(screen.getByLabelText('Email address')).toBeTruthy()
  })

  it('tells a browser that is in another account that it still is, before the app opens', async () => {
    server
      .signedIn(eve)
      .on('POST /api/v1/auth/email/look', whose)
      .on('POST /api/v1/auth/email/finish', refused(400, 'invalid_code'))
    const { user } = await open(link)
    await user.click(await screen.findByRole('button', { name: 'Switch account' }))
    const ended = await screen.findByRole('heading', { name: 'That link no longer works' })
    await waitFor(() => expect(document.activeElement).toBe(ended))
    expect(screen.getByText(/This browser is still signed in as/).textContent).toBe(
      'A link works once, and for a short time. This browser is still signed in as eve@example.org.',
    )
    await expectAccessible()
    await user.click(screen.getByRole('button', { name: 'Open Permaily' }))
    await screen.findByRole('navigation', { name: 'Main' })
  })

  it('opens the app when an earlier try had signed in and lost its answer', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/look', whose)
      .on('POST /api/v1/auth/email/finish', () => {
        // The first try did sign in, and its answer was lost on the way.
        server.signedIn()
        return refused(400, 'invalid_code')
      })
    const { user } = await open(link)
    await user.click(await screen.findByRole('button', { name: 'Sign in' }))
    await screen.findByRole('navigation', { name: 'Main' })
  })

  it('is nowhere but the page memory: a reload while the question shows loses it', async () => {
    server.signedOut().on('POST /api/v1/auth/email/look', whose)
    await open(link)
    await screen.findByRole('heading', { name: 'Sign in to this account?' })
    expectNotKept('tok_secret_1', 'att_link_1')
    await open(current())
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(server.asked('POST /api/v1/auth/email/look')).toHaveLength(1)
  })

  it('is asked about all the same when it is pasted over the open sign-in screen', async () => {
    server.signedOut()
    await open('/sign-in')
    await screen.findByRole('heading', { name: 'Sign in' })
    server.on('POST /api/v1/auth/email/look', whose)
    act(() => {
      window.history.pushState(null, '', link)
      window.dispatchEvent(new Event('hashchange'))
    })
    await screen.findByRole('heading', { name: 'Sign in to this account?' })
    expect(current()).toBe('/sign-in')
  })

  it('is forgotten when the person signs out', async () => {
    server
      .signedIn(eve)
      .on('POST /api/v1/auth/email/look', whose)
      .on('POST /api/v1/auth/sign-out', ok())
    const { user } = await open(link)
    await screen.findByRole('heading', { name: 'Switch to this account?' })
    // Another tab of this browser signs out, and this one hears of it.
    server.signedOut()
    window.dispatchEvent(new Event('focus'))
    await screen.findByRole('heading', { name: 'Sign in to this account?' })
    await user.click(screen.getByRole('button', { name: "Don't sign in" }))
    await screen.findByRole('heading', { name: 'Sign in' })
  })
})
