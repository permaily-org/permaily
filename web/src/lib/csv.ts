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
