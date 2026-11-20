import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { afterEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../src/lib/api.ts'
import {
  CSV_BOM,
  CSV_COLUMNS,
  CSV_HEADER,
  csvLinesOf,
  csvRow,
  csvRowsOf,
  csvSource,
  type ExportState,
  ExportStopped,
  exportManifestName,
  exportName,
  jsonSource,
  localDay,
  type Source,
  type SourcePage,
  saveFile,
  utf8Bytes,
  writeExport,
} from '../../src/lib/csv.ts'
import { digest, entryOf, headDigest, linkOf, pageOf, receiptOf } from '../helpers/receipts.ts'
import { okText, server } from '../helpers/server.ts'

// ADR 0041, "The CSV export" and "The two downloads, in parts": the rows from the walk (E15), the parts and their
// manifest (E16), the names (E17), and the download for checking byte for byte (E18's web half; its Go half,
// web/tests/receipts, runs permaily-verify on the files this test compares with).

const column = (name: (typeof CSV_COLUMNS)[number]) => CSV_COLUMNS.indexOf(name)

/** The rows of a page, each as an object of its columns. */
function rowsOf(page: unknown, verified?: (r: api.Receipt) => string | undefined) {
  return csvRowsOf(page as api.ReceiptsPage, verified).map((cells) => {
    expect(cells).toHaveLength(CSV_COLUMNS.length)
    return Object.fromEntries(CSV_COLUMNS.map((c, i) => [c, cells[i]])) as Record<
      (typeof CSV_COLUMNS)[number],
      string
    >
  })
}

/** A page of the walk as `receipts()` gives it: every link's receipts a list. */
function walk(links: unknown[], options: Parameters<typeof pageOf>[1] = {}): api.ReceiptsPage {
  const page = pageOf(links, options) as unknown as api.ReceiptsPage
  return {
    ...page,
    links: page.links.map((l) => ({ ...l, receipts: l.receipts ?? [] })),
  }
}

const everyKind = ['refusals', 'ended', 'consent', 'refused', 'attachment', 'read', 'search'].map(
  (name) => receiptOf(name),
)

describe('E15: the rows of the CSV, from the walk', () => {
  it('writes a row per message made available or held, refusal, approval, end and count', () => {
    const rows = rowsOf(walk([linkOf(3, everyKind)]))
    expect(rows.map((r) => [r.kind, r.outcome])).toEqual([
      ['refusals', 'refused'],
      ['refusals', 'refused'],
      ['ended', 'ended'],
      ['consent', 'approved request'],
      ['refused', 'refused'],
      ['attachment', 'made available'],
      ['read', 'made available'],
      ['search', 'made available'],
      ['search', 'held'],
      ['search', 'held'],
    ])
    for (const r of rows) {
      expect(r.record).toBe('kept')
      expect(r.receipt).toMatch(/^rcp_/)
      expect(r.position).toMatch(/^3\.\d$/)
      expect(r.vendor).toBe('claude.ai')
      expect(r.through).toBe('mcp')
      expect(r.time).toBe('2026-11-16T10:00:00.123456Z')
      expect(r.sha256).toMatch(/^[0-9a-f]{64}$/)
    }
    const [counted, other] = rows
    expect([counted?.reason, counted?.count, other?.reason, other?.count]).toEqual([
      'read: rate_limited',
      '412',
      'search: invalid_body',
      '7',
    ])
    expect(rows[2]?.reason).toBe('replaced')
    expect(rows[3]?.parts).toBe('sender subject body')
    const refused = rows[4]
    expect([refused?.reason, refused?.mailbox, refused?.rule]).toEqual([
      'held',
      'mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa',
      'default-sign-in-and-security v1',
    ])
    expect(refused?.model).toContain('message about "a sign-in, login or verification code')
    expect(rows[5]?.attachment).toBe('1')
    const made = rows[7]
    expect(made?.from).toBe('Billing <billing@example.com>')
    expect(made?.subject).toBe('Invoice 12 <for> "you"')
    expect(made?.parts).toBe('sender subject')
    expect(made?.received).toBe('2026-11-14T10:00:00.123456Z')
    expect(made?.rule).toBe('default-sign-in-and-security v1')
    expect(made?.model.split('; ')[0]).toBe('subject about "invoices": 0.91 (gpt-6-luna, p2c)')
    expect(JSON.parse(made?.search ?? '')).toEqual(
      (everyKind[6]?.formulas as { role: string; formula?: unknown }[]).find(
        (f) => f.role === 'search',
      )?.formula,
    )
    const held = rows[8]
    expect([held?.reason, held?.rule, held?.message]).toEqual([
      'decided',
      'default-sign-in-and-security v1',
      '0000018a0b0c0d0f2122232425262728',
    ])
    // A read has no search of its own.
    expect(rows[6]?.search).toBe('')
  })

  it('says an approval is the decision recorded, a losing renewal’s too, never the change made', () => {
    const consent = (over: Record<string, unknown>) => {
      const doc = receiptOf('consent')
      return { ...doc, consent: { ...(doc.consent as object), ...over } }
    }
    // A renewal whose write then lost leaves this receipt: the same as one that won.
    const outcomes = rowsOf(
      walk([
        linkOf(3, [
          consent({ change: 'new' }),
          consent({ change: 'add_device' }),
          consent({ renewal: true, change: undefined }),
          consent({ allowed: false }),
        ]),
      ]),
    ).map((r) => r.outcome)
    expect(outcomes).toEqual([
      'approved request',
      'approved adding a device',
      'approved renewal request',
      'refused request',
    ])
  })

  it('writes a row per break: a link, a receipt not kept, a link’s break, and how a link was made', () => {
    const doc = receiptOf('ended')
    const rows = rowsOf(
      walk([
        { n: 9, verdict: 'lost', at: '2026-11-16T10:00:01+01:00' },
        { n: 8, verdict: 'forked' },
        { n: 7, verdict: 'unreadable' },
        linkOf(6, [doc], { verdict: 'prev' }),
        {
          ...linkOf(5, []),
          receipts: [
            entryOf(2, doc, 'missing'),
            entryOf(1, doc, 'changed'),
            entryOf(0, doc, 'unreadable'),
          ],
        },
        linkOf(4, [doc], { kind: 'recovered' }),
        linkOf(3, [doc], { kind: 'adopted', break: 'lost' }),
        linkOf(2, [doc], { verdict: 'expired' }),
      ]),
    )
    expect(rows.map((r) => [r.position, r.record])).toEqual([
      ['9', 'link missing'],
      ['8', 'link written twice'],
      ['7', "link can't be read"],
      ['6', "link doesn't match"],
      ['6.0', 'kept'],
      ['5.2', 'deleted from the storage'],
      ['5.1', 'changed in the storage'],
      ['5.0', "can't be read"],
      ['4.0', 'linked late'],
      ['3.0', 'from before'],
      ['3', "link before can't be read"],
      ['2.0', 'kept'],
    ])
    // A time with an offset is written in UTC; a link with none has no time.
    expect(rows[0]?.time).toBe('2026-11-16T09:00:01Z')
    expect(rows[1]?.time).toBe('')
    expect(rows[10]?.reason).toBe('lost')
    // A receipt not found as recorded has its identifier and digest, and nothing it would have said.
    expect(rows[5]?.receipt).toBe(doc.id)
    expect(rows[5]?.kind).toBe('')
  })

  it('lists a receipt this app can’t read as a row, never dropped and never a failed export', () => {
    const shapes = [
      receiptOf('search', { v: 3 }),
      receiptOf('search', { kind: 'rules' }),
      receiptOf('consent', { consent: null }),
      receiptOf('search', { delivered: [{ mailbox: 'mbx_x' }] }),
      receiptOf('refused', { refused: [] }),
      'not a receipt',
    ]
    const rows = rowsOf(walk([linkOf(3, shapes as Record<string, unknown>[])]))
    expect(rows).toHaveLength(shapes.length)
    for (const r of rows) {
      expect(r.record).toBe("this app can't read it")
      expect(r.position).toMatch(/^3\.\d$/)
      expect(r.outcome).toBe('')
    }
  })

  it('says a search that found nothing, a count of none, and a time it can’t read as it was', () => {
    const rows = rowsOf(
      walk([
        linkOf(3, [
          receiptOf('search', { delivered: [], held: null, at: 'yesterday' }),
          receiptOf('refusals', {
            refusals: { from: 'a', until: 'b', recorded: 30, counts: null },
          }),
          receiptOf('ended', { ended: undefined }),
        ]),
      ]),
    )
    expect(rows.map((r) => [r.outcome, r.count, r.reason, r.time])).toEqual([
      ['nothing matched', '', '', 'yesterday'],
      ['refused', '0', '', '2026-11-16T10:00:00.123456Z'],
      // An ended receipt without its part is one this app can't read.
      ['', '', '', ''],
    ])
  })

  it('names the owner the image verified only when the caller knows it', () => {
    const doc = receiptOf('ended')
    const [none] = rowsOf(walk([linkOf(3, [doc])]))
    expect(none?.verified).toBe('')
    const [named] = rowsOf(walk([linkOf(3, [doc])]), (r) =>
      r.vendor === 'claude.ai' ? 'Anthropic' : undefined,
    )
    expect(named?.verified).toBe('Anthropic')
  })

  it('never writes a cell that starts a formula or holds a control, whatever mail and vendors wrote', () => {
    const doc = receiptOf('search')
    const delivered = (doc.delivered as Record<string, unknown>[])[0] as Record<string, unknown>
    delivered.subject = '=HYPERLINK("https://example.com")\t\r\n'
    delivered.from = [{ name: '@SUM(A1)', address: '+1@example.com' }]
    const lines = csvLinesOf(walk([linkOf(3, [{ ...doc, vendor: '-evil.example' }])]))
    for (const line of lines) {
      expect(line.endsWith('\r\n')).toBe(true)
      const body = line.slice(0, -2)
      expect(/[\u0000-\u001f]/.test(body)).toBe(false)
      for (const cell of body.slice(1, -1).split('","'))
        expect(/^[\s​-‍⁠﻿�]*[=+\-@|%]/.test(cell), cell).toBe(false)
    }
    expect(lines[0]).toContain(`"'=HYPERLINK(""https://example.com"")���"`)
    expect(lines[0]).toContain(`"'@SUM(A1) <+1@example.com>"`)
    expect(lines[0]).toContain(`"'-evil.example"`)
  })

  it('heads every file with the byte order mark and the columns', () => {
    expect(CSV_HEADER).toBe(CSV_BOM + csvRow(CSV_COLUMNS))
    expect(CSV_HEADER.startsWith('﻿"receipt","position","record",')).toBe(true)
  })
})

/** What a download saved, in order: each file's name and bytes. */
function saver() {
  const files: { name: string; blob: Blob }[] = []
  return {
    files,
    save: (name: string, blob: Blob) => {
      files.push({ name, blob })
    },
    async bytes() {
      return Promise.all(
        files.map(async (f) => ({
          name: f.name,
          bytes: new Uint8Array(await f.blob.arrayBuffer()),
        })),
      )
    },
  }
}

// The byte order mark is part of a file: kept when decoded.
const decoder = new TextDecoder('utf-8', { ignoreBOM: true })

async function sha256Of(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const d = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
  return Array.from(d, (b) => b.toString(16).padStart(2, '0')).join('')
}

const keptHead = { n: 9, verdict: 'ok' as const, sha256: headDigest }

/** A source of pages of these lines, each page's next strictly before where it began. */
function sourceOf(
  pages: string[][],
  options: { head?: api.ReceiptsHead; fail?: Map<number, unknown> } = {},
) {
  const asked: (api.ReceiptsPosition | undefined)[] = []
  const source: Source = async (before) => {
    asked.push(before)
    const i = before ? pages.length - before.n + 1 : 0
    const failure = options.fail?.get(i)
    if (failure !== undefined) {
      options.fail?.delete(i)
      throw failure
    }
    const page: SourcePage = {
      lines: pages[i] as string[],
      head: options.head ?? keptHead,
      next: i === pages.length - 1 ? { n: 0, index: 0 } : { n: pages.length - i, index: 0 },
      done: i === pages.length - 1,
      receipts: (pages[i] as string[]).length,
    }
    return page
  }
  return { source, asked }
}

const day = '2026-11-20'

/** Lines of `n` bytes each, numbered, ending in a line feed. */
function lines(count: number, size: number, from = 0): string[] {
  return Array.from({ length: count }, (_, i) => {
    const label = `line ${from + i} `
    return `${label}${'x'.repeat(size - label.length - 1)}\n`
  })
}

async function stopped(promise: Promise<unknown>): Promise<ExportStopped> {
  try {
    await promise
  } catch (error) {
    expect(error).toBeInstanceOf(ExportStopped)
    return error as ExportStopped
  }
  throw new Error('the download did not stop')
}

describe('E16: a download in parts, each saved whole, then its manifest', () => {
  it('is one file while the record fits the limit, with no manifest', async () => {
    const { save, bytes } = saver()
    const pages = [lines(3, 100), lines(2, 100, 3)]
    const done = await writeExport({
      ext: 'jsonl',
      source: sourceOf(pages).source,
      save,
      day,
      partBytes: 500,
    })
    const files = await bytes()
    expect(files.map((f) => f.name)).toEqual(['permaily-receipts-2026-11-20.jsonl'])
    expect(decoder.decode(files[0]?.bytes)).toBe(pages.flat().join(''))
    expect(done.manifest).toBeNull()
    expect(done.files).toEqual([
      {
        name: 'permaily-receipts-2026-11-20.jsonl',
        bytes: 500,
        sha256: await sha256Of(files[0]?.bytes as Uint8Array<ArrayBuffer>),
      },
    ])
  })

  it('past the limit, closes each part before the line that would pass it, and writes the manifest last', async () => {
    const { save, bytes } = saver()
    const pages = [lines(3, 100), lines(4, 100, 3), lines(1, 100, 7)]
    const progress: unknown[] = []
    const done = await writeExport({
      ext: 'csv',
      source: sourceOf(pages).source,
      save,
      day,
      partBytes: utf8Bytes(CSV_HEADER) + 250,
      onProgress: (p) => progress.push(p),
    })
    const files = await bytes()
    expect(files.map((f) => f.name)).toEqual([
      exportName(day, 'csv', 1),
      exportName(day, 'csv', 2),
      exportName(day, 'csv', 3),
      exportName(day, 'csv', 4),
      exportManifestName(day, 'csv'),
    ])
    // Every part is a complete file: the header, then whole lines, two of them, as 250 bytes hold.
    const parts = files.slice(0, -1).map((f) => decoder.decode(f.bytes))
    for (const part of parts) {
      expect(part.startsWith(CSV_HEADER)).toBe(true)
      expect(utf8Bytes(part)).toBeLessThanOrEqual(utf8Bytes(CSV_HEADER) + 250)
    }
    expect(parts.map((p) => p.slice(CSV_HEADER.length)).join('')).toBe(pages.flat().join(''))
    expect(parts.map((p) => p.slice(CSV_HEADER.length).split('\n').length - 1)).toEqual([
      2, 2, 2, 2,
    ])
    const manifest = JSON.parse(decoder.decode(files.at(-1)?.bytes))
    expect(manifest).toEqual({
      complete: true,
      file: 'csv',
      date: day,
      head: `9:${headDigest}`,
      parts: await Promise.all(
        files.slice(0, -1).map(async (f) => ({
          name: f.name,
          bytes: f.bytes.length,
          sha256: await sha256Of(f.bytes as Uint8Array<ArrayBuffer>),
        })),
      ),
    })
    expect(done).toEqual({ files: manifest.parts, manifest: exportManifestName(day, 'csv') })
    expect(progress.at(-1)).toEqual({ receipts: 8, parts: 3 })
    expect(progress).toContainEqual({ receipts: 3, parts: 1 })
  })

  it('counts a part in UTF-8, so rows of multibyte subjects split where their bytes say', async () => {
    const { save, bytes } = saver()
    // 300 characters, 900 bytes: by their length eight would fit a part of 3,000 bytes; by their bytes three do.
    const row = `${'請求書'.repeat(100)}\n`
    expect(row.length).toBe(301)
    expect(utf8Bytes(row)).toBe(901)
    await writeExport({
      ext: 'jsonl',
      source: sourceOf([Array(8).fill(row)]).source,
      save,
      day,
      partBytes: 3000,
    })
    const files = await bytes()
    expect(files.slice(0, -1).map((f) => f.bytes.length)).toEqual([2703, 2703, 1802])
  })

  it('saves a part only whole: a page that fails, a cancel, or a page that does not move on saves nothing of it', async () => {
    const pages = [lines(2, 100), lines(2, 100, 2), lines(2, 100, 4)]
    // A failure with nothing saved yet: no file at all.
    for (const [fail, code] of [
      [new api.ApiError('busy'), 'busy'],
      [new Error('not the API'), 'other'],
    ] as const) {
      const { save, files } = saver()
      const err = await stopped(
        writeExport({
          ext: 'csv',
          source: sourceOf(pages, { fail: new Map([[1, fail]]) }).source,
          save,
          day,
          partBytes: 10_000,
        }),
      )
      expect(err.code).toBe(code)
      expect(files).toEqual([])
      expect(err.state.parts).toEqual([])
    }

    // A page that does not move on.
    const { save, files } = saver()
    const still: Source = async (before) => ({
      lines: ['a\n'],
      head: keptHead,
      next: before ?? { n: 5, index: 0 },
      done: false,
      receipts: 1,
    })
    const err = await stopped(writeExport({ ext: 'jsonl', source: still, save, day }))
    expect(err.code).toBe('other')
    expect(files).toEqual([])

    // A cancel, before anything or once a page is read.
    const controller = new AbortController()
    controller.abort()
    const early = saver()
    expect(
      (
        await stopped(
          writeExport({
            ext: 'csv',
            source: sourceOf(pages).source,
            save: early.save,
            day,
            signal: controller.signal,
          }),
        )
      ).code,
    ).toBe('cancelled')
    expect(early.files).toEqual([])

    const late = new AbortController()
    const { source } = sourceOf([lines(3, 100), ...pages])
    const cancelling: Source = async (before) => {
      const page = await source(before)
      if (before) late.abort()
      return page
    }
    const cancelled = saver()
    const err2 = await stopped(
      writeExport({
        ext: 'jsonl',
        source: cancelling,
        save: cancelled.save,
        day,
        signal: late.signal,
        partBytes: 250,
      }),
    )
    expect(err2.code).toBe('cancelled')
    // Part 1 (the first page) was saved before; nothing of part 2, and no manifest.
    expect(cancelled.files.map((f) => f.name)).toEqual([exportName(day, 'jsonl', 1)])
    expect(err2.state.parts.map((p) => p.name)).toEqual([exportName(day, 'jsonl', 1)])
  })

  it('refuses a line no part could hold, and a file the browser would not take', async () => {
    const big = saver()
    const err = await stopped(
      writeExport({
        ext: 'jsonl',
        source: sourceOf([lines(1, 300)]).source,
        save: big.save,
        day,
        partBytes: 200,
      }),
    )
    expect(err.code).toBe('other')
    expect(big.files).toEqual([])

    const refusing = await stopped(
      writeExport({
        ext: 'csv',
        source: sourceOf([lines(1, 10)]).source,
        save: () => {
          throw new Error('quota')
        },
        day,
      }),
    )
    expect(refusing.code).toBe('other')
    expect(refusing.state.parts).toEqual([])
  })

  it('goes on from the next part after a stop, holding nothing of an earlier part, to the same files', async () => {
    const pages = [lines(3, 100), lines(3, 100, 3), lines(3, 100, 6), lines(1, 100, 9)]
    const options = { ext: 'csv' as const, day, partBytes: utf8Bytes(CSV_HEADER) + 250 }

    const whole = saver()
    await writeExport({ ...options, source: sourceOf(pages).source, save: whole.save })
    const wanted = await whole.bytes()

    // The third page fails once: parts 1 to 3 saved (the second page split between parts 2 and 3).
    const first = saver()
    const { source, asked } = sourceOf(pages, { fail: new Map([[2, new api.ApiError('network')]]) })
    const err = await stopped(writeExport({ ...options, source, save: first.save }))
    expect(err.code).toBe('network')
    const state: ExportState = err.state
    expect(state.parts.map((p) => p.name)).toEqual([1, 2].map((n) => exportName(day, 'csv', n)))
    // Part 3 begins on the second page, after the first of its lines, which part 2 holds.
    expect([state.from, state.skip, state.receipts]).toEqual([{ n: 4, index: 0 }, 1, 3])
    expect(state.head).toBe(`9:${headDigest}`)

    const second = saver()
    const progress: unknown[] = []
    const done = await writeExport({
      ...options,
      source,
      save: second.save,
      resume: state,
      onProgress: (p) => progress.push(p),
    })
    // It read again from the page part 3 begins on, and saved only what was not saved.
    expect(asked.slice(-3)).toEqual([
      { n: 4, index: 0 },
      { n: 3, index: 0 },
      { n: 2, index: 0 },
    ])
    expect(progress[0]).toEqual({ receipts: 6, parts: 2 })
    const got = [...(await first.bytes()), ...(await second.bytes())]
    expect(got.map((f) => f.name)).toEqual(wanted.map((f) => f.name))
    expect(got.map((f) => decoder.decode(f.bytes))).toEqual(
      wanted.map((f) => decoder.decode(f.bytes)),
    )
    expect(done.files).toHaveLength(5)
    // The stopped state was not changed by going on.
    expect(state.parts).toHaveLength(2)
  })

  it('notes no head when the head is not one to note', async () => {
    const { save, files } = saver()
    await writeExport({
      ext: 'jsonl',
      source: sourceOf([lines(2, 100), lines(2, 100)], { head: { n: 9, verdict: 'lost' } }).source,
      save,
      day,
      partBytes: 250,
    })
    const manifest = JSON.parse(await (files.at(-1)?.blob as Blob).text())
    expect(manifest.head).toBeNull()
  })
})

describe('E17: the downloads’ names, from the person’s local day', () => {
  it('writes the local day the download began', () => {
    expect(localDay(new Date(2026, 10, 20, 23, 59))).toBe('2026-11-20')
    expect(localDay(new Date(2027, 0, 5, 0, 1))).toBe('2027-01-05')
    expect(localDay()).toMatch(/^\d{4}-\d{2}-\d{2}$/)
  })

  it('saves a file through a same-origin download, naming nobody', async () => {
    const created = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:here/1')
    const revoked = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const clicked: HTMLAnchorElement[] = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      clicked.push(this)
    })
    const file = new Blob(['x'])
    saveFile('permaily-receipts-2026-11-20.csv', file)
    expect(created).toHaveBeenCalledWith(file)
    expect(clicked).toHaveLength(1)
    expect(clicked[0]?.download).toBe('permaily-receipts-2026-11-20.csv')
    expect(clicked[0]?.getAttribute('href')).toBe('blob:here/1')
    expect(clicked[0]?.isConnected).toBe(false)
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(revoked).toHaveBeenCalledWith('blob:here/1')
  })
})

// The played export of web/tests/receipts (export.jsonl, written by its Go test), as the server's pages.
const here = join(import.meta.dirname, '../receipts')
const played = readFileSync(join(here, 'export.jsonl'), 'utf8')

function playExport() {
  const pages = played.split(/(?<=\n)/)
  let before = ''
  for (const line of pages) {
    server.on(`GET /api/v1/receipts/export${before}`, okText(line))
    const { next } = JSON.parse(line)
    before = `?before=${next.n}.${next.index}`
  }
  return pages
}

describe('E18: the download for checking holds the export’s pages byte for byte', () => {
  afterEach(() => {
    server.reset()
  })

  it('saves the pages the server wrote, untouched, as one file', async () => {
    const pages = playExport()
    expect(pages.length).toBeGreaterThan(3)
    const { save, bytes } = saver()
    await writeExport({ ext: 'jsonl', source: jsonSource, save, day })
    const [file, ...rest] = await bytes()
    expect(rest).toEqual([])
    expect(file?.name).toBe('permaily-receipts-2026-11-20.jsonl')
    expect(decoder.decode(file?.bytes)).toBe(played)
  })

  it('saves it in parts that joined in order are that file: the files web/tests/receipts checks', async () => {
    playExport()
    const { save, bytes } = saver()
    // Two pages a part.
    await writeExport({ ext: 'jsonl', source: jsonSource, save, day, partBytes: 2600 })
    const files = await bytes()
    expect(files.length).toBeGreaterThan(2)
    const parts = files.slice(0, -1)
    expect(parts.map((f) => decoder.decode(f.bytes)).join('')).toBe(played)
    const dir = join(here, 'parts')
    if (process.env.UPDATE_EXPORT_PARTS) {
      rmSync(dir, { recursive: true, force: true })
      mkdirSync(dir)
      for (const f of files) writeFileSync(join(dir, f.name), f.bytes)
    }
    expect(readdirSync(dir).sort()).toEqual(files.map((f) => f.name).sort())
    for (const f of parts)
      expect(decoder.decode(f.bytes)).toBe(readFileSync(join(dir, f.name), 'utf8'))
    const manifest = files.at(-1) as { name: string; bytes: Uint8Array }
    expect(JSON.parse(decoder.decode(manifest.bytes))).toEqual(
      JSON.parse(readFileSync(join(dir, manifest.name), 'utf8')),
    )
  })
})

describe('the CSV’s source', () => {
  it('reads the walk a hundred receipts at a time, as rows', async () => {
    const doc = receiptOf('ended')
    server.on(
      'GET /api/v1/receipts?limit=100',
      okText(
        JSON.stringify(
          pageOf([linkOf(2, [doc]), { n: 1, verdict: 'lost' }], {
            next: { n: 1, index: 0 },
            done: false,
          }),
        ),
      ),
    )
    const page = await csvSource((r) => (r.vendor === 'claude.ai' ? 'Anthropic' : undefined))(
      undefined,
    )
    expect(page.receipts).toBe(1)
    expect(page.done).toBe(false)
    expect(page.lines).toHaveLength(2)
    expect(page.lines[0]).toContain('"Anthropic"')
    expect(page.head.sha256).toBe(headDigest)

    server.on(
      'GET /api/v1/receipts?before=1.0&limit=100',
      okText(JSON.stringify(pageOf([], { done: true }))),
    )
    const last = await csvSource()({ n: 1, index: 0 })
    expect([last.lines, last.done]).toEqual([[], true])
    expect(digest('x')).toHaveLength(64)
  })
})

it('keeps the CSV’s columns where they were', () => {
  expect(column('receipt')).toBe(0)
  expect(column('sha256')).toBe(CSV_COLUMNS.length - 1)
})
