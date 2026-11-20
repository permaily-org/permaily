import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, expectNotKept, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { connectionOf } from '../../helpers/connections.ts'
import { personal, work } from '../../helpers/mailboxes.ts'
import {
  digest,
  entryOf,
  firstPage,
  goldenConnection,
  headDigest,
  linkOf,
  otherReceipt,
  pageBefore,
  pageOf,
  receiptOf,
} from '../../helpers/receipts.ts'
import {
  type Account,
  ana,
  later,
  meOf,
  network,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

// ADR 0041, "The activity screen": the account's record from the walk of GET /api/v1/receipts, tests E1 to E11.

/** The mailbox the golden receipts name, by the label the person gave it. */
const golden = { ...personal, id: 'mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa' }

/** Claude Code's connection as the golden receipts name it. */
const claude = connectionOf({ id: goldenConnection })

function signedIn(me: unknown = meOf(), connections: unknown[] = [claude]) {
  return server
    .on('GET /api/v1/me', ok(me))
    .on('GET /api/v1/connections', ok({ connections }))
    .on('GET /api/v1/mailboxes', ok({ mailboxes: [golden, work], limit: 5 }))
}

/** Goldens of every kind, the newest first, in one link of a page that is the whole record. */
const everyKind = ['refusals', 'ended', 'consent', 'refused', 'attachment', 'read', 'search'].map(
  (name) => receiptOf(name),
)

async function openActivity(address = '/activity') {
  const opened = await open(address)
  await screen.findByRole('heading', { level: 1, name: 'Activity' })
  return opened
}

/** The list's rows, by what they say. */
function rows(): HTMLElement[] {
  return screen.queryAllByRole('button').filter((b) => b.hasAttribute('data-row'))
}

function rowSaying(text: string | RegExp): HTMLElement {
  const found = rows().filter((r) =>
    typeof text === 'string' ? r.textContent?.includes(text) : text.test(r.textContent ?? ''),
  )
  if (found.length === 0) throw new Error(`no row says ${text}`)
  return found[0] as HTMLElement
}

/** Waits for a row that says this. */
async function findRow(text: string | RegExp): Promise<HTMLElement> {
  let found: HTMLElement | undefined
  await waitFor(() => {
    found = rowSaying(text)
  })
  return found as HTMLElement
}

/** The receipt beside the list. */
function panel() {
  return within(screen.getByRole('complementary', { name: 'Receipt' }))
}

/** The panel's heading. */
function panelHeading(): string {
  return panel().getAllByRole('heading', { level: 2 })[0]?.textContent ?? ''
}

/** Below 80rem: a receipt opens under its row. */
function narrow() {
  vi.stubGlobal('matchMedia', (media: string) => ({
    matches: media !== '(min-width: 80rem)',
    media,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
}

describe('E1: the walk, from the head and then from each next', () => {
  it('reads the head first, then the next the server gave on Show older, and stops when done', async () => {
    signedIn()
      .on(
        firstPage,
        ok(pageOf([linkOf(3, [receiptOf('search')])], { next: { n: 2, index: 4 }, done: false })),
      )
      .on(pageBefore(2, 4), ok(pageOf([linkOf(2, [receiptOf('ended')])], { done: true })))
    const { user } = await openActivity()
    await waitFor(() => expect(rows().length).toBeGreaterThan(0))
    expect(screen.queryByText('A connection ended')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Show older' }))
    await findRow('A connection ended')
    expect(
      server.calls.filter((c) => c.path.startsWith('/api/v1/receipts')).map((c) => c.path),
    ).toEqual(['/api/v1/receipts?limit=100', '/api/v1/receipts?before=2.4&limit=100'])
    expect(screen.getByText("That's the start of your record.")).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Show older' })).toBeNull()
  })

  it('refuses a page that does not move on, rather than asking for it for ever', async () => {
    signedIn()
      .on(
        firstPage,
        ok(pageOf([linkOf(3, [receiptOf('search')])], { next: { n: 2, index: 4 }, done: false })),
      )
      .on(pageBefore(2, 4), ok(pageOf([], { next: { n: 2, index: 4 }, done: false })))
    const { user } = await openActivity()
    await user.click(await screen.findByRole('button', { name: 'Show older' }))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Something went wrong on our side.')
    expect(server.asked(pageBefore(2, 4))).toHaveLength(1)
    // The rows read stay.
    expect(rowSaying('Billing')).toBeTruthy()
  })
})

describe('E2: every kind of receipt, and one this app cannot read', () => {
  it('renders a row and a panel for every golden receipt', async () => {
    signedIn().on(firstPage, ok(pageOf([linkOf(3, everyKind)])))
    const { user } = await openActivity()
    await findRow('Refused 419 requests in a minute')
    const list = screen.getByRole('main')
    // search, read and attachment: one row per message made available, and a held message a row of its own.
    expect(rows().filter((r) => r.textContent?.includes('Made available'))).toHaveLength(3)
    expect(rows().filter((r) => r.textContent?.includes('Held by a rule'))).toHaveLength(2)
    expect(within(list).getAllByText('Sender and date, Subject')).toHaveLength(1)
    expect(within(list).getByText('Sender and date, Subject, Attachment 1')).toBeTruthy()
    expect(rowSaying('A rule held the message').textContent).toContain('Refused')
    expect(rowSaying('Approved a connection request').textContent).toContain('Approved')
    expect(rowSaying('A connection ended').textContent).toContain('A new connection replaced it')
    expect(list.textContent).not.toContain('received')

    // The first row's receipt shows beside the list from 80rem, until another is picked.
    expect(panelHeading()).toBe('Requests refused in a minute')
    expect(panel().getByText('Reads: Too many requests')).toBeTruthy()

    await user.click(rows().filter((r) => r.textContent?.includes('Billing'))[2] as HTMLElement)
    expect(panelHeading()).toBe('A search')
    expect(panel().getByText('What left')).toBeTruthy()
    expect(panel().getAllByText('Personal').length).toBeGreaterThan(0)
    expect(panel().getByText('Billing <billing@example.com>')).toBeTruthy()
    expect(panel().getByText('Invoice 12 <for> "you"')).toBeTruthy()
    expect(panel().getByText('What a rule held')).toBeTruthy()
    // The mailbox of the second held message is not one the account has now.
    expect(panel().getByText('A mailbox that was disconnected')).toBeTruthy()
    expect(panel().getByText('By the model')).toBeTruthy()
    expect(panel().getByText('By exact words')).toBeTruthy()
    const model = panel().getByRole('region', { name: 'A model decided' })
    expect(model.textContent).toContain('subject is about “invoices”0.91gpt-6-luna, p2c')
    expect(panel().queryByText(/characters/)).toBeNull()

    await user.click(rowSaying('Approved a connection request'))
    expect(panel().getByText('Mail whose subject contains "invoice".')).toBeTruthy()
    expect(panel().getByText('Who sent it and when, the subject and the text')).toBeTruthy()
    expect(panel().getByText('2026-11-15')).toBeTruthy()

    await user.click(rowSaying('A rule held the message'))
    expect(panel().getByText('Why it was refused')).toBeTruthy()
    expect(panel().getByText('default-sign-in-and-security v1')).toBeTruthy()

    await user.click(rowSaying('A connection ended'))
    expect(panel().getByText('A new connection replaced it.')).toBeTruthy()
    await expectAccessible()
  })

  it('lists a receipt it cannot read with its identifier and position, never dropped and never throwing', async () => {
    const shapes = [
      receiptOf('search', { id: 'rcp_v3', v: 3 }),
      receiptOf('search', { id: 'rcp_kind', kind: 'webhook' }),
      receiptOf('consent', { id: 'rcp_null', consent: null }),
      receiptOf('refused', { id: 'rcp_array', refused: [] }),
      receiptOf('search', { id: 'rcp_list', delivered: {} }),
      receiptOf('search', {
        id: 'rcp_nested',
        delivered: [{ ...(receiptOf('search').delivered as object[])[0], received: 7 }],
      }),
      // The review's reproduction: a held message without its rule.
      receiptOf('search', {
        id: 'rcp_rule',
        held: [{ mailbox: 'mbx_x', message: 'm', why: 'exact' }],
      }),
    ]
    signedIn().on(firstPage, ok(pageOf([linkOf(9, shapes)])))
    const { user } = await openActivity()
    await waitFor(() => expect(rows()).toHaveLength(shapes.length))
    for (const s of shapes)
      expect(rowSaying(s.id as string).textContent).toContain("This app can't show this receipt")
    await user.click(rowSaying('rcp_nested'))
    expect(panel().getByText('receipt 9.1')).toBeTruthy()
    await user.click(panel().getByRole('button', { name: 'Technical record' }))
    expect(panel().getByText('rcp_nested')).toBeTruthy()
    await expectAccessible()
  })

  it('says a renewal whose write then lost was an approved request, never that it renewed', async () => {
    const renewal = receiptOf('consent', {
      consent: { ...(receiptOf('consent').consent as object), renewal: true, change: undefined },
    })
    signedIn().on(firstPage, ok(pageOf([linkOf(3, [renewal])])))
    await openActivity()
    await screen.findAllByText('Approved a renewal request')
    expect(rowSaying('Approved a renewal request')).toBeTruthy()
    expect(panelHeading()).toBe('Approved a renewal request')
    expect(screen.getByRole('main').textContent).not.toMatch(/renewed/i)
    expect(panel().getByRole('button', { name: 'About this approval' })).toBeTruthy()
    expect(screen.getByRole('complementary', { name: 'Receipt' }).textContent).toContain(
      'Permaily records it before it makes the change, so a change that then failed still shows here.',
    )
  })

  it('names what a device added and a refused request were', async () => {
    const consent = receiptOf('consent').consent as object
    signedIn().on(
      firstPage,
      ok(
        pageOf([
          linkOf(3, [
            receiptOf('consent', { id: 'rcp_a', consent: { ...consent, change: 'add_device' } }),
            receiptOf('consent', { id: 'rcp_b', consent: { ...consent, allowed: false } }),
            receiptOf('ended', { id: 'rcp_c', ended: { reason: 'reuse' } }),
          ]),
        ]),
      ),
    )
    await openActivity()
    await screen.findAllByText('Approved adding a device')
    expect(rowSaying('Approved adding a device')).toBeTruthy()
    expect(rowSaying('Refused a request').textContent).toContain('Refused')
    expect(rowSaying('A device ended').textContent).toContain('Permaily ended it')
  })
})

describe('E3: what the walk found, in words', () => {
  it('says every verdict of a link and of a receipt, and how a receipt was linked', async () => {
    const search = receiptOf('search')
    signedIn().on(
      firstPage,
      ok(
        pageOf([
          linkOf(1234, [], { verdict: 'lost', kind: undefined, receipts: [] }),
          linkOf(1233, [], { verdict: 'forked', receipts: [] }),
          linkOf(1232, [], { verdict: 'unreadable', receipts: [] }),
          {
            ...linkOf(1231, []),
            verdict: 'prev',
            receipts: [
              entryOf(3, receiptOf('ended', { id: 'rcp_gone' }), 'missing'),
              entryOf(2, receiptOf('ended', { id: 'rcp_changed' }), 'changed'),
              entryOf(1, receiptOf('ended', { id: 'rcp_other' }), 'unreadable'),
            ],
          },
          linkOf(1230, [receiptOf('ended', { id: 'rcp_late' })], { kind: 'recovered' }),
          linkOf(1229, [receiptOf('refused', { id: 'rcp_before' })], { kind: 'adopted' }),
          linkOf(1228, [search], { verdict: 'expired', break: 'lost' }),
        ]),
      ),
    )
    const { user } = await openActivity()
    await findRow("Link 1,234 is missing from the storage: the receipts it named can't be listed.")
    const main = screen.getByRole('main')
    for (const words of [
      'Link 1,233 was written twice: the storage holds two versions of it.',
      "Link 1,232 can't be read: something else is stored in its place.",
      "Link 1,231 doesn't match the link after it.",
      'Receipt 1,231.3 was deleted from the storage.',
      "Receipt 1,231.2 was changed in the storage: it isn't what was recorded.",
      "Receipt 1,231.1 can't be read: something else is stored in its place.",
      "Link 1,228: the link before this one couldn't be read when this one was written.",
    ])
      expect(main.textContent).toContain(words)
    expect(rowSaying('A connection ended').textContent).toContain('Linked late')
    expect(rowSaying('A rule held the message').textContent).toContain('From before')
    // An expired link reads as kept: its receipts are rows as any, and no row says it.
    expect(rowSaying('Billing').textContent).not.toMatch(/expired|lock/i)
    await user.click(rowSaying('Billing'))
    await user.click(panel().getByRole('button', { name: 'Technical record' }))
    expect(panel().getByText('Link 1,228 is kept as it was written.')).toBeTruthy()
    expect(panel().getByRole('button', { name: 'About this link' })).toBeTruthy()
    expect(panel().getByText(`sha256 ${digest('e0')}`)).toBeTruthy()
    expect(panel().getByText(`sha256 ${digest('p1228')}`)).toBeTruthy()
    await user.click(rowSaying('A connection ended'))
    await user.click(panel().getByRole('button', { name: 'Technical record' }))
    expect(screen.getByRole('complementary', { name: 'Receipt' }).textContent).toContain(
      "This receipt was found without a link by Permaily's daily check, and linked then.",
    )
    await expectAccessible()
  })
})

describe('E4: filters', () => {
  it('keeps the rows of the connection in the address, and every break and unreadable receipt', async () => {
    signedIn(meOf(), [
      claude,
      connectionOf({ id: 'con_other', vendor: { domain: 'trips.example' } }),
    ]).on(
      firstPage,
      ok(
        pageOf([
          linkOf(5, [
            otherReceipt('rcp_o1'),
            receiptOf('search'),
            receiptOf('search', { id: 'rcp_x', v: 9 }),
          ]),
          linkOf(4, [], { verdict: 'lost', receipts: [] }),
        ]),
      ),
    )
    const { user } = await openActivity(`/activity?connection=${goldenConnection}`)
    await waitFor(() => expect(rowSaying('Made available')).toBeTruthy())
    expect((screen.getByLabelText('Connection') as HTMLSelectElement).value).toBe(goldenConnection)
    expect(screen.queryByText('A connection ended')).toBeNull()
    expect(rowSaying('rcp_x')).toBeTruthy()
    expect(rowSaying('Link 4 is missing')).toBeTruthy()

    // By kind, in memory only: approvals and ends.
    await user.selectOptions(screen.getByLabelText('Show'), 'decided')
    expect(rows().some((r) => r.textContent?.includes('Made available'))).toBe(false)
    expect(rowSaying('rcp_x')).toBeTruthy()
    expect(current()).toBe(`/activity?connection=${goldenConnection}`)

    await user.selectOptions(screen.getByLabelText('Connection'), '')
    expect(current()).toBe('/activity')
    expect(await findRow('A connection ended')).toBeTruthy()
    await user.selectOptions(screen.getByLabelText('Connection'), 'con_other')
    expect(current()).toBe('/activity?connection=con_other')
  })

  it('reads at most five pages a press with a filter, and says how far it looked', async () => {
    const page = (n: number) =>
      ok(
        pageOf([linkOf(n, [otherReceipt(`rcp_${n}`)])], {
          next: { n: n - 1, index: 0 },
          done: false,
          head: { n: 20, verdict: 'ok', sha256: headDigest },
        }),
      )
    signedIn().on(firstPage, page(20))
    for (let n = 19; n > 5; n--) server.on(pageBefore(n, 0), page(n))
    const { user } = await openActivity(`/activity?connection=${goldenConnection}`)
    await screen.findByText('No receipt of this connection in the newest 5.')
    expect(server.calls.filter((c) => c.path.startsWith('/api/v1/receipts'))).toHaveLength(5)
    await user.click(screen.getByRole('button', { name: 'Look further' }))
    await screen.findByText('No receipt of this connection in the newest 10.')
    expect(server.calls.filter((c) => c.path.startsWith('/api/v1/receipts'))).toHaveLength(10)
  })

  it('stops a press once 50 rows match', async () => {
    const many = (n: number) =>
      Array.from({ length: 30 }, (_, i) => receiptOf('ended', { id: `rcp_${n}_${i}` }))
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(9, many(9))], { next: { n: 8, index: 0 }, done: false })))
      .on(
        pageBefore(8, 0),
        ok(pageOf([linkOf(8, many(8))], { next: { n: 7, index: 0 }, done: false })),
      )
    await openActivity('/activity?connection=con_cccccccccccccccccccccccccc')
    await waitFor(() => expect(rows()).toHaveLength(60))
    await settle()
    expect(server.calls.filter((c) => c.path.startsWith('/api/v1/receipts'))).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Show older' })).toBeTruthy()
  })

  it('says a short page with more looked through its links, and offers to look further', async () => {
    signedIn()
      .on(
        firstPage,
        ok(pageOf([linkOf(7, []), linkOf(6, [])], { next: { n: 5, index: 0 }, done: false })),
      )
      .on(pageBefore(5, 0), ok(pageOf([linkOf(5, [receiptOf('ended')])])))
    const { user } = await openActivity()
    await screen.findByText('Looked through 2 links.')
    await user.click(screen.getByRole('button', { name: 'Look further' }))
    await findRow('A connection ended')
  })
})

describe('E5: the head', () => {
  it('shows its number and digest, and Copy writes what permaily-verify takes', async () => {
    signedIn().on(
      firstPage,
      ok(
        pageOf([linkOf(1234, [receiptOf('ended')])], {
          head: { n: 1234, verdict: 'ok', sha256: headDigest },
        }),
      ),
    )
    const { user } = await openActivity()
    await screen.findByText('Your record has 1,234 links.')
    expect(screen.getByText(/^Newest: link 1,234,/).textContent).toBe(
      'Newest: link 1,234, sha256 3f2a…9c1d',
    )
    const writeText = vi.spyOn(navigator.clipboard, 'writeText')
    await user.click(screen.getByRole('button', { name: named('Copy the newest link') }))
    expect(writeText).toHaveBeenCalledWith(`1234:${headDigest}`)
    expect(await screen.findByText('Copied.')).toBeTruthy()
  })

  it('says when copying failed', async () => {
    signedIn().on(firstPage, ok(pageOf([linkOf(3, [receiptOf('ended')])])))
    const { user } = await openActivity()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    await user.click(await screen.findByRole('button', { name: named('Copy the newest link') }))
    expect(await screen.findByText("Couldn't copy. Select it and copy it yourself.")).toBeTruthy()
  })

  // Review of PR #195, finding 3: the words said to select the head, but only its shortened digest was on the page.
  it('shows the whole newest link to select when copying failed', async () => {
    signedIn().on(
      firstPage,
      ok(
        pageOf([linkOf(1234, [receiptOf('ended')])], {
          head: { n: 1234, verdict: 'ok', sha256: headDigest },
        }),
      ),
    )
    const { user } = await openActivity()
    await screen.findByText('Your record has 1,234 links.')
    expect(screen.queryByLabelText('The newest link')).toBeNull()
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValue(new Error('denied'))
    await user.click(screen.getByRole('button', { name: named('Copy the newest link') }))
    const field = (await screen.findByLabelText('The newest link')) as HTMLInputElement
    expect(field.value).toBe(`1234:${headDigest}`)
    expect(field.readOnly).toBe(true)
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(field.selectionStart).toBe(0)
    expect(field.selectionEnd).toBe(field.value.length)
    await expectAccessible()
  })

  it('offers nothing to note of a head that is not kept', async () => {
    signedIn().on(
      firstPage,
      ok(
        pageOf([linkOf(1234, [], { verdict: 'forked', receipts: [] })], {
          head: { n: 1234, verdict: 'forked' },
        }),
      ),
    )
    await openActivity()
    await screen.findByText('Your record has 1,234 links.')
    expect(
      screen.getAllByText('Link 1,234 was written twice: the storage holds two versions of it.')
        .length,
    ).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: named('Copy the newest link') })).toBeNull()
    expect(screen.queryByText(/^Newest:/)).toBeNull()
  })
})

describe('E6: states', () => {
  it('is busy while it reads, then shows the record', async () => {
    const waiting = later()
    signedIn().on(firstPage, waiting.answer)
    await openActivity()
    expect(
      (await screen.findByText('Reading your record…'))
        .closest('[aria-busy]')
        ?.getAttribute('aria-busy'),
    ).toBe('true')
    await expectAccessible()
    waiting.give(ok(pageOf([linkOf(3, [receiptOf('ended')])])))
    await findRow('A connection ended')
    expect(screen.queryByText('Reading your record…')).toBeNull()
  })

  it('says when nothing has left, and leads to Connections', async () => {
    signedIn().on(firstPage, ok(pageOf([], { head: { n: 0 } })))
    await openActivity()
    expect(await screen.findByText('Nothing has left your mailboxes.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Connections' }).getAttribute('href')).toBe(
      '/connections',
    )
    expect(screen.queryByLabelText('Connection')).toBeNull()
    await expectAccessible()
  })

  it('says a record that cannot be read in its fixed words, with nothing to try again', async () => {
    signedIn().on(firstPage, refused(500, 'receipts_unreadable'))
    await openActivity()
    expect((await screen.findByRole('alert')).textContent).toContain(
      "Your receipts can't be read now: what Permaily stores for them was changed by someone else.",
    )
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    await expectAccessible()
  })

  it('says a busy record, and tries again', async () => {
    signedIn().on(
      firstPage,
      once(refused(503, 'busy'), ok(pageOf([linkOf(3, [receiptOf('ended')])]))),
    )
    const { user } = await openActivity()
    expect((await screen.findByRole('alert')).textContent).toContain(
      'Your record is busy. Try again in a moment.',
    )
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await findRow('A connection ended')
  })

  it('keeps the rows shown when there is no answer, and goes on from where it was', async () => {
    signedIn()
      .on(
        firstPage,
        ok(pageOf([linkOf(3, [receiptOf('search')])], { next: { n: 2, index: 0 }, done: false })),
      )
      .on(pageBefore(2, 0), once(network, ok(pageOf([linkOf(2, [receiptOf('ended')])]))))
    const { user } = await openActivity()
    await user.click(await screen.findByRole('button', { name: 'Show older' }))
    expect((await screen.findByRole('alert')).textContent).toContain("Permaily can't be reached.")
    expect(rowSaying('Billing')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await findRow('A connection ended')
    expect(server.asked(pageBefore(2, 0))).toHaveLength(2)
  })

  it('starts again from the head when the position is one the chain no longer has', async () => {
    signedIn()
      .on(
        firstPage,
        once(
          ok(pageOf([linkOf(3, [receiptOf('search')])], { next: { n: 2, index: 0 }, done: false })),
          ok(pageOf([linkOf(4, [receiptOf('ended')])], { done: true })),
        ),
      )
      .on(pageBefore(2, 0), refused(400, 'invalid_before'))
    const { user } = await openActivity()
    await user.click(await screen.findByRole('button', { name: 'Show older' }))
    await findRow('A connection ended')
    expect(screen.queryByText('Billing')).toBeNull()
    expect(
      screen.getByText('Your record changed while you read it, so it shows from the newest again.'),
    ).toBeTruthy()
    expect(server.asked(firstPage)).toHaveLength(2)
  })
})

describe('E7: what mail wrote stays text, and nothing of a receipt is kept', () => {
  it('puts senders, subjects and topics in bdi, and nothing in the title, the address or storage', async () => {
    const hostile = receiptOf('search', {
      delivered: [
        {
          ...(receiptOf('search').delivered as object[])[0],
          from: [{ name: '<img src=x onerror=alert(1)>', address: 'evil@example.net' }],
          subject: '=HYPERLINK("http://evil.example")',
        },
      ],
    })
    signedIn().on(firstPage, ok(pageOf([linkOf(3, [hostile])])))
    const { user } = await openActivity()
    const sender = await screen.findByText('<img src=x onerror=alert(1)>')
    expect(sender.tagName).toBe('BDI')
    expect(document.querySelector('img')).toBeNull()
    for (const subject of screen.getAllByText('=HYPERLINK("http://evil.example")'))
      expect(subject.tagName).toBe('BDI')
    const topic = panel().getAllByText('“invoices”')[0]
    expect(topic?.tagName).toBe('BDI')
    await user.click(rows()[0] as HTMLElement)
    expect(document.title).toBe('Permaily')
    expect(current()).toBe('/activity')
    expectNotKept('evil@example.net', 'HYPERLINK', 'rcp_', 'invoices', 'Billing')
  })
})

describe('E8: the search in words', () => {
  it('asks the check route with the receipt’s formula only when the person presses', async () => {
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
      .on(
        'POST /api/v1/queries/check',
        ok({
          formula: {},
          id: 'qry_x',
          model: true,
          sentence: {
            lang: 'en',
            text: 'Mail about invoices',
            lead: [],
            lines: [{ parts: [{ text: 'Mail about invoices' }] }],
          },
        }),
      )
    const { user } = await openActivity()
    await waitFor(() => expect(rowSaying('Made available')).toBeTruthy())
    await settle()
    expect(server.asked('POST /api/v1/queries/check')).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'Show the search in words' }))
    expect(await screen.findByText('Mail about invoices')).toBeTruthy()
    expect(server.asked('POST /api/v1/queries/check')[0]?.body).toEqual({
      formula: (receiptOf('search').formulas as { formula?: unknown }[])[3]?.formula,
    })
  })

  it('shows a formula the route refuses as its canonical JSON', async () => {
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
      .on('POST /api/v1/queries/check', refused(400, 'invalid_formula'))
    const { user } = await openActivity()
    await user.click(await screen.findByRole('button', { name: 'Show the search in words' }))
    expect((await screen.findByText(/"op":"about"/)).textContent).toBe(
      '{"v":1,"all":[{"any":[{"var":"subject","op":"about","value":"invoices"}]}]}',
    )
    await expectAccessible()
  })

  it('says why the route could not be asked, and asks again', async () => {
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
      .on('POST /api/v1/queries/check', network)
    const { user } = await openActivity()
    await user.click(await screen.findByRole('button', { name: 'Show the search in words' }))
    expect((await screen.findByRole('alert')).textContent).toContain("Permaily can't be reached.")
    expect(screen.getByRole('button', { name: 'Show the search in words' })).toBeTruthy()
  })
})

describe('E9: accessibility', () => {
  it('keeps focus on Show older after it loads, and says what it loaded', async () => {
    signedIn()
      .on(
        firstPage,
        ok(pageOf([linkOf(3, [receiptOf('ended')])], { next: { n: 2, index: 0 }, done: false })),
      )
      .on(
        pageBefore(2, 0),
        ok(
          pageOf([linkOf(2, [receiptOf('ended', { id: 'rcp_2' })])], {
            next: { n: 1, index: 0 },
            done: false,
          }),
        ),
      )
      .on(pageBefore(1, 0), ok(pageOf([linkOf(1, [])])))
    const { user } = await openActivity()
    const more = await screen.findByRole('button', { name: 'Show older' })
    await user.click(more)
    await waitFor(() => expect(rows()).toHaveLength(2))
    await waitFor(() => expect(document.activeElement).toBe(more))
    expect(screen.getByText('Showed 1 more receipt').getAttribute('role')).toBe('status')
    await expectAccessible()
    await user.click(more)
    await waitFor(() =>
      expect(document.activeElement?.textContent).toBe("That's the start of your record."),
    )
    expect(screen.getByText('No more')).toBeTruthy()
  })

  it('opens a row under it below 80rem, and picks it beside the list from there', async () => {
    narrow()
    signedIn().on(firstPage, ok(pageOf([linkOf(3, [receiptOf('ended'), receiptOf('refused')])])))
    const { user } = await openActivity()
    const row = await findRow('A connection ended')
    expect(row.getAttribute('aria-expanded')).toBe('false')
    expect(row.hasAttribute('aria-pressed')).toBe(false)
    await user.click(row)
    expect(row.getAttribute('aria-expanded')).toBe('true')
    const region = screen.getByRole('region', { name: 'Receipt' })
    expect(row.getAttribute('aria-controls')).toBe(region.id)
    await expectAccessible()
    await user.click(row)
    expect(row.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByRole('region', { name: 'Receipt' })).toBeNull()
  })

  it('marks the row whose receipt is beside the list as pressed', async () => {
    signedIn().on(firstPage, ok(pageOf([linkOf(3, [receiptOf('ended'), receiptOf('refused')])])))
    const { user } = await openActivity()
    await findRow('A connection ended')
    const [first, second] = rows() as [HTMLElement, HTMLElement]
    expect(first.getAttribute('aria-pressed')).toBe('true')
    await user.click(second)
    expect(second.getAttribute('aria-pressed')).toBe('true')
    expect(first.getAttribute('aria-pressed')).toBe('false')
    fireEvent.click(second)
    expect(second.getAttribute('aria-pressed')).toBe('true')
  })
})

describe('E10: reachable while closing and under terms not agreed', () => {
  const closing: Account = {
    ...ana,
    state: 'closing',
    closes_at: '2026-11-18T14:32:00Z',
    closing_by: 'person',
  }

  it('shows the record in the limited shell while the account is closing', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-12T10:00:00Z') })
    server
      .on('GET /api/v1/me', ok(meOf(closing)))
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal], limit: 5 }))
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
    await openActivity()
    expect(screen.getByRole('region', { name: 'Your account is being deleted' })).toBeTruthy()
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
    const nav = within(screen.getByRole('navigation', { name: 'Mailboxes' }))
    expect(nav.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/activity')
    await waitFor(() => expect(rowSaying('Made available')).toBeTruthy())
    // Its connections ended with it: they are not asked for, and nothing links to them.
    expect(server.asked('GET /api/v1/connections')).toHaveLength(0)
    expect(panel().queryByRole('link')).toBeNull()
    await expectAccessible()
  })

  it('shows the record in the limited shell under terms not agreed', async () => {
    signedIn(meOf(ana, {}, '2027-01-01')).on(
      firstPage,
      ok(pageOf([linkOf(3, [receiptOf('search')])])),
    )
    await openActivity()
    expect(screen.getByRole('region', { name: 'The terms changed' })).toBeTruthy()
    await waitFor(() => expect(rowSaying('Made available')).toBeTruthy())
    expect(panel().getByRole('link', { name: 'Claude Code' }).getAttribute('href')).toBe(
      `/connections/claude.ai/${goldenConnection}`,
    )
    await expectAccessible()
  })
})

describe('E11: a connection’s activity', () => {
  it('links from a connection’s page to its activity', async () => {
    signedIn()
      .on(`GET /api/v1/connections/${goldenConnection}`, ok(claude))
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('ended')])])))
    const { user } = await open(`/connections/claude.ai/${goldenConnection}`)
    const link = await screen.findByRole('link', { name: named('Activity of Claude Code') })
    expect(link.getAttribute('href')).toBe(`/activity?connection=${goldenConnection}`)
    await user.click(link)
    await screen.findByRole('heading', { level: 1, name: 'Activity' })
    expect((screen.getByLabelText('Connection') as HTMLSelectElement).value).toBe(goldenConnection)
  })

  it('names a connection no longer listed by its vendor’s domain', async () => {
    signedIn().on(
      firstPage,
      ok(pageOf([linkOf(3, [otherReceipt('rcp_gone', 'con_gone', 'gone.example')])])),
    )
    await openActivity('/activity?connection=con_gone')
    await findRow('A connection ended')
    const select = screen.getByLabelText('Connection') as HTMLSelectElement
    expect(select.value).toBe('con_gone')
    expect(select.selectedOptions[0]?.textContent).toBe('gone.example, no longer listed')
    expect(rowSaying('A connection ended').textContent).toContain('gone.example')
    expect(panel().getByText('A connection no longer listed')).toBeTruthy()
  })
})

/** A search receipt of another connection, whose search is another formula, made available from Trips. */
function tripsSearch() {
  const search = receiptOf('search')
  return receiptOf('search', {
    id: 'rcp_trips',
    connection: 'con_other',
    vendor: 'trips.example',
    formulas: [
      {
        role: 'search',
        id: 'qry_trips',
        formula: { v: 1, all: [{ any: [{ var: 'subject', op: 'about', value: 'trips' }] }] },
      },
    ],
    delivered: [
      {
        ...(search.delivered as object[])[0],
        from: [{ name: 'Trips', address: 'trips@example.com' }],
      },
    ],
    held: [],
  })
}

/** The check route's answer: one sentence, of the search the formula is about. */
function sentenceOf(text: string) {
  return ok({
    formula: {},
    id: 'qry_x',
    model: true,
    sentence: { lang: 'en', text, lead: [], lines: [{ parts: [{ text }] }] },
  })
}

/** What the check route was asked about: the value of the formula's one condition. */
function topicOf(body: unknown): unknown {
  return (body as { formula: { all: { any: { value: unknown }[] }[] } }).formula.all[0]?.any[0]
    ?.value
}

// Review of PR #195, finding 1: the search in words is of one receipt. Another receipt never shows it, nor its answer.
describe('E8: the search in words belongs to its receipt', () => {
  function twoSearches(check: Parameters<typeof server.on>[1]) {
    return signedIn(meOf(), [
      claude,
      connectionOf({ id: 'con_other', vendor: { domain: 'trips.example' } }),
    ])
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search'), tripsSearch()])])))
      .on('POST /api/v1/queries/check', check)
  }

  it('does not show the words of one search under another receipt picked after it', async () => {
    twoSearches(({ body }) =>
      sentenceOf(topicOf(body) === 'invoices' ? 'Search A only' : 'Search B only'),
    )
    const { user } = await openActivity()
    await findRow('Trips')
    await user.click(panel().getByRole('button', { name: 'Show the search in words' }))
    await panel().findByText('Search A only')
    await user.click(rowSaying('Trips'))
    expect(panel().getByText('Trips <trips@example.com>')).toBeTruthy()
    expect(panel().queryByText('Search A only')).toBeNull()
    await user.click(panel().getByRole('button', { name: 'Show the search in words' }))
    expect(await panel().findByText('Search B only')).toBeTruthy()
    expect(server.asked('POST /api/v1/queries/check').map((c) => topicOf(c.body))).toEqual([
      'invoices',
      'trips',
    ])
  })

  it('drops the answer to a search asked before another receipt was picked', async () => {
    const waiting = later()
    twoSearches(waiting.answer)
    const { user } = await openActivity()
    await findRow('Trips')
    await user.click(panel().getByRole('button', { name: 'Show the search in words' }))
    await user.click(rowSaying('Trips'))
    waiting.give(sentenceOf('Search A only'))
    await settle()
    expect(panel().queryByText('Search A only')).toBeNull()
    const ask = panel().getByRole('button', { name: 'Show the search in words' })
    expect(ask.getAttribute('aria-disabled')).not.toBe('true')
  })

  it('does not carry the words to the receipt a filter shows instead', async () => {
    twoSearches(sentenceOf('Search A only'))
    const { user } = await openActivity()
    await findRow('Trips')
    await user.click(panel().getByRole('button', { name: 'Show the search in words' }))
    await panel().findByText('Search A only')
    await user.selectOptions(screen.getByLabelText('Connection'), 'con_other')
    await waitFor(() => expect(panel().getByText('Trips <trips@example.com>')).toBeTruthy())
    expect(panel().queryByText('Search A only')).toBeNull()
    expect(panel().getByRole('button', { name: 'Show the search in words' })).toBeTruthy()
  })

  it('asks once while it waits, however often it is pressed', async () => {
    const waiting = later()
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
      .on('POST /api/v1/queries/check', waiting.answer)
    const { user } = await openActivity()
    await findRow('Made available')
    const ask = panel().getByRole('button', { name: 'Show the search in words' })
    await user.click(ask)
    await user.click(ask)
    expect(ask.getAttribute('aria-disabled')).toBe('true')
    waiting.give(sentenceOf('Mail about invoices'))
    expect(await panel().findByText('Mail about invoices')).toBeTruthy()
    expect(server.asked('POST /api/v1/queries/check')).toHaveLength(1)
  })

  it('says nothing when the session ended while it asked', async () => {
    signedIn()
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
      .on('POST /api/v1/queries/check', refused(401, 'unauthorized'))
    const { user } = await openActivity()
    await findRow('Made available')
    const ask = panel().getByRole('button', { name: 'Show the search in words' })
    server.signedOut()
    await user.click(ask)
    await waitFor(() => expect(current()).toBe('/sign-in'))
    await settle()
    expect(screen.queryByText(/Something went wrong/)).toBeNull()
  })
})

describe('E2: the panel names every kind of row', () => {
  it('names a message read, an attachment read, a break and a receipt deleted', async () => {
    signedIn().on(
      firstPage,
      ok(
        pageOf([
          linkOf(5, [receiptOf('read', { id: 'rcp_read' })]),
          linkOf(4, [receiptOf('attachment', { id: 'rcp_att' })]),
          {
            ...linkOf(3, []),
            receipts: [entryOf(0, receiptOf('ended', { id: 'rcp_gone' }), 'missing')],
          },
          linkOf(2, [receiptOf('ended')], { break: 'lost' }),
        ]),
      ),
    )
    const { user } = await openActivity()
    await findRow('Receipt 3.0 was deleted from the storage.')
    const titles: string[] = []
    for (const key of ['5.0:d0', '4.0:d0', '3.0', '2:break']) {
      const row = rows().find((r) => r.dataset.row === key)
      if (!row) throw new Error(`no row ${key}`)
      await user.click(row)
      titles.push(panelHeading())
    }
    expect(titles).toEqual([
      'A message read',
      'An attachment read',
      'Deleted',
      'A break in the record',
    ])
  })

  it('lists a search that matched nothing, and keeps it among what was made available', async () => {
    signedIn().on(
      firstPage,
      ok(pageOf([linkOf(3, [receiptOf('search', { delivered: [], held: [] }), receiptOf('ended')])])),
    )
    const { user } = await openActivity()
    await findRow('Searched, nothing matched')
    await user.selectOptions(screen.getByLabelText('Show'), 'made')
    expect(rows()).toHaveLength(1)
    expect(rowSaying('Searched, nothing matched')).toBeTruthy()
  })
})

describe('E6: what the screen shows without the connections and mailboxes', () => {
  it('shows the record when the connections and mailboxes cannot be read, naming no mailbox', async () => {
    server
      .on('GET /api/v1/me', ok(meOf()))
      .on('GET /api/v1/connections', refused(500, 'internal'))
      .on('GET /api/v1/mailboxes', refused(500, 'internal'))
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
    await openActivity()
    await findRow('Made available')
    await settle()
    expect(panel().getAllByText('A mailbox').length).toBeGreaterThan(0)
    expect(panel().getByText('A connection no longer listed')).toBeTruthy()
    expect(panel().queryByRole('link')).toBeNull()
  })
})
