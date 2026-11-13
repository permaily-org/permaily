import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { current, expectNotKept, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  confirmationOf,
  hostile,
  lost,
  mailboxOf,
  messageOf,
  personal,
  personalAddress,
  summaryOf,
  work,
} from '../../helpers/mailboxes.ts'
import { network, ok, refused, server } from '../../helpers/server.ts'

const list = 'GET /api/v1/mailboxes'
const first = 'GET /api/v1/mailboxes/mbx_personal/messages?limit=50'
const one = messageOf()
const messageRoute = `GET /api/v1/mailboxes/mbx_personal/messages/${one.id}`

function messages() {
  return within(screen.getByRole('list', { name: 'Messages' }))
}

describe('W4: the inbox', () => {
  it('leads /inbox to the first mailbox that can be read', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [lost, personal, work], limit: 5 }))
      .on(first, ok({ messages: [] }))
    await open('/inbox')
    await screen.findByText('No mail here.')
    expect(current()).toBe('/inbox/mbx_personal')
  })

  it('says there is no mailbox yet, or none that can be read, or that the list could not load', async () => {
    server.signedIn().on(list, ok({ mailboxes: [], limit: 5 }))
    await open('/inbox')
    await screen.findByText(
      'No mailbox yet. Mail you forward from Gmail to a mailbox of your own shows here.',
    )
    expect(screen.getByRole('link', { name: 'Connect a mailbox' }).getAttribute('href')).toBe(
      '/accounts/new',
    )
    await expectAccessible()

    server.on(list, ok({ mailboxes: [lost], limit: 5 }))
    await open('/inbox')
    await screen.findByText("Your mailboxes can't be read.", { exact: false })

    server.on(list, network)
    const { user } = await open('/inbox')
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain("Your mailboxes couldn't load.")
    server.on(list, ok({ mailboxes: [], limit: 5 }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await screen.findByText(/^No mailbox yet/)
  })

  it('lists the newest first, pages with Older, and ends on the day forwarding started', async () => {
    const page1 = [1, 2, 3].map((n) =>
      summaryOf(n, n === 2 ? { attachments: 2, confirmation: true, subject: '' } : {}),
    )
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal, work], limit: 5 }))
      .on(first, ok({ messages: page1, next: summaryOf(3).id }))
      .on(`${first}&after=${summaryOf(3).id}`, ok({ messages: [summaryOf(4)] }))
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    const rows = messages().getAllByRole('listitem')
    expect(rows).toHaveLength(3)
    expect(rows[0].textContent).toContain('Subject 1')
    expect(within(rows[0]).getByRole('link').getAttribute('href')).toBe(
      `/inbox/mbx_personal/${summaryOf(1).id}`,
    )
    expect(rows[1].textContent).toContain('No subject')
    expect(rows[1].textContent).toContain('Gmail code')
    expect(within(rows[1]).getByText('attachments')).toBeTruthy()
    expect(within(rows[0]).getByText('11 Oct 2025')).toBeTruthy()
    // The horizon is the end of the mail: not shown while there is more.
    expect(screen.queryByText(/^Nothing from before/)).toBeNull()
    expect(screen.getByLabelText('Mailbox')).toBeTruthy()
    await expectAccessible()

    await user.click(screen.getByRole('button', { name: 'Older' }))
    await screen.findByText('Sender 4')
    expect(messages().getAllByRole('listitem')).toHaveLength(4)
    expect(screen.queryByRole('button', { name: 'Older' })).toBeNull()
    expect(screen.getByText(/^Nothing from before/).textContent).toBe(
      'Nothing from before 3 Nov 2026 is here: Permaily only sees mail forwarded after that.',
    )
    expectNotKept(personalAddress, 'Subject 1', 'Sender 1')
  })

  it('offers Older on a short or empty page that has more, and says when a page could not load', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [], next: 'ffffffffffffffffffffffffffffffff' }))
      .on(`${first}&after=ffffffffffffffffffffffffffffffff`, network)
    const { user } = await open('/inbox/mbx_personal')
    const older = await screen.findByRole('button', { name: 'Older' })
    expect(screen.getByText('No mail here.')).toBeTruthy()
    await user.click(older)
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    server.on(`${first}&after=ffffffffffffffffffffffffffffffff`, ok({ messages: [summaryOf(9)] }))
    await user.click(older)
    await screen.findByText('Sender 9')
    expect(screen.getByText(/^Nothing from before/)).toBeTruthy()
  })

  it('says a mailbox waits for Gmail, is paused, is gone, cannot be read, or its mail could not load', async () => {
    const waiting = { ...work, id: 'mbx_personal' }
    server
      .signedIn()
      .on(list, ok({ mailboxes: [waiting], limit: 5 }))
      .on(first, ok({ messages: [] }))
    await open('/inbox/mbx_personal')
    await screen.findByText('Waiting for Gmail.')
    expect(
      screen.getByRole('link', { name: 'Finish the forwarding set-up' }).getAttribute('href'),
    ).toBe('/accounts/mbx_personal/forwarding')
    await expectAccessible()

    server.on(
      list,
      ok({
        mailboxes: [mailboxOf({ state: 'paused', paused_at: '2026-11-10T10:00:00Z' })],
        limit: 5,
      }),
    )
    await open('/inbox/mbx_personal')
    await screen.findByText(/^Paused by Permaily: it accepts no new mail/)
    expect(screen.getByText('Paused by Permaily')).toBeTruthy()

    server.on(list, ok({ mailboxes: [], limit: 5 })).on(first, refused(404, 'not_found'))
    await open('/inbox/mbx_personal')
    await screen.findByText("That mailbox isn't there any more.", { exact: false })

    server.on(first, refused(409, 'mailbox_unreadable'))
    await open('/inbox/mbx_personal')
    await screen.findByText(/^This mailbox can't be read: its mail can't be opened/)

    server.on(list, network).on(first, network)
    const { user } = await open('/inbox/mbx_personal')
    const alert = (await screen.findByText(/^The mail couldn't load\./)).closest(
      '[role="alert"]',
    ) as HTMLElement
    server.on(first, ok({ messages: [summaryOf(1)] }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await screen.findByText('Sender 1')
  })

  it('switches mailboxes with the picker, and nothing of the last one stays', async () => {
    const receivingWork = { ...work, state: 'receiving', receiving_since: '2026-11-04T08:00:00Z' }
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal, receivingWork], limit: 5 }))
      .on(first, ok({ messages: [summaryOf(1)] }))
      .on('GET /api/v1/mailboxes/mbx_work/messages?limit=50', ok({ messages: [summaryOf(7)] }))
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    await user.selectOptions(screen.getByLabelText('Mailbox'), 'mbx_work')
    await screen.findByText('Sender 7')
    expect(current()).toBe('/inbox/mbx_work')
    expect(screen.queryByText('Sender 1')).toBeNull()
  })
})

describe('W4: searching', () => {
  it('searches with what was typed, kept in memory only, and says how far back it looked', async () => {
    const q = 'ana dinner'
    const searched = `${first}&q=ana+dinner`
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [summaryOf(1), summaryOf(2)] }))
      .on(
        searched,
        ok({ messages: [], next: summaryOf(5).id, searched_to: '2025-10-15T00:00:00Z' }),
      )
      .on(
        `${first}&after=${summaryOf(5).id}&q=ana+dinner`,
        ok({ messages: [summaryOf(6)], searched_to: '2025-09-01T00:00:00Z' }),
      )
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    await user.type(screen.getByRole('searchbox', { name: 'Search this mailbox' }), ` ${q} `)
    await user.click(screen.getByRole('button', { name: 'Search' }))
    const summary = await screen.findByText(/^Nothing found/)
    expect(summary.textContent).toBe('Nothing found. Searched mail back to 15 Oct 2025.')
    expect(screen.queryByText('Sender 1')).toBeNull()
    // Nothing found yet, and more to search: the horizon of the mailbox is not this.
    expect(screen.queryByText(/^Nothing from before/)).toBeNull()
    await expectAccessible()
    expectNotKept(q, 'ana', 'dinner')

    await user.click(screen.getByRole('button', { name: 'Search older mail' }))
    await screen.findByText('Sender 6')
    expect(screen.getByText(/^1 found/).textContent).toBe(
      '1 found. Searched mail back to 1 Sept 2025.',
    )
    expect(screen.queryByRole('button', { name: 'Search older mail' })).toBeNull()
    expectNotKept(q)

    await user.click(screen.getByRole('button', { name: 'Clear search' }))
    await screen.findByText('Sender 1')
    expect(
      (screen.getByRole('searchbox', { name: 'Search this mailbox' }) as HTMLInputElement).value,
    ).toBe('')
  })

  it('says a search it cannot run, and one too long before asking', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [summaryOf(1)] }))
      .on(`${first}&q=x`, refused(429, 'too_many_requests'))
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    const field = screen.getByRole('searchbox', { name: 'Search this mailbox' })
    // An empty search outside a search does nothing.
    await user.click(screen.getByRole('button', { name: 'Search' }))
    await user.type(field, 'q'.repeat(201))
    await user.click(screen.getByRole('button', { name: 'Search' }))
    expect(screen.getByText('A search is 1 to 200 characters.')).toBeTruthy()
    await user.clear(field)
    await user.type(field, 'x')
    await user.click(screen.getByRole('button', { name: 'Search' }))
    await screen.findByText(
      "The search didn't run. Too many attempts. Wait a few minutes, then try again.",
    )
    expect(server.calls.filter((call) => call.path.includes('q=')).length).toBe(1)
  })

  it('goes back to the list when the search is emptied', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [summaryOf(1)] }))
      .on(`${first}&q=x`, ok({ messages: [summaryOf(2)] }))
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    const field = screen.getByRole('searchbox', { name: 'Search this mailbox' })
    await user.type(field, 'x{Enter}')
    await screen.findByText('Sender 2')
    await user.clear(field)
    await user.type(field, '{Enter}')
    await screen.findByText('Sender 1')
  })
})

describe('W4: deleting', () => {
  it('deletes what is ticked, and all shown at once', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [1, 2, 3].map((n) => summaryOf(n)) }))
      .on('POST /api/v1/mailboxes/mbx_personal/messages/delete', (asked) =>
        ok({ deleted: (asked.body as { ids: string[] }).ids.length }),
      )
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    // Nothing ticked: Delete does nothing.
    await user.click(screen.getByRole('button', { name: named('Delete selected') }))
    expect(server.asked('POST /api/v1/mailboxes/mbx_personal/messages/delete')).toHaveLength(0)

    await user.click(screen.getByRole('checkbox', { name: 'Select Subject 2' }))
    await user.click(screen.getByRole('button', { name: named('Delete 1 selected') }))
    await screen.findByText('1 message deleted.')
    expect(server.asked('POST /api/v1/mailboxes/mbx_personal/messages/delete')[0].body).toEqual({
      ids: [summaryOf(2).id],
    })
    expect(messages().getAllByRole('listitem')).toHaveLength(2)

    await user.click(screen.getByRole('checkbox', { name: 'Select all shown' }))
    expect(
      (screen.getByRole('checkbox', { name: 'Select Subject 1' }) as HTMLInputElement).checked,
    ).toBe(true)
    await user.click(screen.getByRole('checkbox', { name: 'Select all shown' }))
    expect(
      (screen.getByRole('checkbox', { name: 'Select Subject 1' }) as HTMLInputElement).checked,
    ).toBe(false)
    await user.click(screen.getByRole('checkbox', { name: 'Select all shown' }))
    await user.click(screen.getByRole('button', { name: named('Delete 2 selected') }))
    await screen.findByText('2 messages deleted.')
    expect(screen.getByText('No mail here.')).toBeTruthy()
    await expectAccessible()
  })

  it('deletes at most a hundred at a time, and says when nothing was deleted', async () => {
    const many = Array.from({ length: 101 }, (_, n) => summaryOf(n + 1))
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: many.slice(0, 50), next: many[49].id }))
      .on(`${first}&after=${many[49].id}`, ok({ messages: many.slice(50, 100), next: many[99].id }))
      .on(`${first}&after=${many[99].id}`, ok({ messages: many.slice(100) }))
      .on('POST /api/v1/mailboxes/mbx_personal/messages/delete', network)
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    await user.click(screen.getByRole('button', { name: 'Older' }))
    await screen.findByText('Sender 51')
    await user.click(screen.getByRole('button', { name: 'Older' }))
    await screen.findByText('Sender 101')
    await user.click(screen.getByRole('checkbox', { name: 'Select all shown' }))
    await user.click(screen.getByRole('button', { name: named('Delete 101 selected') }))
    expect(screen.getByText('Delete at most 100 messages at a time.')).toBeTruthy()
    expect(server.asked('POST /api/v1/mailboxes/mbx_personal/messages/delete')).toHaveLength(0)
    await user.click(screen.getByRole('checkbox', { name: 'Select Subject 1' }))
    await user.click(screen.getByRole('button', { name: named('Delete 100 selected') }))
    await screen.findByText(
      "Nothing was deleted. Permaily can't be reached. Check your connection, then try again.",
    )
  })
})

describe('W4: one message', () => {
  it('shows who sent it, when, who signed it, the details, the attachments and the text, as text', async () => {
    const withAll = messageOf({
      cc: [{ name: 'Bo', address: 'bo@example.com' }],
      reply_to: [{ address: 'reply@example.com' }],
      attachments: [
        { index: 0, name: 'ticket.pdf', type: 'application/pdf', size: 248_000 },
        { index: 1, name: '', type: 'image/png', size: 900 },
      ],
      auth: [
        { method: 'dkim', domain: 'example.com', result: 'pass' },
        { method: 'dkim', domain: 'mailer.example', result: 'fail: body hash' },
      ],
      text: `Line one\n${'https://example.com/'.repeat(40)}\nEnd`,
    })
    server
      .signedIn()
      .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
      .on(messageRoute, ok(withAll))
    await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(screen.getByText('Ana Ruiz')).toBeTruthy()
    expect(screen.getByText('Sat, 11 Oct 2025, 09:30 UTC')).toBeTruthy()
    expect(screen.getByText(/^Signed by/).textContent).toBe('Signed by example.com')
    // Never that the sender is who they say.
    expect(screen.queryByText(/verified|genuine|is who/i)).toBeNull()
    const details = screen.getByText('Details').closest('details') as HTMLDetailsElement
    expect(within(details).getByText('bo@example.com')).toBeTruthy()
    expect(within(details).getByText('reply@example.com')).toBeTruthy()
    expect(within(details).getByText('Sat, 11 Oct 2025 11:30:00 +0200')).toBeTruthy()
    expect(within(details).getByText(': fail: body hash', { exact: false })).toBeTruthy()
    expect(within(details).getByText('18 KB')).toBeTruthy()
    const attachments = within(screen.getByRole('region', { name: '2 attachments' }))
    const pdf = attachments.getByRole('link', { name: named('Download ticket.pdf') })
    expect(pdf.getAttribute('href')).toBe(
      `/api/v1/mailboxes/mbx_personal/messages/${one.id}/attachments/0`,
    )
    expect(pdf.hasAttribute('download')).toBe(true)
    expect(attachments.getByText('application/pdf, 248 KB')).toBeTruthy()
    expect(attachments.getByText('Attachment 2')).toBeTruthy()
    const original = screen.getByRole('link', { name: 'Download original' })
    expect(original.getAttribute('href')).toBe(
      `/api/v1/mailboxes/mbx_personal/messages/${one.id}/raw`,
    )
    expect(original.hasAttribute('download')).toBe(true)
    const text = within(screen.getByRole('region', { name: 'Message text' }))
    // The text is text: an address in it is no link.
    expect(text.queryByRole('link')).toBeNull()
    expect(text.getByText(/^Line one/).textContent).toBe(withAll.text)
    expect(screen.getByRole('link', { name: 'Back to Personal' }).getAttribute('href')).toBe(
      '/inbox/mbx_personal',
    )
    // Nothing of the mail names the tab.
    expect(document.title).toBe('Permaily')
    await expectAccessible()
    expectNotKept('Dinner on Friday', 'ana.ruiz@example.com', 'Are we still on')
  })

  it('shows a forwarding confirmation with its code, text cut short, a message that could not be read, and one with no text', async () => {
    const confirmation = messageOf({
      id: confirmationOf().message,
      confirmation: true,
      text_truncated: true,
      from: [],
      subject: '',
      auth: [],
    })
    server
      .signedIn()
      .on(
        'GET /api/v1/mailboxes/mbx_personal',
        ok({ ...personal, confirmations: [confirmationOf()] }),
      )
      .on(`GET /api/v1/mailboxes/mbx_personal/messages/${confirmation.id}`, ok(confirmation))
    await open(`/inbox/mbx_personal/${confirmation.id}`)
    await screen.findByRole('heading', { level: 1, name: 'No subject' })
    expect(screen.getByText('no sender')).toBeTruthy()
    await screen.findByText('836104527')
    expect(
      screen.getByText('The text is cut short here. The rest is in the original.'),
    ).toBeTruthy()
    await expectAccessible()

    const older = messageOf({ id: 'a'.repeat(32), confirmation: true, text: '', unparsed: true })
    server.on(`GET /api/v1/mailboxes/mbx_personal/messages/${older.id}`, ok(older))
    await open(`/inbox/mbx_personal/${older.id}`)
    await screen.findByText(
      "Permaily couldn't read this message as mail. Download the original to open it elsewhere.",
    )
    expect(screen.getByText('There is no text to show.')).toBeTruthy()
    // Its code is no longer kept: the set-up says where it was.
    expect(screen.getByRole('link', { name: 'forwarding set-up' })).toBeTruthy()

    const empty = messageOf({ id: 'b'.repeat(32), text: '', text_from: 'none' })
    server.on(`GET /api/v1/mailboxes/mbx_personal/messages/${empty.id}`, ok(empty))
    await open(`/inbox/mbx_personal/${empty.id}`)
    await screen.findByText('This message has no text.')
  })

  it('deletes it and goes back to the list, which says so once', async () => {
    server
      .signedIn()
      .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
      .on(messageRoute, ok(one))
      .on(`DELETE /api/v1/mailboxes/mbx_personal/messages/${one.id}`, ok())
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [] }))
    const { user } = await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByText('Message deleted.')
    expect(current()).toBe('/inbox/mbx_personal')
    // A reload does not say it again.
    await open('/inbox/mbx_personal')
    await screen.findByText('No mail here.')
    expect(screen.queryByText('Message deleted.')).toBeNull()
  })

  it('says when it could not be deleted, and one deleted elsewhere is gone all the same', async () => {
    server
      .signedIn()
      .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
      .on(messageRoute, ok(one))
      .on(`DELETE /api/v1/mailboxes/mbx_personal/messages/${one.id}`, network)
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [] }))
    const { user } = await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByText(
      "It wasn't deleted. Permaily can't be reached. Check your connection, then try again.",
    )
    server.on(`DELETE /api/v1/mailboxes/mbx_personal/messages/${one.id}`, refused(404, 'not_found'))
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    await screen.findByText('Message deleted.')
  })

  it('says a message is not there any more, or could not load', async () => {
    server
      .signedIn()
      .on('GET /api/v1/mailboxes/mbx_personal', network)
      .on(messageRoute, refused(404, 'not_found'))
    await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByRole('heading', { level: 1, name: "That message isn't there any more" })
    expect(screen.getByRole('link', { name: 'Back to the inbox' })).toBeTruthy()

    server.on(messageRoute, network)
    const { user } = await open(`/inbox/mbx_personal/${one.id}`)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain("The message couldn't load.")
    server.on(messageRoute, ok(one))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
  })
})

describe('mail is hostile input', () => {
  it('shows a hostile subject, sender, filename, snippet and text as text, never as elements', async () => {
    const evil = messageOf({
      subject: hostile,
      from: [{ name: hostile, address: 'x@example.com' }],
      to: [{ name: hostile, address: personalAddress }],
      text: `${hostile}\n<a href="javascript:alert(1)">click</a>\nhttps://phish.example/login`,
      attachments: [{ index: 0, name: hostile, type: 'text/html', size: 10 }],
      date: hostile,
    })
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(
        first,
        ok({
          messages: [
            summaryOf(1, {
              subject: hostile,
              snippet: hostile,
              from: [{ name: hostile, address: 'x@example.com' }],
            }),
          ],
        }),
      )
      .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
      .on(messageRoute, ok(evil))
    await open('/inbox/mbx_personal')
    await screen.findAllByText(hostile)
    expect(document.querySelector('img')).toBeNull()
    expect(document.body.innerHTML).not.toContain('<img')
    await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByRole('heading', { level: 1, name: hostile })
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('a[href^="javascript"]')).toBeNull()
    expect(
      [...document.querySelectorAll('a')].every((a) => !a.href.includes('phish.example')),
    ).toBe(true)
    expect(screen.getAllByText(hostile, { exact: false }).length).toBeGreaterThan(3)
    expect(document.title).toBe('Permaily')
  })

  it('has no dangerouslySetInnerHTML anywhere in the app', () => {
    const found: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const path = join(dir, name)
        if (statSync(path).isDirectory()) walk(path)
        else if (
          /\.(tsx?|jsx?)$/.test(name) &&
          readFileSync(path, 'utf8').includes('dangerouslySetInnerHTML')
        )
          found.push(path)
      }
    }
    walk(resolve(process.cwd(), 'src'))
    expect(found).toEqual([])
  })
})

describe('W6: nothing of the mail in the address bar', () => {
  it('keeps the search out of the address bar, and opening a message puts only its id there', async () => {
    server
      .signedIn()
      .on(list, ok({ mailboxes: [personal], limit: 5 }))
      .on(first, ok({ messages: [summaryOf(1)] }))
      .on(
        `GET /api/v1/mailboxes/mbx_personal/messages/${summaryOf(1).id}`,
        ok(messageOf({ id: summaryOf(1).id })),
      )
      .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
    const { user } = await open('/inbox/mbx_personal')
    await user.click(await screen.findByRole('link', { name: /Sender 1/ }))
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(current()).toBe(`/inbox/mbx_personal/${summaryOf(1).id}`)
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
    await settle()
    expectNotKept('Dinner on Friday', 'Sender 1', personalAddress)
  })
})
