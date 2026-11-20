import { describe, expect, it } from 'vitest'
import {
  CSV_BOM,
  CSV_COLUMNS,
  CSV_NEWLINE,
  csvCell,
  csvRow,
  EXPORT_PART_BYTES,
  exportManifestName,
  exportName,
  utf8Bytes,
} from '../../src/lib/csv.ts'

/** A cell as a spreadsheet reads it: the quotes taken off and the doubled ones made one. */
function unquoted(cell: string): string {
  expect(cell.startsWith('"') && cell.endsWith('"')).toBe(true)
  return cell.slice(1, -1).replaceAll('""', '"')
}

const fffd = '\uFFFD'

// ADR 0041, E12 to E14: the columns, the encoding and the guard every cell passes.
describe('the CSV’s cells', () => {
  it('has its columns in their order, each once', () => {
    expect(CSV_COLUMNS[0]).toBe('receipt')
    expect(CSV_COLUMNS.at(-1)).toBe('sha256')
    expect(new Set(CSV_COLUMNS).size).toBe(CSV_COLUMNS.length)
    expect(CSV_BOM).toBe('\uFEFF')
    expect(CSV_NEWLINE).toBe('\r\n')
  })

  it('quotes every cell and doubles its quotes', () => {
    expect(csvCell('')).toBe('""')
    expect(csvCell('Invoice 12 "you", <for>')).toBe('"Invoice 12 ""you"", <for>"')
    expect(csvRow(['a', 'b,c'])).toBe('"a","b,c"\r\n')
  })

  it('puts a single quote before whatever would start a formula, after any white space', () => {
    const triggers = [
      '=1+1',
      '+1',
      '-1',
      '@SUM(A1)',
      '|calc',
      '%0A',
      '\uFF1D1+1',
      '\uFF0B1',
      '\uFF0D1',
      '\uFF20x',
      '\uFF5Cx',
      '\uFF05x',
      '   =HYPERLINK("https://example.com")',
      '\u00A0=x',
      '\u3000=x',
      '\u200B=x',
      '\uFEFF=x',
      '\t=x',
      '\r=x',
      '\n=x',
      '=cmd|" /C calc"!A0',
    ]
    for (const t of triggers)
      expect(unquoted(csvCell(t)).startsWith("'"), JSON.stringify(t)).toBe(true)
  })

  it('leaves text, addresses and the numbers it writes itself as they are', () => {
    for (const t of [
      'Invoice 12',
      'billing@example.com',
      'Ana <ana@example.com>',
      '12.3',
      '0',
      'a=b',
      "'quoted",
    ])
      expect(unquoted(csvCell(t))).toBe(t)
  })

  it('writes controls, separators and the characters that reorder text as U+FFFD', () => {
    const hostile = 'a\tb\rc\nd\u0000e\u007Ff\u0085g\u2028h\u2029i\u202Ej\u2066k\u200Fl\u061Cm'
    expect(unquoted(csvCell(hostile))).toBe(
      ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm'].join(fffd),
    )
    // No cell holds what would end it or a row in a reader that ignores quotes.
    for (let c = 0; c < 0x20; c++) expect(csvCell(String.fromCharCode(c))).toBe(`"${fffd}"`)
  })

  it('never lets a cell start a formula, whatever it is made of', () => {
    const pieces = [
      ' ',
      '\t',
      '\r',
      '\n',
      '\u200B',
      '\uFEFF',
      '\u00A0',
      '=',
      '+',
      '-',
      '@',
      '|',
      '%',
      '\uFF1D',
      'a',
      '1',
      '"',
      "'",
    ]
    const triggers = [
      '=',
      '+',
      '-',
      '@',
      '|',
      '%',
      '\uFF1D',
      '\uFF0B',
      '\uFF0D',
      '\uFF20',
      '\uFF5C',
      '\uFF05',
    ]
    let seed = 41
    const random = () => {
      seed = (seed * 1103515245 + 12345) % 2 ** 31
      return seed / 2 ** 31
    }
    for (let i = 0; i < 2000; i++) {
      let text = ''
      for (let j = 0; j < 1 + Math.floor(random() * 6); j++)
        text += pieces[Math.floor(random() * pieces.length)]
      const cell = unquoted(csvCell(text))
      const first = cell.replace(/^[\s\u200B-\u200D\u2060\uFEFF\uFFFD]*/u, '')[0] ?? ''
      expect(triggers.includes(first) && !cell.startsWith("'"), JSON.stringify(text)).toBe(false)
      expect(
        [...cell].some((c) => c.charCodeAt(0) < 0x20),
        JSON.stringify(text),
      ).toBe(false)
    }
  })
})

// ADR 0041, "Changed after the design review", 4: a download is one file while it fits EXPORT_PART_BYTES, else numbered
// parts and a manifest written last. What the seam fixes: the limit, the names, and the bytes a row takes once encoded.
describe('the downloads’ parts', () => {
  it('holds at most 16 MiB in a part, counted in UTF-8', () => {
    expect(EXPORT_PART_BYTES).toBe(16 * 1024 * 1024)
    expect(utf8Bytes('')).toBe(0)
    expect(utf8Bytes('abc')).toBe(3)
    expect(utf8Bytes(CSV_BOM)).toBe(3)
    expect(utf8Bytes('é')).toBe(2)
    expect(utf8Bytes('\uFFFD')).toBe(3)
    expect(utf8Bytes('😀')).toBe(4)
    // A subject of characters that each take three bytes takes three times its length, not its length.
    const cell = csvCell('請求書'.repeat(1000))
    expect(utf8Bytes(cell)).toBe(2 + 9000)
  })

  it('names a whole file, its parts in order and their manifest by the local day alone', () => {
    expect(exportName('2026-11-20', 'csv')).toBe('permaily-receipts-2026-11-20.csv')
    expect(exportName('2026-11-20', 'jsonl')).toBe('permaily-receipts-2026-11-20.jsonl')
    expect(exportName('2026-11-20', 'csv', 1)).toBe('permaily-receipts-2026-11-20-part-001.csv')
    expect(exportName('2026-11-20', 'jsonl', 12)).toBe(
      'permaily-receipts-2026-11-20-part-012.jsonl',
    )
    expect(exportName('2026-11-20', 'csv', 1000)).toBe('permaily-receipts-2026-11-20-part-1000.csv')
    expect(exportManifestName('2026-11-20', 'csv')).toBe(
      'permaily-receipts-2026-11-20-csv-manifest.json',
    )
    expect(exportManifestName('2026-11-20', 'jsonl')).toBe(
      'permaily-receipts-2026-11-20-jsonl-manifest.json',
    )
    const names = [1, 2, 9, 10, 100].map((n) => exportName('2026-11-20', 'csv', n))
    expect([...names].sort()).toEqual(names)
    for (const bad of [0, -1, 1.5, Number.NaN])
      expect(() => exportName('2026-11-20', 'csv', bad)).toThrow()
    for (const bad of ['', '2026-11-2', '20261120', 'acc_ana'])
      expect(() => exportName(bad, 'csv')).toThrow()
  })
})
