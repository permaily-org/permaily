import { screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { TERMS_VERSION } from '../../../src/lib/terms.ts'
import { agree, current, expectNotKept, open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { meOf, network, ok, refused, server } from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const started = ok({ attempt: 'att_secret_1', expires_in: 600 })

/** Fills in the address and asks for the message. */
async function ask(user: User, address = 'ana@example.org') {
  await user.type(await screen.findByLabelText('Email address'), address)
  await agree(user)
  await user.click(screen.getByRole('button', { name: 'Continue' }))
}

describe('signing in by mail', () => {
  it('asks for the address, then for the code, and opens the app', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-in')
    const address = await screen.findByLabelText('Email address')
    await waitFor(() => expect(document.activeElement).toBe(address))
    await waitFor(() => expect(document.title).toBe('Sign in · Permaily'))
    await expectAccessible()

    await ask(user)
    await screen.findByRole('heading', { name: 'Check your mail' })
    // It never says whether the address has an account, and promises no code.
    expect(screen.getByText(/can sign in, a message is on its way/).textContent).toBe(
      'If ana@example.org can sign in, a message is on its way. It works for 10 minutes.',
    )
    const code = screen.getByLabelText('Code from the message')
    await waitFor(() => expect(document.activeElement).toBe(code))
    expect(code.getAttribute('autocomplete')).toBe('one-time-code')
    await expectAccessible()

    server.on('POST /api/v1/auth/email/finish', ok(meOf()))
    // A pasted code may come with spaces or a dash: only its digits are kept.
    await user.click(code)
    await user.paste('123 456')
    expect((code as HTMLInputElement).value).toBe('123456')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(server.asked('POST /api/v1/auth/email/start')[0].body).toEqual({
      address: 'ana@example.org',
    })
    expect(server.asked('POST /api/v1/auth/email/finish')[0].body).toEqual({
      attempt: 'att_secret_1',
      code: '123456',
    })
    // Nothing typed or received is anywhere but the page's memory.
    expectNotKept('123456', 'att_secret_1', 'ana@example.org')
    expect({ ...localStorage }).toEqual({})
    expect({ ...sessionStorage }).toEqual({})
  })

  it('says what is wrong with the address before and after asking', async () => {
    server.signedOut()
    const { user } = await open('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Continue' }))
    const address = screen.getByLabelText('Email address')
    expect(screen.getByText('Enter your email address.').getAttribute('role')).toBe('alert')
    expect(address.getAttribute('aria-invalid')).toBe('true')
    await waitFor(() => expect(document.activeElement).toBe(address))
    expect(server.asked('POST /api/v1/auth/email/start')).toHaveLength(0)
    await expectAccessible()

    server.on('POST /api/v1/auth/email/start', refused(400, 'invalid_address'))
    await ask(user, 'ana@nowhere')
    await screen.findByText("Permaily can't use that address. Check it for typos.")
    await waitFor(() => expect(document.activeElement).toBe(address))
  })

  it.each([
    [refused(429, 'too_many_requests'), 'Too many attempts. Wait a few minutes, then try again.'],
    [network, "Permaily can't be reached. Check your connection, then try again."],
    [refused(500, 'internal'), 'Something went wrong on our side. Try again in a moment.'],
  ])('says why no message was asked for', async (answer, words) => {
    server.signedOut().on('POST /api/v1/auth/email/start', answer)
    const { user } = await open('/sign-in')
    await ask(user)
    expect((await screen.findByText(words)).getAttribute('role')).toBe('alert')
    // And the form can be sent again.
    expect((screen.getByRole('button', { name: 'Continue' }) as HTMLButtonElement).disabled).toBe(
      false,
    )
  })

  it('says what is wrong with a code, and keeps the step for another try', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-in')
    await ask(user)
    const code = await screen.findByLabelText('Code from the message')
    await user.type(code, '123')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText('Enter the 6 digits of the code.')
    expect(server.asked('POST /api/v1/auth/email/finish')).toHaveLength(0)

    server.on('POST /api/v1/auth/email/finish', refused(400, 'invalid_code'))
    await user.type(code, '456')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText(
      "That code isn't right, or it has expired. Check it, or send another message.",
    )
    await waitFor(() => expect(document.activeElement).toBe(code))

    server.on('POST /api/v1/auth/email/finish', network)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    await expectAccessible()
  })

  it('sends another message, and goes back for another address', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-in')
    await ask(user)
    await user.type(await screen.findByLabelText('Code from the message'), '111111')
    server.on('POST /api/v1/auth/email/start', ok({ attempt: 'att_secret_2', expires_in: 60 }))
    await user.click(screen.getByRole('button', { name: 'Send another message' }))
    const note = await screen.findByText(
      'Asked for another message. Use the code or the link of the newest one.',
    )
    expect(note.getAttribute('role')).toBe('status')
    expect((screen.getByLabelText('Code from the message') as HTMLInputElement).value).toBe('')
    expect(screen.getByText(/It works for 1 minute\./)).toBeTruthy()

    // The code is entered against the newest message.
    server.on('POST /api/v1/auth/email/finish', refused(400, 'invalid_code'))
    await user.type(screen.getByLabelText('Code from the message'), '222222')
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() =>
      expect(server.asked('POST /api/v1/auth/email/finish')[0].body).toEqual({
        attempt: 'att_secret_2',
        code: '222222',
      }),
    )

    server.on('POST /api/v1/auth/email/start', refused(429, 'too_many_requests'))
    await user.click(screen.getByRole('button', { name: 'Send another message' }))
    await screen.findByText('Too many attempts. Wait a few minutes, then try again.')

    await user.click(screen.getByRole('button', { name: 'Use another address' }))
    const address = (await screen.findByLabelText('Email address')) as HTMLInputElement
    expect(address.value).toBe('ana@example.org')
  })

  it('says when the message has expired, instead of failing later', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 0 }))
    const { user } = await open('/sign-in')
    await ask(user)
    await screen.findByText(/It has expired by now\./)
  })

  it('leads to the sign-up screen and back, keeping where the person was going', async () => {
    server.signedOut()
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('link', { name: 'Create an account' }))
    await screen.findByRole('heading', { name: 'Create your account' })
    expect(current()).toBe('/sign-up')
    expect(window.history.state.usr).toEqual({ from: '/rules' })
    await user.click(screen.getByRole('link', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(window.history.state.usr).toEqual({ from: '/rules' })
  })
})

describe('signing up', () => {
  it('takes an invitation typed any way, shows it in groups, and makes the account', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-up')
    await screen.findByRole('heading', { name: 'Create your account' })
    await waitFor(() => expect(document.title).toBe('Create your account · Permaily'))
    const invitation = screen.getByLabelText('Invitation') as HTMLInputElement
    await user.type(screen.getByLabelText('Email address'), 'ana@example.org')
    await user.type(invitation, 'abcd2345efgh')
    expect(invitation.value).toBe('ABCD-2345-EFGH')
    await expectAccessible()
    await agree(user)
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByRole('heading', { name: 'Check your mail' })
    expect(screen.getByText(/A message is on its way to/).textContent).toBe(
      'A message is on its way to ana@example.org. It works for 10 minutes.',
    )
    expect(server.asked('POST /api/v1/auth/email/start')[0].body).toEqual({
      address: 'ana@example.org',
      invitation: 'abcd2345efgh',
      terms: TERMS_VERSION,
    })
    server.on('POST /api/v1/auth/email/finish', ok(meOf()))
    await user.type(screen.getByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expectNotKept('abcd2345efgh', 'ABCD-2345-EFGH', '123456', 'att_secret_1', 'ana@example.org')
  })

  it('fills in an invitation that arrived by link, and takes it out of the address bar', async () => {
    server.signedOut()
    await open('/sign-up?invitation=ABCD-2345-EFGH')
    const invitation = (await screen.findByLabelText('Invitation')) as HTMLInputElement
    expect(invitation.value).toBe('ABCD-2345-EFGH')
    expect(current()).toBe('/sign-up')
    expectNotKept('ABCD-2345-EFGH', 'abcd2345efgh')
  })

  it('says what cannot be an invitation before anything is asked', async () => {
    server.signedOut()
    const { user } = await open('/sign-up')
    await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
    await user.type(screen.getByLabelText('Invitation'), 'abcd')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await screen.findByText(
      'An invitation has 12 letters and digits. Check it against the one you got.',
    )
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Invitation')))
    expect(server.asked('POST /api/v1/auth/email/start')).toHaveLength(0)
    await expectAccessible()
  })

  it('says in its field that the server refused the invitation, at either step', async () => {
    const refusal =
      "That invitation isn't valid, or was used already. Check it, or ask for a new one."
    server.signedOut().on('POST /api/v1/auth/email/start', refused(400, 'invalid_invitation'))
    const { user } = await open('/sign-up?invitation=abcd2345efgh')
    await ask(user)
    await screen.findByText(refusal)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Invitation')))

    // Used by someone else between the message and its code: back to where it is typed.
    server
      .on('POST /api/v1/auth/email/start', started)
      .on('POST /api/v1/auth/email/finish', refused(400, 'invalid_invitation'))
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await user.type(await screen.findByLabelText('Code from the message'), '123456')
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    await screen.findByText(refusal)
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Invitation')))
  })

  it('has its own words for another message that an invitation does not allow', async () => {
    server.signedOut().on('POST /api/v1/auth/email/start', started)
    const { user } = await open('/sign-up?invitation=abcd2345efgh')
    await ask(user)
    await screen.findByLabelText('Code from the message')
    server.on('POST /api/v1/auth/email/start', refused(400, 'invalid_invitation'))
    await user.click(screen.getByRole('button', { name: 'Send another message' }))
    await screen.findByText(
      "Another message can't be sent with that invitation right now. Use the one you have, or try again in an hour.",
    )
    // The step stays: the message the person has may still work.
    expect(screen.getByLabelText('Code from the message')).toBeTruthy()
  })
})
