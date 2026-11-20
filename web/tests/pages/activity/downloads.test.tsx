import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type * as api from '../../../src/lib/api.ts'
import { CSV_HEADER, csvLinesOf, localDay, utf8Bytes } from '../../../src/lib/csv.ts'
import { Downloads } from '../../../src/pages/activity/Downloads.tsx'
import { expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { connectionOf } from '../../helpers/connections.ts'
import { personal } from '../../helpers/mailboxes.ts'
import {
  entryOf,
  firstPage,
  goldenConnection,
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
  okText,
  refused,
  server,
} from '../../helpers/server.ts'

// ADR 0041, "The two downloads, in parts" and "Accessibility": the downloads' component (E16's screen half), both
// downloads on the Activity screen, and in the limited shell (E10's downloads).

/** The files the browser was given to save, in order: each one's name and text. */
function watchSaves() {
  const blobs: Blob[] = []
  const names: string[] = []
  vi.spyOn(URL, 'createObjectURL').mockImplementation((blob) => {
    blobs.push(blob as Blob)
    return `blob:here/${blobs.length}`
  })
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    names.push(this.download)
  })
  return {
    names,
    async texts(): Promise<string[]> {
      const decoder = new TextDecoder('utf-8', { ignoreBOM: true })
      return Promise.all(blobs.map(async (b) => decoder.decode(await b.arrayBuffer())))
    },
  }
}

const today = () => localDay()

/** The played export of web/tests/receipts: six pages, about 1,220 bytes each. */
const played = readFileSync(join(import.meta.dirname, '../../receipts/export.jsonl'), 'utf8')
const playedPages = played.split(/(?<=\n)/)
const routeOf = (i: number) => {
  if (i === 0) return 'GET /api/v1/receipts/export'
  const { next } = JSON.parse(playedPages[i - 1] as string)
  return `GET /api/v1/receipts/export?before=${next.n}.${next.index}`
}

function playExport() {
  playedPages.forEach((line, i) => {
    server.on(routeOf(i), okText(line))
  })
}

/** Two pages of the export a part. */
const twoPages = 2600

/** The component alone, signed in, with a part's limit the tests choose. */
async function openDownloads(
  partBytes = twoPages,
  connections: ReadonlyMap<string, api.Connection> = new Map(),
) {
  server.on('GET /api/v1/me', ok(meOf()))
  const { SessionProvider } = await import('../../../src/lib/session.tsx')
  const view = render(
    <SessionProvider>
      <Downloads connections={connections} partBytes={partBytes} />
    </SessionProvider>,
  )
  await settle()
  return { ...view, user: userEvent.setup() }
}

const checking = () => screen.getByRole('button', { name: 'Download for checking' })
const spreadsheet = () => screen.getByRole('button', { name: 'Download as a spreadsheet' })
const status = () =>
  within(screen.getByRole('region', { name: 'Download your record' })).getAllByRole('status')[0]

describe('E16: the downloads, on the screen', () => {
  it('saves a record in parts, says so as it goes, and names the list of parts at the end', async () => {
    const saves = watchSaves()
    playExport()
    const held = later()
    server.on(routeOf(4), held.answer)
    const { user } = await openDownloads()
    await user.click(checking())
    // Part 1 (two pages) is saved, part 2 holds the next two, and the fifth page is being read.
    await waitFor(() =>
      expect(status()?.textContent).toBe(
        'Downloading for checking: 8 receipts read, 1 part saved.',
      ),
    )
    expect(
      screen.getByText(/Your browser may ask whether Permaily can save several files/),
    ).toBeTruthy()
    expect(checking().getAttribute('aria-disabled')).toBe('true')
    // A second press while it reads asks nothing more.
    await user.click(spreadsheet())
    expect(server.asked(firstPage)).toHaveLength(0)
    await expectAccessible()
    held.give(okText(playedPages[4] as string))
    const manifest = `permaily-receipts-${today()}-jsonl-manifest.json`
    await waitFor(() =>
      expect(status()?.textContent).toBe(`Saved 3 parts and the list of them, ${manifest}.`),
    )
    expect(saves.names).toEqual([
      `permaily-receipts-${today()}-part-001.jsonl`,
      `permaily-receipts-${today()}-part-002.jsonl`,
      `permaily-receipts-${today()}-part-003.jsonl`,
      manifest,
    ])
    const texts = await saves.texts()
    expect(texts.slice(0, 3).join('')).toBe(played)
    expect(JSON.parse(texts[3] as string).complete).toBe(true)
    expect(screen.queryByRole('button', { name: /Cancel/ })).toBeNull()
    expect(checking().getAttribute('aria-disabled')).toBe('false')
    await expectAccessible()
  })

  it('says a set stopped before its manifest is incomplete, and goes on from the next part', async () => {
    const saves = watchSaves()
    playExport()
    server.on(routeOf(4), refused(503, 'busy'))
    const { user } = await openDownloads()
    await user.click(checking())
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain(
      'Saved 1 part. The download stopped before the end: these files are not your whole record.',
    )
    expect(alert.textContent).toContain('Your record is busy. Try again in a moment.')
    expect(saves.names).toHaveLength(1)
    await expectAccessible()

    server.on(routeOf(4), okText(playedPages[4] as string))
    await user.click(within(alert).getByRole('button', { name: 'Continue' }))
    await waitFor(() => expect(status()?.textContent).toMatch(/^Saved 3 parts and the list/))
    // Nothing of part 1 was saved again: parts 2 and 3 and the manifest, which names all three.
    expect(saves.names.slice(1)).toEqual([
      `permaily-receipts-${today()}-part-002.jsonl`,
      `permaily-receipts-${today()}-part-003.jsonl`,
      `permaily-receipts-${today()}-jsonl-manifest.json`,
    ])
    const texts = await saves.texts()
    expect(texts.slice(0, 3).join('')).toBe(played)
    expect(JSON.parse(texts[3] as string).parts).toHaveLength(3)
    expect(server.asked(routeOf(0))).toHaveLength(1)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('starts again from the newest when asked, and moves focus to Cancel', async () => {
    const saves = watchSaves()
    playExport()
    server.on(routeOf(3), network)
    const { user } = await openDownloads()
    await user.click(checking())
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Saved 1 part.')
    expect(alert.textContent).toContain("Permaily can't be reached.")
    const held = later()
    server.on(routeOf(0), held.answer)
    await user.click(within(alert).getByRole('button', { name: 'Start again' }))
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('button', { name: /Cancel/ })),
    )
    held.give(okText(playedPages[0] as string))
    server.on(routeOf(3), okText(playedPages[3] as string))
    await waitFor(() => expect(status()?.textContent).toMatch(/^Saved 3 parts/))
    expect(saves.names[0]).toBe(saves.names[1])
    expect(server.asked(routeOf(0))).toHaveLength(2)
  })

  it('says nothing was saved when it stops before a file, and tries again', async () => {
    const saves = watchSaves()
    server.on(firstPage, refused(503, 'busy'))
    const { user } = await openDownloads()
    await user.click(spreadsheet())
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe(
      'The download stopped. Nothing was saved.Your record is busy. Try again in a moment.Try again',
    )
    expect(saves.names).toEqual([])
    server.on(firstPage, ok(pageOf([linkOf(3, [receiptOf('ended')])])))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await waitFor(() =>
      expect(status()?.textContent).toBe(`Saved permaily-receipts-${today()}.csv.`),
    )
    const [csv] = await saves.texts()
    expect(csv?.startsWith(CSV_HEADER)).toBe(true)
    expect(csv).toContain('"ended"')
  })

  it('offers no retry when the record can’t be read', async () => {
    server.on(routeOf(0), refused(503, 'receipts_unreadable'))
    const { user } = await openDownloads()
    await user.click(checking())
    const alert = await screen.findByRole('alert')
    expect(within(alert).queryByRole('button')).toBeNull()
  })

  it('says nothing more of why when the session ended', async () => {
    server.on(routeOf(0), refused(401, 'unauthorized'))
    const { user } = await openDownloads()
    await user.click(checking())
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toBe('The download stopped. Nothing was saved.Try again')
  })

  it('cancels at the next page: nothing of the part being read, no manifest, focus back on its button', async () => {
    const saves = watchSaves()
    playExport()
    const held = later()
    server.on(routeOf(1), held.answer)
    const { user } = await openDownloads()
    await user.click(checking())
    const cancel = await screen.findByRole('button', { name: /Cancel/ })
    await user.click(cancel)
    expect(document.activeElement).toBe(checking())
    held.give(okText(playedPages[1] as string))
    await waitFor(() =>
      expect(status()?.textContent).toBe('Download cancelled. Nothing was saved.'),
    )
    expect(saves.names).toEqual([])
    expect(screen.queryByRole('alert')).toBeNull()
    expect(server.asked(routeOf(2))).toHaveLength(0)
    await expectAccessible()
  })

  it('a cancel after a part says the set is incomplete', async () => {
    watchSaves()
    playExport()
    const held = later()
    server.on(routeOf(3), held.answer)
    const { user } = await openDownloads()
    await user.click(checking())
    await waitFor(() => expect(status()?.textContent).toMatch(/1 part saved/))
    await user.click(screen.getByRole('button', { name: /Cancel/ }))
    held.give(okText(playedPages[3] as string))
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Saved 1 part. The download stopped before the end')
    expect(within(alert).getByRole('button', { name: 'Continue' })).toBeTruthy()
  })

  it('stops reading when the page is left', async () => {
    const saves = watchSaves()
    playExport()
    const held = later()
    server.on(routeOf(1), held.answer)
    const { user } = await openDownloads()
    await user.click(checking())
    await screen.findByRole('button', { name: /Cancel/ })
    cleanup()
    await act(async () => {
      held.give(okText(playedPages[1] as string))
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(saves.names).toEqual([])
    expect(server.asked(routeOf(2))).toHaveLength(0)
  })

  it('names the owner a listed connection verified, by its identifier or its domain, and no other', async () => {
    const saves = watchSaves()
    const claude = connectionOf({ id: goldenConnection })
    const elsewhere = connectionOf({ id: 'con_elsewhere', vendor: { domain: 'mail.example' } })
    server.on(
      firstPage,
      ok(
        pageOf([
          linkOf(3, [
            receiptOf('ended'),
            receiptOf('ended', { connection: 'con_gone' }),
            otherReceipt(`rcp_${'1'.repeat(36)}`, 'con_elsewhere', 'other.example'),
          ]),
        ]),
      ),
    )
    const { user } = await openDownloads(
      undefined,
      new Map<string, api.Connection>([
        [goldenConnection, claude as unknown as api.Connection],
        ['con_elsewhere', elsewhere as unknown as api.Connection],
      ]),
    )
    await user.click(spreadsheet())
    await waitFor(() => expect(saves.names).toHaveLength(1))
    const [csv] = await saves.texts()
    const verified = (csv as string)
      .split('\r\n')
      .slice(1, -1)
      .map((line) => line.split('","')[7])
    expect(verified).toEqual(['Anthropic', 'Anthropic', ''])
  })
})

// The review of pull request 198, on the screen.
describe('PR 198 review', () => {
  it('finding 1: says a set can’t go on when the record changed where it stopped, and offers Start again', async () => {
    const saves = watchSaves()
    const search = receiptOf('search')
    const ended = receiptOf('ended')
    const kept = linkOf(3, [search, ended])
    const lost = { ...kept, receipts: [entryOf(1, search, 'missing'), entryOf(0, ended)] }
    const first = (link: unknown) => pageOf([link], { done: false, next: { n: 2, index: 0 } })
    const rows = csvLinesOf(first(kept) as unknown as api.ReceiptsPage)
    server.on(firstPage, ok(first(kept)))
    server.on(pageBefore(2, 0), network)
    const { user } = await openDownloads(
      utf8Bytes(CSV_HEADER) + utf8Bytes(rows.slice(0, 2).join('')),
    )
    await user.click(spreadsheet())
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Saved 1 part.')

    server.on(firstPage, ok(first(lost)))
    server.on(pageBefore(2, 0), ok(pageOf([linkOf(2, [receiptOf('read')])])))
    await user.click(within(alert).getByRole('button', { name: 'Continue' }))
    await waitFor(() =>
      expect(screen.getByRole('alert').textContent).toContain(
        'Your record changed where the download stopped, so it can’t go on from there. Start again to get your whole record.',
      ),
    )
    const after = screen.getByRole('alert')
    expect(after.textContent).toContain('Saved 1 part. The download stopped before the end')
    expect(within(after).queryByRole('button', { name: 'Continue' })).toBeNull()
    expect(within(after).getByRole('button', { name: 'Start again' })).toBeTruthy()
    expect(saves.names).toHaveLength(1)
    await expectAccessible()
  })

  it('finding 2: leaving the page while a file is hashed saves nothing', async () => {
    const saves = watchSaves()
    playExport()
    const real = crypto.subtle.digest.bind(crypto.subtle)
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let hashing: () => void = () => {}
    const started = new Promise<void>((resolve) => {
      hashing = resolve
    })
    let hashed: Promise<ArrayBuffer> = Promise.resolve(new ArrayBuffer(0))
    vi.spyOn(crypto.subtle, 'digest').mockImplementation((algorithm, data) => {
      hashing()
      hashed = gate.then(() => real(algorithm, data))
      return hashed
    })
    const { user } = await openDownloads(1_000_000)
    await user.click(checking())
    await started
    cleanup()
    await act(async () => {
      release()
      await hashed
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
    expect(saves.names).toEqual([])
  })
})

describe('both downloads on the Activity screen', () => {
  const claude = connectionOf({ id: goldenConnection })

  function signedIn(me: unknown = meOf()) {
    return server
      .on('GET /api/v1/me', ok(me))
      .on('GET /api/v1/connections', ok({ connections: [claude] }))
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal], limit: 5 }))
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
  }

  async function bothWork() {
    const saves = watchSaves()
    playExport()
    await open('/activity')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Download for checking' }))
    await waitFor(() =>
      expect(status()?.textContent).toBe(`Saved permaily-receipts-${today()}.jsonl.`),
    )
    await user.click(spreadsheet())
    await waitFor(() =>
      expect(status()?.textContent).toBe(`Saved permaily-receipts-${today()}.csv.`),
    )
    const [json, csv] = await saves.texts()
    expect(json).toBe(played)
    expect(csv?.startsWith(CSV_HEADER)).toBe(true)
    expect(csv).toContain('"made available"')
    // Nothing of the record is kept anywhere it could be read back from.
    expectNotKept('Invoice 12', 'billing@example.com', 'rcp_')
    await expectAccessible()
    return csv as string
  }

  it('saves the record for checking and as a spreadsheet', async () => {
    signedIn()
    const csv = await bothWork()
    expect(csv).toContain('"Anthropic"')
  })

  it('both work in the limited shell while the account is closing (E10)', async () => {
    const closing: Account = {
      ...ana,
      state: 'closing',
      closes_at: '2026-11-18T14:32:00Z',
      closing_by: 'person',
    }
    server
      .on('GET /api/v1/me', ok(meOf(closing)))
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal], limit: 5 }))
      .on(firstPage, ok(pageOf([linkOf(3, [receiptOf('search')])])))
    await bothWork()
    expect(screen.getByRole('region', { name: 'Your account is being deleted' })).toBeTruthy()
    expect(server.asked('GET /api/v1/connections')).toHaveLength(0)
  })

  it('both work in the limited shell under terms not agreed (E10)', async () => {
    signedIn(meOf(ana, {}, '2027-01-01'))
    await bothWork()
    expect(screen.getByRole('region', { name: 'The terms changed' })).toBeTruthy()
  })

  it('offers no download of an empty record', async () => {
    signedIn().on(firstPage, ok(pageOf([], { head: { n: 0 } })))
    await open('/activity')
    await screen.findByText('Nothing has left your mailboxes.')
    expect(screen.queryByRole('button', { name: 'Download for checking' })).toBeNull()
  })
})
