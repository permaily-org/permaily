import * as api from './api.ts'

/*
  The CSV export of the record of what left (ADR 0041, "The CSV export"): for people, in a spreadsheet. The JSON export
  is the one permaily-verify -receipts checks; this one is read, never verified. Its columns only grow at the end, never
  change place or meaning. Every cell is guarded: what mail and vendors wrote (a subject, a sender's name, a model's
  topic) is never read by a spreadsheet as a formula, nor turned by controls into more cells, rows or reordered text.
*/

/** The columns, in order. A new one goes at the end (ADR 0040's details removed by a rule, ADR 0039's deliveries). */
export const CSV_COLUMNS = [
  'receipt',
  'position',
  'record',
  'time',
  'kind',
  'outcome',
  'vendor',
  'verified',
  'connection',
  'device',
  'through',
  'mailbox',
  'message',
  'received',
  'from',
  'subject',
  'parts',
  'attachment',
  'rule',
  'reason',
  'model',
  'count',
  'search',
  'sha256',
] as const

export type CsvColumn = (typeof CSV_COLUMNS)[number]

/** UTF-8 with a byte order mark, so spreadsheets read it as UTF-8; rows end in CRLF (RFC 4180). */
export const CSV_BOM = '\uFEFF'
export const CSV_NEWLINE = '\r\n'

/**
 * Characters that never reach a cell as themselves, each written as U+FFFD: C0 and C1 controls and DEL (a tab or a
 * line break would end a cell or a row in a reader that ignores quotes), the line and paragraph separators, and the
 * characters that reorder text (bidirectional embeddings, overrides, isolates and marks).
 */
const replaced =
  // biome-ignore lint/suspicious/noControlCharactersInRegex: these are the characters it replaces
  /[\u0000-\u001f\u007f-\u009f\u061C\u200E\u200F\u2028\u2029\u202A-\u202E\u2066-\u2069]/g

/** What may come before a formula and still start it: white space and the characters that show nothing. */
const leading = /^[\s\u200B-\u200D\u2060\uFEFF\uFFFD]*/u

/**
 * The first characters that make a spreadsheet read a cell as a formula, or as a command in older ones: = + - @, the
 * pipe and percent, and their full-width forms.
 */
const trigger = /^[=+\-@|%\uFF1D\uFF0B\uFF0D\uFF20\uFF5C\uFF05]/

/**
 * One cell: controls replaced, a single quote put first when what follows the leading white space would start a
 * formula, then quoted with its quotes doubled. Numbers the export writes itself (counts, positions) start with a
 * digit and are never changed.
 */
export function csvCell(value: string): string {
  let text = value.replace(replaced, '\uFFFD')
  if (trigger.test(text.replace(leading, ''))) text = `'${text}`
  return `"${text.replaceAll('"', '""')}"`
}

/** One row of cells, one for each column, ended by CRLF. */
export function csvRow(cells: readonly string[]): string {
  return cells.map(csvCell).join(',') + CSV_NEWLINE
}

/*
  The downloads' files (ADR 0041, "The two downloads, in parts"). A download is one file while the whole record fits
  EXPORT_PART_BYTES; past that it is numbered parts, each a complete file (the CSV's with its byte order mark and
  header, the JSON's whole lines), closed before the row that would take it past the limit, then a manifest written
  last, naming the parts and the head when the download began. A set without its manifest is incomplete, and the screen
  says so. Rows are counted in UTF-8 as they are encoded, so a part holds at most the limit and the page being read.
*/

/** The most bytes one file of a download holds: what a phone's tab keeps with room to spare (ADR 0041, choice 16). */
export const EXPORT_PART_BYTES = 16 * 1024 * 1024

const encoder = new TextEncoder()

/** The bytes a string takes in UTF-8, as a part counts it. */
export function utf8Bytes(text: string): number {
  return encoder.encode(text).length
}

const day = /^\d{4}-\d{2}-\d{2}$/

/** A download's file name from the person's local day: the whole file, or its part (1, 2, …) when it has parts. */
export function exportName(localDay: string, ext: 'csv' | 'jsonl', part?: number): string {
  if (!day.test(localDay)) throw new Error('not a day')
  if (part === undefined) return `permaily-receipts-${localDay}.${ext}`
  if (!Number.isInteger(part) || part < 1) throw new Error('not a part')
  return `permaily-receipts-${localDay}-part-${String(part).padStart(3, '0')}.${ext}`
}

/** The manifest of a download in parts, written after its last part. */
export function exportManifestName(localDay: string, ext: 'csv' | 'jsonl'): string {
  if (!day.test(localDay)) throw new Error('not a day')
  return `permaily-receipts-${localDay}-${ext}-manifest.json`
}

/*
  The rows of the CSV, from the walk's pages (ADR 0041, "Columns"): one per message made available or held, per
  refusal, per approval, per end, per count of refusals, per break, and per receipt this app can't read. What the walk
  found is the `record` column, in the screen's words; nothing found is left out, and nothing of a row is interpreted.
*/

type Cells = Partial<Record<CsvColumn, string>>

/** A row's cells in the columns' order, each empty when it does not apply. */
function cellsOf(row: Cells): string[] {
  return CSV_COLUMNS.map((column) => row[column] ?? '')
}

/**
 * A time as RFC 3339 in UTC: as the server wrote it when it is already (keeping its microseconds), else converted; one
 * that is not a time is written as it was, guarded like every cell.
 */
function utc(iso: string | undefined): string {
  if (iso === undefined) return ''
  const date = new Date(iso)
  if (Number.isNaN(date.getTime()) || iso.endsWith('Z')) return iso
  return date.toISOString().replace('.000Z', 'Z')
}

const receiptRecords: Record<api.ReceiptVerdict, string> = {
  ok: 'kept',
  missing: 'deleted from the storage',
  changed: 'changed in the storage',
  unreadable: "can't be read",
}

const linkRecords: Partial<Record<api.LinkVerdict, string>> = {
  lost: 'link missing',
  forked: 'link written twice',
  unreadable: "link can't be read",
  prev: "link doesn't match",
}

/** What the walk found of a receipt kept as recorded: linked late, from before, or kept. */
function keptRecord(link: api.ReceiptsLink): string {
  return link.kind === 'recovered'
    ? 'linked late'
    : link.kind === 'adopted'
      ? 'from before'
      : 'kept'
}

/** An approval's outcome is the decision recorded, not the change made (ADR 0041, choice 17). */
function consentOutcome(consent: api.ReceiptConsent): string {
  if (!consent.allowed) return 'refused request'
  if (consent.renewal) return 'approved renewal request'
  if (consent.change === 'add_device') return 'approved adding a device'
  return 'approved request'
}

const rule = (r: api.RuleRef): string => `${r.id} v${r.version}`

/** The answers a decision rested on: `<var> <op> "<topic>": <p> (<model>, <prompt>)`, separated by `; `. */
function modelCell(models: readonly api.ReceiptModel[]): string {
  return models
    .map((m) => {
      const by = [m.model, m.prompt].filter((v) => v !== undefined && v !== '').join(', ')
      return `${m.var} ${m.op} "${m.topic}": ${m.p}${by ? ` (${by})` : ''}${m.refused ? ' refused' : ''}`
    })
    .join('; ')
}

const sendersCell = (from: readonly api.MailAddress[]): string =>
  from.map((a) => (a.name ? `${a.name} <${a.address}>` : a.address)).join('; ')

/**
 * The owner the image's list verified for a receipt's vendor, from the connections listed now; none when the vendor
 * is not one of theirs.
 */
export type VerifiedOf = (receipt: api.Receipt) => string | undefined

/** The CSV's rows of one page of the walk, as cells in the columns' order. */
export function csvRowsOf(
  page: api.ReceiptsPage,
  verified: VerifiedOf = () => undefined,
): string[][] {
  const rows: string[][] = []
  for (const link of page.links) {
    const linkRecord = linkRecords[link.verdict]
    if (linkRecord)
      rows.push(cellsOf({ position: String(link.n), record: linkRecord, time: utc(link.at) }))
    for (const entry of link.receipts) {
      const position = `${link.n}.${entry.index}`
      const found = { receipt: entry.id, position, sha256: entry.sha256 }
      if (entry.verdict !== 'ok') {
        rows.push(cellsOf({ ...found, record: receiptRecords[entry.verdict] ?? "can't be read" }))
        continue
      }
      const receipt = api.readReceipt(entry.receipt)
      if (!receipt) {
        rows.push(cellsOf({ ...found, record: "this app can't read it" }))
        continue
      }
      for (const cells of receiptRows(receipt, verified(receipt)))
        rows.push(cellsOf({ ...found, record: keptRecord(link), ...cells }))
    }
    if (link.break)
      rows.push(
        cellsOf({
          position: String(link.n),
          record: "link before can't be read",
          time: utc(link.at),
          reason: link.break,
        }),
      )
  }
  return rows
}

/** The rows of one receipt read: what it records, row by row. */
function receiptRows(r: api.Receipt, verified: string | undefined): Cells[] {
  const search = r.formulas.find((f) => f.role === 'search')?.formula
  const shared: Cells = {
    time: utc(r.at),
    kind: r.kind,
    vendor: r.vendor,
    verified: verified ?? '',
    connection: r.connection ?? '',
    device: r.device ?? '',
    through: r.resource,
    search: search === undefined ? '' : JSON.stringify(search),
  }
  const of = (cells: Cells): Cells => ({ ...shared, ...cells })
  switch (r.kind) {
    case 'search':
    case 'read':
    case 'attachment': {
      const rows = [
        ...r.delivered.map((d) =>
          of({
            outcome: 'made available',
            mailbox: d.mailbox,
            message: d.message,
            received: utc(d.received),
            from: sendersCell(d.from),
            subject: d.subject ?? '',
            parts: d.parts.join(' '),
            attachment: d.attachment === undefined ? '' : String(d.attachment),
            rule: d.cleared.map((c) => rule(c.rule)).join('; '),
            model: modelCell([...d.model, ...d.cleared.flatMap((c) => c.model)]),
          }),
        ),
        ...r.held.map((h) =>
          of({
            outcome: 'held',
            mailbox: h.mailbox,
            message: h.message,
            rule: rule(h.rule),
            reason: h.why,
            model: modelCell(h.model),
          }),
        ),
      ]
      return rows.length > 0 ? rows : [of({ outcome: 'nothing matched' })]
    }
    case 'refused': {
      const refused = r.refused as api.ReceiptRefused
      const held = refused.held
      return [
        of({
          outcome: 'refused',
          reason: refused.reason,
          mailbox: refused.mailbox ?? held?.mailbox ?? '',
          message: refused.message ?? held?.message ?? '',
          rule: held ? rule(held.rule) : '',
          model: held ? modelCell(held.model) : '',
        }),
      ]
    }
    case 'consent': {
      const consent = r.consent as api.ReceiptConsent
      return [of({ outcome: consentOutcome(consent), parts: consent.parts.join(' ') })]
    }
    case 'ended':
      return [of({ outcome: 'ended', reason: (r.ended as { reason: string }).reason })]
    case 'refusals': {
      const { counts } = r.refusals as api.ReceiptRefusals
      return counts.length > 0
        ? counts.map((c) =>
            of({
              outcome: 'refused',
              reason: `${c.operation}: ${c.reason}`,
              count: String(c.count),
            }),
          )
        : [of({ outcome: 'refused', count: '0' })]
    }
  }
}

/** The CSV's lines of one page of the walk, each a whole row ended by CRLF. */
export function csvLinesOf(page: api.ReceiptsPage, verified?: VerifiedOf): string[] {
  return csvRowsOf(page, verified).map(csvRow)
}

/** The first line of every CSV file, a part's too: the byte order mark and the columns' names. */
export const CSV_HEADER = CSV_BOM + csvRow(CSV_COLUMNS)

/*
  The writer of a download (ADR 0041, "The two downloads, in parts"): it reads the record a page at a time and keeps
  at most one part's bytes. A file is saved only whole: one file when the record ends within the limit, else numbered
  parts, each closed before the line that would take it past the limit, then the manifest, last. A stop (a page that
  fails, does not move on, or a cancel) saves nothing of the part being read and no manifest, and says where the next
  part begins, so the download can go on from there while the page is open.
*/

/** A page of what a download reads: its lines, never split between parts, and where the next page begins. */
export type SourcePage = {
  lines: string[]
  head: api.ReceiptsHead
  next: api.ReceiptsPosition
  done: boolean
  receipts: number
}

/** Reads the page that begins at `before`, or the first. */
export type Source = (before: api.ReceiptsPosition | undefined) => Promise<SourcePage>

/** The CSV's pages, from the walk: rows the person reads. */
export function csvSource(verified?: VerifiedOf): Source {
  return async (before) => {
    const page = await api.receipts({ ...(before ? { before } : {}), limit: 100 })
    return {
      lines: csvLinesOf(page, verified),
      head: page.head,
      next: page.next,
      done: page.done,
      receipts: page.links.reduce((n, l) => n + l.receipts.length, 0),
    }
  }
}

/** The JSON export's pages, each the line the server wrote: the file permaily-verify -receipts reads. */
export const jsonSource: Source = async (before) => {
  const page = await api.exportReceipts(before)
  return { ...page, lines: [page.line] }
}

/** A file a download saved: its name, its bytes and their SHA-256, as the manifest names it. */
export type SavedFile = { name: string; bytes: number; sha256: string }

/** Where a download stopped: what it saved, and where the part it was reading began. */
export type ExportState = {
  ext: 'csv' | 'jsonl'
  day: string
  /** The head when the download began, as `<n>:<sha256>`, or null when it was not one to note. */
  head: string | null
  parts: SavedFile[]
  /** The page the next part's first line is on, and how many lines of it earlier parts hold. */
  from?: api.ReceiptsPosition
  skip: number
  /**
   * The SHA-256 of those `skip` lines, as earlier parts hold them: going on, the page must begin with exactly them, or
   * the download can't prove where it stopped. Empty when `skip` is 0.
   */
  held: string
  /** Every line is in a saved part: only the manifest is left to write. */
  written: boolean
  /** Receipts read before the next part, for progress. */
  receipts: number
}

/**
 * Why a download stopped: the API's code, `cancelled`, or `changed` when the page the next part begins on no longer
 * begins with the lines earlier parts hold, so it can't go on and must start again.
 */
export type ExportStopCode = api.ApiErrorCode | 'cancelled' | 'changed'

/** A download that stopped before its end: `state` says what it saved and where it would go on; `code` why. */
export class ExportStopped extends Error {
  readonly state: ExportState
  readonly code: ExportStopCode
  constructor(state: ExportState, code: ExportStopCode) {
    super(code)
    this.state = state
    this.code = code
  }
}

/** What a finished download saved: its files, and its manifest's name when it is in parts. */
export type ExportDone = { files: SavedFile[]; manifest: string | null }

export type ExportProgress = { receipts: number; parts: number }

export type ExportOptions = {
  ext: 'csv' | 'jsonl'
  source: Source
  /** Saves one whole file. */
  save: (name: string, file: Blob) => void
  /** The person's local day when the download began. */
  day: string
  signal?: AbortSignal
  onProgress?: (progress: ExportProgress) => void
  /** Where a download that stopped goes on from. */
  resume?: ExportState
  /** The limit of a part: `EXPORT_PART_BYTES`, lower only in tests. */
  partBytes?: number
}

const types = { csv: 'text/csv;charset=utf-8', jsonl: 'application/x-ndjson' } as const

/** Says whether a is strictly before b on the chain, as the next page's start always is. */
function below(a: api.ReceiptsPosition, b: api.ReceiptsPosition): boolean {
  return a.n < b.n || (a.n === b.n && a.index < b.index)
}

async function sha256(bytes: Uint8Array<ArrayBuffer>): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes))
  return Array.from(digest, (b) => b.toString(16).padStart(2, '0')).join('')
}

/** The head to note, `<n>:<sha256>`, when it is one: kept, with its digest. */
function noted(head: api.ReceiptsHead): string | null {
  return (head.verdict === 'ok' || head.verdict === 'expired') && head.sha256
    ? `${head.n}:${head.sha256}`
    : null
}

/** A download, written in files of at most `partBytes`, then its manifest. Throws `ExportStopped` when it stops. */
export async function writeExport(options: ExportOptions): Promise<ExportDone> {
  const state: ExportState = options.resume
    ? { ...options.resume, parts: [...options.resume.parts] }
    : {
        ext: options.ext,
        day: options.day,
        head: null,
        parts: [],
        skip: 0,
        held: '',
        written: false,
        receipts: 0,
      }
  try {
    return await writing(options, state)
  } catch (error) {
    if (error instanceof ExportStopped) throw error
    // Anything else that stops it (a file the browser would not take) stops it the same way: nothing more saved.
    throw new ExportStopped({ ...state, parts: [...state.parts] }, 'other')
  }
}

async function writing(options: ExportOptions, state: ExportState): Promise<ExportDone> {
  const { ext, source, save, signal, onProgress } = options
  const limit = options.partBytes ?? EXPORT_PART_BYTES
  const header = encoder.encode(ext === 'csv' ? CSV_HEADER : '')
  const fresh = !options.resume
  let chunks: Uint8Array[] = []
  let bytes = 0
  let lines = 0
  const stop = (code: ExportStopCode): never => {
    throw new ExportStopped({ ...state, parts: [...state.parts] }, code)
  }
  // A cancel holds at every wait: nothing is saved after it, of a part or of the manifest.
  const go = () => {
    if (signal?.aborted) stop('cancelled')
  }
  const open = () => {
    chunks = header.length > 0 ? [header] : []
    bytes = header.length
    lines = 0
  }
  const close = async (name: string): Promise<void> => {
    const file = new Uint8Array(bytes)
    let at = 0
    for (const chunk of chunks) {
      file.set(chunk, at)
      at += chunk.length
    }
    chunks = []
    const saved = { name, bytes: file.length, sha256: await sha256(file) }
    go()
    save(name, new Blob([file], { type: types[ext] }))
    state.parts.push(saved)
  }
  /** The digest of a page's first `n` lines, as `held` notes them. */
  const heldOf = async (page: SourcePage, n: number): Promise<string> =>
    n === 0 ? '' : sha256(encoder.encode(page.lines.slice(0, n).join('')))

  if (!state.written) {
    open()
    let before = state.from
    let skip = state.skip
    let receipts = state.receipts
    for (;;) {
      go()
      let page: SourcePage
      try {
        page = await source(before)
      } catch (error) {
        return stop(api.errorCode(error))
      }
      go()
      // A page that does not move on would be asked for ever.
      if (!page.done && before && !below(page.next, before)) stop('other')
      if (fresh && state.parts.length === 0 && before === undefined) state.head = noted(page.head)
      // Going on, the page must begin with the lines earlier parts hold, or a receipt could be left out or repeated.
      if (skip > 0) {
        const held = page.lines.length >= skip ? await heldOf(page, skip) : null
        go()
        if (held !== state.held) stop('changed')
      }
      for (let i = skip; i < page.lines.length; i++) {
        const line = encoder.encode(page.lines[i])
        // A line no part could hold: never far from it (a receipt is at most 256 KiB sealed), and never cut.
        if (header.length + line.length > limit) stop('other')
        if (bytes + line.length > limit && lines > 0) {
          const held = await heldOf(page, i)
          await close(exportName(state.day, ext, state.parts.length + 1))
          // Where the next part begins is noted as soon as this one is saved, with nothing to wait for between.
          state.from = before
          state.skip = i
          state.held = held
          state.receipts = receipts
          onProgress?.({ receipts, parts: state.parts.length })
          open()
        }
        chunks.push(line)
        bytes += line.length
        lines++
      }
      skip = 0
      receipts += page.receipts
      onProgress?.({ receipts, parts: state.parts.length })
      if (page.done) break
      before = page.next
    }

    if (state.parts.length === 0) {
      const name = exportName(state.day, ext)
      await close(name)
      return { files: state.parts, manifest: null }
    }
    await close(exportName(state.day, ext, state.parts.length + 1))
    // Every line is saved: going on writes the manifest and reads nothing again.
    state.written = true
    state.from = undefined
    state.skip = 0
    state.held = ''
    state.receipts = receipts
  }

  go()
  const manifest = exportManifestName(state.day, ext)
  const body = `${JSON.stringify(
    { complete: true, file: ext, date: state.day, head: state.head, parts: state.parts },
    null,
    2,
  )}\n`
  save(manifest, new Blob([body], { type: 'application/json' }))
  return { files: state.parts, manifest }
}

/** The person's local day, as the downloads' names write it. */
export function localDay(now: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`
}

/** Saves a file through the browser, as every download of the app: a same-origin `<a download>`. */
export function saveFile(name: string, file: Blob): void {
  const url = URL.createObjectURL(file)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.rel = 'noopener'
  document.body.append(a)
  a.click()
  a.remove()
  // The browser has the file once the click is handled; the address is not needed after.
  setTimeout(() => URL.revokeObjectURL(url), 0)
}
