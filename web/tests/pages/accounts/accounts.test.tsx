import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { act, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, expectNotKept, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  confirmationOf,
  lost,
  mailboxOf,
  personal,
  personalAddress,
  work,
  workAddress,
} from '../../helpers/mailboxes.ts'
import {
  confirmationNeeded,
  later,
  meOf,
  network,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

const list = 'GET /api/v1/mailboxes'

function rows() {
  return within(screen.getByRole('list', { name: 'Your mailboxes' }))
}

function row(name: string) {
  return within(screen.getByRole('listitem', { name }))
}

describe('W1: the list of mailboxes', () => {
  it('lists each mailbox from the server with its address, label, state and retention', async () => {
    server.signedIn().on(list, ok({ mailboxes: [personal, work], limit: 5 }))
    await open('/accounts')
    await screen.findByRole('heading', { level: 1, name: '2 mailboxes connected.' })
    expect(screen.getByText('An account can have 5.')).toBeTruthy()
    const first = row('Personal')
    expect(first.getByText(personalAddress)).toBeTruthy()
    expect(first.getByText('Receiving since 3 Nov 2026')).toBeTruthy()
    expect(first.getByText('Not confirmed by Google')).toBeTruthy()
    expect((first.getByLabelText('Keep mail for') as HTMLSelectElement).value).toBe('90')
    expect(
      first.getByRole('link', { name: named('Open inbox of Personal') }).getAttribute('href'),
    ).toBe('/inbox/mbx_personal')
    const second = row('Work')
    expect(second.getByText('Waiting for Gmail')).toBeTruthy()
    // A mailbox that is waiting has no mail to open yet, and leads to its set-up.
    expect(second.queryByRole('link', { name: /Open inbox/ })).toBeNull()
    expect(
      second.getByRole('link', { name: named('Forwarding set-up of Work') }).getAttribute('href'),
    ).toBe('/accounts/mbx_work/forwarding')
    expect(screen.getByRole('link', { name: 'Connect a mailbox' })).toBeTruthy()
    await expectAccessible()
    expectNotKept(personalAddress, workAddress)
  })

  it('says when there is none, offers no more at the limit, and says when the list could not load', async () => {
    server.signedIn().on(list, ok({ mailboxes: [], limit: 5 }))
    await open('/accounts')
    await screen.findByRole('heading', { level: 1, name: 'No mailboxes connected.' })
    expect(
      screen.getByText('Forward your Gmail to a mailbox of your own, then read it here.'),
    ).toBeTruthy()

    server.on(list, ok({ mailboxes: [personal, work], limit: 2 }))
    await open('/accounts')
    await screen.findByRole('heading', { level: 1, name: '2 mailboxes connected.' })
    expect(
      screen.getByText("That's as many as an account can have. Disconnect one to connect another."),
    ).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'Connect a mailbox' })).toBeNull()

    server.on(list, network)
    const { user } = await open('/accounts')
    const alert = await screen.findByRole('alert', { name: '' })
    expect(alert.textContent).toContain(
      "Your mailboxes couldn't load. Permaily can't be reached. Check your connection, then try again.",
    )
    await expectAccessible()
    server.on(list, ok({ mailboxes: [personal], limit: 5 }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: '1 mailbox connected.' })
  })

  it('copies the address, renames in place and changes the retention, saying it applies from now on', async () => {
    let kept: Record<string, unknown> = personal
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on('PATCH /api/v1/mailboxes/mbx_personal', (asked) => {
        const body = asked.body as { label?: string; retention_days?: number }
        if (body.label === 'Taken') return refused(400, 'invalid_label')
        kept = { ...kept, ...body }
        return ok(kept)
      })
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    await user.click(row('Personal').getByRole('button', { name: /^Copy/ }))
    expect(writeText).toHaveBeenCalledWith(personalAddress)
    await row('Personal').findByText('Copied')

    await user.click(row('Personal').getByRole('button', { name: /^Rename/ }))
    const field = row('Personal').getByLabelText('Label')
    await waitFor(() => expect(document.activeElement).toBe(field))
    await user.clear(field)
    await user.type(field, 'x'.repeat(65))
    await user.click(row('Personal').getByRole('button', { name: 'Save' }))
    expect(row('Personal').getByText('A label is one line of at most 64 characters.')).toBeTruthy()
    expect(server.asked('PATCH /api/v1/mailboxes/mbx_personal')).toHaveLength(0)
    await user.clear(field)
    await user.type(field, 'Taken')
    await user.click(row('Personal').getByRole('button', { name: 'Save' }))
    await row('Personal').findByText(
      "The label wasn't changed. A label is one line of at most 64 characters.",
    )
    await user.clear(field)
    await user.type(field, 'Home')
    await user.click(row('Personal').getByRole('button', { name: 'Save' }))
    await screen.findByRole('heading', { level: 2, name: 'Home' })
    expect(server.asked('PATCH /api/v1/mailboxes/mbx_personal').at(-1)?.body).toEqual({
      label: 'Home',
    })

    // Escape leaves the label as it was.
    await user.click(row('Home').getByRole('button', { name: /^Rename/ }))
    await user.keyboard('{Escape}')
    await screen.findByRole('heading', { level: 2, name: 'Home' })

    await user.selectOptions(row('Home').getByLabelText('Keep mail for'), '30')
    await row('Home').findByText('Saved. It applies to mail from now on.')
    expect(server.asked('PATCH /api/v1/mailboxes/mbx_personal').at(-1)?.body).toEqual({
      retention_days: 30,
    })

    server.on('PATCH /api/v1/mailboxes/mbx_personal', network)
    await user.selectOptions(row('Home').getByLabelText('Keep mail for'), '365')
    await row('Home').findByText(
      "Nothing changed. Permaily can't be reached. Check your connection, then try again.",
    )
    await expectAccessible()
  })

  it('says why copying failed', async () => {
    server.signedIn().on(list, ok({ mailboxes: [personal], limit: 5 }))
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    await user.click(row('Personal').getByRole('button', { name: /^Copy/ }))
    await row('Personal').findByText("Couldn't copy. Select it and copy it yourself.")
  })
})

describe('W1: connecting a mailbox and its forwarding set-up', () => {
  it('makes the mailbox with its label and opens its set-up, with the address to copy and Gmail’s steps', async () => {
    const made = mailboxOf({
      id: 'mbx_new',
      label: 'Trips',
      state: 'waiting',
      receiving_since: undefined,
    })
    server
      .signedIn()
      .on(list, ok({ mailboxes: [], limit: 5 }))
      .on('POST /api/v1/mailboxes', ok(made))
      .on('GET /api/v1/mailboxes/mbx_new', ok(made))
    const { user } = await open('/accounts/new')
    await screen.findByRole('heading', { level: 1, name: 'Connect a mailbox' })
    expect(screen.getByText('Connecting with a Google account is coming later.')).toBeTruthy()
    // Nothing offers it.
    expect(screen.queryByRole('button', { name: /Google/ })).toBeNull()
    await expectAccessible()
    await user.type(screen.getByLabelText('Label (optional)'), '  Trips ')
    await user.click(screen.getByRole('button', { name: 'Make the address' }))
    await screen.findByRole('heading', { level: 1, name: 'Forward your Gmail here' })
    expect(current()).toBe('/accounts/mbx_new/forwarding')
    expect(server.asked('POST /api/v1/mailboxes')[0].body).toEqual({ label: 'Trips' })
    expect(screen.getAllByText(personalAddress).length).toBeGreaterThan(0)
    expect(
      screen.getByText('Open Forwarding and POP/IMAP, and choose Add a forwarding address.'),
    ).toBeTruthy()
    // The filter step of the sample is gone: Gmail forwards all mail.
    expect(screen.queryByText(/filter/i)).toBeNull()
    expect(screen.getByText('Waiting for the first mail.')).toBeTruthy()
    await expectAccessible()
    expectNotKept(personalAddress)
  })

  it('says when the account has as many mailboxes as it can, and a label it cannot keep', async () => {
    server
      .signedIn()
      .on(
        'POST /api/v1/mailboxes',
        once(refused(409, 'too_many_mailboxes'), refused(400, 'invalid_label')),
      )
    const { user } = await open('/accounts/new')
    await screen.findByRole('heading', { level: 1, name: 'Connect a mailbox' })
    await user.click(screen.getByRole('button', { name: 'Make the address' }))
    await screen.findByText(
      'No mailbox was made. This account has as many mailboxes as it can have. Disconnect one first.',
    )
    expect(server.asked('POST /api/v1/mailboxes')[0].body).toEqual({ label: '' })
    await user.click(screen.getByRole('button', { name: 'Make the address' }))
    await screen.findByText('A label is one line of at most 64 characters.')
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByLabelText('Label (optional)')),
    )
    await user.type(screen.getByLabelText('Label (optional)'), 'y'.repeat(70))
    await user.click(screen.getByRole('button', { name: 'Make the address' }))
    expect(server.asked('POST /api/v1/mailboxes')).toHaveLength(2)
    expect(current()).toBe('/accounts/new')
    await expectAccessible()
  })

  it('shows Gmail’s code as soon as it arrives, asking every five seconds, then stops after fifteen minutes', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'], shouldAdvanceTime: true })
    const waiting = mailboxOf({ state: 'waiting', receiving_since: undefined })
    const coded = { ...waiting, confirmations: [confirmationOf()] }
    let answer: unknown = waiting
    server.signedIn().on('GET /api/v1/mailboxes/mbx_personal', () => ok(answer))
    await open('/accounts/mbx_personal/forwarding')
    await screen.findByText("Waiting for Gmail's message. This page checks every few seconds.")
    const asked = () => server.asked('GET /api/v1/mailboxes/mbx_personal').length
    expect(asked()).toBe(1)
    await act(() => vi.advanceTimersByTimeAsync(5_000))
    await waitFor(() => expect(asked()).toBe(2))

    answer = coded
    await act(() => vi.advanceTimersByTimeAsync(5_000))
    await screen.findByText('836104527')
    const confirmation = within(screen.getByRole('list', { name: "Gmail's confirmations" }))
    expect(confirmation.getByText(/^Asked by/).textContent).toBe(
      'Asked by ana.ruiz@example.com, 12 Nov 2026, 10:04 UTC',
    )
    // Google's check is off: no confirmation is said to be verified.
    expect(
      confirmation.getByText(
        "Not confirmed by Google: if you didn't ask Gmail to forward here, ignore it.",
      ),
    ).toBeTruthy()
    expect(confirmation.getByText(/is your account\.$/).textContent).toBe(
      'Copy this code into Gmail only if ana.ruiz@example.com is your account.',
    )
    // The confirmation's link is never shown.
    expect(screen.queryByRole('link', { name: /google|gmail|verify/i })).toBeNull()
    expectNotKept('836104527', personalAddress)

    // Still waiting for the first mail: it goes on asking, for fifteen minutes in all.
    await act(() => vi.advanceTimersByTimeAsync(15 * 60_000))
    const all = asked()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(asked()).toBe(all)
    expect(all).toBeLessThanOrEqual(15 * 12 + 1)

    // Once the first mail came, the page says so.
    answer = { ...coded, state: 'receiving', receiving_since: '2026-11-12T10:30:00Z' }
    await open('/accounts/mbx_personal/forwarding')
    await screen.findByText('Receiving since 12 Nov 2026.', { selector: '[role="status"]' })
    expect(screen.getByRole('link', { name: 'Open the inbox' })).toBeTruthy()
    await expectAccessible()
  })

  it('offers Check again once it stopped asking, and asks nothing while the page is hidden', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'], shouldAdvanceTime: true })
    const waiting = mailboxOf({ state: 'waiting', receiving_since: undefined })
    server.signedIn().on('GET /api/v1/mailboxes/mbx_personal', ok(waiting))
    const { user } = await open('/accounts/mbx_personal/forwarding')
    await screen.findByText("Waiting for Gmail's message. This page checks every few seconds.")
    const hidden = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await act(() => vi.advanceTimersByTimeAsync(30_000))
    expect(server.asked('GET /api/v1/mailboxes/mbx_personal')).toHaveLength(1)
    hidden.mockRestore()
    await act(() => vi.advanceTimersByTimeAsync(15 * 60_000))
    await screen.findByText('Nothing from Gmail yet.')
    const before = server.asked('GET /api/v1/mailboxes/mbx_personal').length
    server.on(
      'GET /api/v1/mailboxes/mbx_personal',
      ok({ ...waiting, confirmations: [confirmationOf({ verified: true })] }),
    )
    await user.click(screen.getByRole('button', { name: 'Check again' }))
    await screen.findByText('Google confirmed this request.')
    expect(server.asked('GET /api/v1/mailboxes/mbx_personal')).toHaveLength(before + 1)
  })

  it('says when the mailbox is gone, paused, cannot be read or could not load', async () => {
    server.signedIn().on('GET /api/v1/mailboxes/mbx_personal', refused(404, 'not_found'))
    await open('/accounts/mbx_personal/forwarding')
    await screen.findByText("That mailbox isn't there any more.", { exact: false })

    server.on('GET /api/v1/mailboxes/mbx_personal', ok(lost))
    await open('/accounts/mbx_personal/forwarding')
    await screen.findByText(/This mailbox can't be read: it accepts no mail\./)

    server.on(
      'GET /api/v1/mailboxes/mbx_personal',
      ok(mailboxOf({ state: 'paused', paused_at: '2026-11-10T10:00:00Z' })),
    )
    await open('/accounts/mbx_personal/forwarding')
    await screen.findByText(/^Paused by Permaily: it accepts no new mail/)
    await expectAccessible()

    server.on('GET /api/v1/mailboxes/mbx_personal', network)
    const { user } = await open('/accounts/mbx_personal/forwarding')
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain("The mailbox couldn't load.")
    server.on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: 'Forward your Gmail here' })
  })

  it('leads the old address of the sample to connecting', async () => {
    server.signedIn()
    await open('/accounts/new/forwarding')
    await screen.findByRole('heading', { level: 1, name: 'Connect a mailbox' })
    expect(current()).toBe('/accounts/new')
  })
})

describe('W2: what Google confirmed', () => {
  it('shows every confirmation not confirmed while the mark is off, and verified or for another address when it is on', async () => {
    const shown = mailboxOf({
      state: 'waiting',
      receiving_since: undefined,
      confirmations: [
        confirmationOf({ code: '111222333', verified: true }),
        confirmationOf({
          code: '',
          requester: undefined,
          for_another_address: true,
          verified: true,
          received_at: '2026-11-12T09:00:00Z',
        }),
        confirmationOf({ code: '444555666', received_at: '2026-11-11T09:00:00Z' }),
      ],
    })
    server.signedIn().on('GET /api/v1/mailboxes/mbx_personal', ok(shown))
    await open('/accounts/mbx_personal/forwarding')
    const items = within(
      await screen.findByRole('list', { name: "Gmail's confirmations" }),
    ).getAllByRole('listitem')
    expect(items).toHaveLength(3)
    expect(within(items[0]).getByText('111222333')).toBeTruthy()
    expect(within(items[0]).getByText('Google confirmed this request.')).toBeTruthy()
    expect(items[1].textContent).toContain(
      'Gmail sent this for another address. It says nothing about this mailbox.',
    )
    // One for another address shows no code.
    expect(within(items[1]).queryByRole('button', { name: /Copy/ })).toBeNull()
    expect(within(items[2]).getByText('444555666')).toBeTruthy()
    expect(
      within(items[2]).getByText(
        "Not confirmed by Google: if you didn't ask Gmail to forward here, ignore it.",
      ),
    ).toBeTruthy()
    await expectAccessible()
  })

  it('says on the list which Gmail account Google confirmed, and what that does not show', async () => {
    const verified = mailboxOf({
      source: {
        provider: 'gmail',
        address: 'ana.ruiz@gmail.example',
        verified_at: '2026-11-03T08:00:00Z',
      },
    })
    server.signedIn().on(list, ok({ mailboxes: [verified], limit: 5 }))
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    expect(row('Personal').getByText('ana.ruiz@gmail.example')).toBeTruthy()
    expect(row('Personal').getByText(/^Google confirmed:/)).toBeTruthy()
    await user.hover(row('Personal').getByRole('button', { name: 'What Google confirms' }))
    expect(screen.getByRole('tooltip').textContent).toBe(
      "Google confirmed that this Gmail account asked to forward its mail here. It doesn't show that forwarding is on, or where later mail comes from.",
    )
  })
})

describe('W3: disconnecting', () => {
  it('asks in the exact words, confirms it is the person, and the row goes', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal, work], limit: 5 }))
      .on('DELETE /api/v1/mailboxes/mbx_personal', once(confirmationNeeded, ok()))
      .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
      .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on(
        'POST /api/v1/auth/confirm/email/finish',
        ok(meOf(undefined, { confirmed_until: '2026-11-12T10:10:00Z' })),
      )
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    await user.click(row('Personal').getByRole('button', { name: named('Disconnect Personal') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Disconnect this mailbox?' }))
    await waitFor(() =>
      expect(document.activeElement).toBe(
        dialog.getByRole('heading', { name: 'Disconnect this mailbox?' }),
      ),
    )
    const words = [
      ...screen.getByRole('dialog', { name: 'Disconnect this mailbox?' }).querySelectorAll('p'),
    ]
      .map((p) => p.textContent)
      .filter((text) => text !== '')
    expect(words).toEqual([
      `Personal ${personalAddress}`,
      'Its address stops accepting mail now, and nobody can open its mail in Permaily any more.',
      "Permaily deletes the messages it holds, usually within minutes. Mail that was arriving at that moment can't be opened and expires on its own.",
      'Gmail keeps its own copy of your mail, and so does anything you downloaded.',
      "Permaily's code never opens this mailbox's key again, but the key isn't destroyed: a copy of Permaily's storage saved before now could still be opened by someone who also got Permaily's root secret, which only Permaily's published servers hold. A record that a mailbox with this number existed, with times and fingerprints and no content, stays in a locked log for ten years.",
      'Turn forwarding to it off in Gmail, or Gmail will tell you forwarding fails.',
    ])
    // The two actions have the same weight.
    const go = dialog.getByRole('button', { name: 'Disconnect and delete mail' })
    const keep = dialog.getByRole('button', { name: 'Keep it' })
    expect(go.className).toBe(keep.className)
    await expectAccessible()

    await user.click(go)
    // The server asked for a fresh proof: the person confirms with a code.
    const confirming = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
    await user.click(await confirming.findByRole('button', { name: 'Email me a code' }))
    await user.type(await confirming.findByLabelText('Code from the message'), '123456')
    await user.click(confirming.getByRole('button', { name: 'Confirm' }))
    await screen.findByText('Mailbox disconnected. Its mail is being deleted.')
    expect(server.asked('DELETE /api/v1/mailboxes/mbx_personal')).toHaveLength(2)
    expect(rows().queryByText(personalAddress)).toBeNull()
    expect(screen.getByRole('heading', { level: 1, name: '1 mailbox connected.' })).toBeTruthy()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
  })

  it('keeps the mailbox when the person keeps it, does not confirm, or the server does not answer', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on('DELETE /api/v1/mailboxes/mbx_personal', confirmationNeeded)
      .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    await user.click(row('Personal').getByRole('button', { name: named('Disconnect Personal') }))
    await user.click(await screen.findByRole('button', { name: 'Keep it' }))
    await settle()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(server.asked('DELETE /api/v1/mailboxes/mbx_personal')).toHaveLength(0)

    await user.click(row('Personal').getByRole('button', { name: named('Disconnect Personal') }))
    await user.click(await screen.findByRole('button', { name: 'Disconnect and delete mail' }))
    const confirming = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
    await user.click(await confirming.findByRole('button', { name: 'Cancel' }))
    await settle()
    expect(screen.getByRole('dialog', { name: 'Disconnect this mailbox?' })).toBeTruthy()
    expect(screen.getByText(personalAddress, { selector: '.font-mono.text-small' })).toBeTruthy()

    server.on('DELETE /api/v1/mailboxes/mbx_personal', network)
    await user.click(screen.getByRole('button', { name: 'Disconnect and delete mail' }))
    await screen.findByText(
      "The mailbox wasn't disconnected. Permaily can't be reached. Check your connection, then try again.",
    )
  })

  it('disconnects a mailbox that cannot be read, and says the words of a forked one, which stays', async () => {
    const forked = { ...lost, id: 'mbx_forked', created_at: '2026-09-01T09:00:00Z' }
    server
      .signedIn()
      .on(list, ok({ mailboxes: [lost, forked], limit: 5 }))
      .on('DELETE /api/v1/mailboxes/mbx_lost', ok())
      .on('DELETE /api/v1/mailboxes/mbx_forked', refused(409, 'mailbox_unreadable'))
    const { user } = await open('/accounts')
    await screen.findByRole('heading', { level: 1, name: '2 mailboxes connected.' })
    const unreadable = screen.getAllByRole('listitem', { name: "Can't be read" })
    expect(unreadable).toHaveLength(2)
    expect(unreadable[0].textContent).toContain('mbx_lost, added 1 Oct 2026')
    // Nothing but Disconnect is offered for it.
    expect(within(unreadable[0]).getAllByRole('button')).toHaveLength(1)
    expect(within(unreadable[0]).getByRole('link', { name: 'support@permaily.com' })).toBeTruthy()
    await expectAccessible()

    await user.click(within(unreadable[0]).getByRole('button', { name: /^Disconnect/ }))
    await user.click(await screen.findByRole('button', { name: 'Disconnect and delete mail' }))
    await screen.findByText('Mailbox disconnected. Its mail is being deleted.')
    expect(screen.getAllByRole('listitem', { name: "Can't be read" })).toHaveLength(1)

    await user.click(
      within(screen.getByRole('listitem', { name: "Can't be read" })).getByRole('button', {
        name: /^Disconnect/,
      }),
    )
    await user.click(await screen.findByRole('button', { name: 'Disconnect and delete mail' }))
    const words = await screen.findByText(/^This mailbox can't be disconnected/)
    expect(words.textContent).toBe(
      "This mailbox can't be disconnected: what Permaily stores for it was changed by someone else. It accepts no mail and nobody can open it. Write to support@permaily.com",
    )
    expect(screen.queryByRole('dialog')).toBeNull()
    // It stays, counted.
    expect(screen.getByRole('heading', { level: 1, name: '1 mailbox connected.' })).toBeTruthy()
  })

  it('says the words the server has for a forked mailbox, as they are', () => {
    // The app's words are contracts.ErrMailboxForked's, which the server sends with mailbox_unreadable.
    const contracts = readFileSync(
      resolve(process.cwd(), '../internal/contracts/mailboxes.go'),
      'utf8',
    )
    const go = /ErrMailboxForked = &Error\{"mailbox_unreadable", "([^"]+)"\}/.exec(contracts)?.[1]
    expect(go).toBeTruthy()
    const words = (go as string).charAt(0).toUpperCase() + (go as string).slice(1)
    return import('../../../src/pages/accounts/shared.tsx').then(({ forkedWords }) =>
      expect(forkedWords).toBe(words),
    )
  })

  it('leaves the session to say a session that ended', async () => {
    const answer = later()
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on('DELETE /api/v1/mailboxes/mbx_personal', answer.answer)
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    await user.click(row('Personal').getByRole('button', { name: named('Disconnect Personal') }))
    await user.click(await screen.findByRole('button', { name: 'Disconnect and delete mail' }))
    // While it is on its way, the dialog stays and a second click does nothing.
    await user.click(screen.getByRole('button', { name: 'Disconnect and delete mail' }))
    expect(server.asked('DELETE /api/v1/mailboxes/mbx_personal')).toHaveLength(1)
    server.signedOut()
    answer.give(refused(401, 'unauthorized'))
    await waitFor(() => expect(current()).toBe('/sign-in'))
  })
})
