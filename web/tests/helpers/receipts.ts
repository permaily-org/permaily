/**
 * The account's record as `GET /api/v1/receipts` answers it (ADR 0037, internal/contracts/receipts.go), built from the
 * receipts service's golden documents (internal/services/receipts/testdata/golden), the documents the server stores.
 * Digests are made here at run time: none is a real one.
 */
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

const dir = join(import.meta.dirname, '../../../internal/services/receipts/testdata/golden')

/** Every golden receipt by its file's name without `.json`: `search`, `read`, `attachment`, `refused`, … */
export const golden: Record<string, Record<string, unknown>> = Object.fromEntries(
  readdirSync(dir)
    .filter((f) => f.endsWith('.json'))
    .map((f) => [f.slice(0, -5), JSON.parse(readFileSync(join(dir, f), 'utf8'))]),
)

/** A golden receipt, a fresh copy, with what a test changes. */
export function receiptOf(name: string, overrides: Record<string, unknown> = {}) {
  return { ...structuredClone(golden[name]), ...overrides }
}

/** The connection the golden receipts name. */
export const goldenConnection = 'con_cccccccccccccccccccccccccc'

/** A digest of 64 hexadecimal digits, made at run time from a seed: "3f2a…9c1d" for the head. */
export function digest(seed: string): string {
  return (seed + '0'.repeat(64)).slice(0, 64)
}

export const headDigest = `3f2a${'0'.repeat(56)}9c1d`

type Doc = Record<string, unknown>

/** One receipt a link names: the document only when it is `ok`. */
export function entryOf(index: number, doc: Doc, verdict = 'ok') {
  return {
    index,
    id: doc.id as string,
    sha256: digest(`e${index}`),
    verdict,
    ...(verdict === 'ok' ? { receipt: doc } : {}),
  }
}

/** A link of a page, kept, holding the receipts given (the newest first, as the walk lists them). */
export function linkOf(n: number, docs: Doc[], overrides: Record<string, unknown> = {}) {
  return {
    n,
    verdict: 'ok',
    kind: 'receipts',
    at: '2026-11-16T10:00:01Z',
    prev: digest(`p${n}`),
    receipts: docs.map((d, i) => entryOf(docs.length - 1 - i, d)),
    ...overrides,
  }
}

/** A page of the walk. */
export function pageOf(
  links: unknown[],
  options: {
    head?: Record<string, unknown>
    next?: { n: number; index: number }
    done?: boolean
  } = {},
) {
  return {
    head: options.head ?? { n: 3, verdict: 'ok', sha256: headDigest, at: '2026-11-16T10:00:01Z' },
    links,
    next: options.next ?? { n: 0, index: 0 },
    done: options.done ?? true,
  }
}

/** A receipt of another connection and vendor, as the golden `search` is of Claude's. */
export function otherReceipt(id: string, connection = 'con_other', vendor = 'trips.example') {
  return receiptOf('ended', { id, connection, vendor, device: undefined })
}

export const firstPage = 'GET /api/v1/receipts?limit=100'
export const pageBefore = (n: number, index: number) =>
  `GET /api/v1/receipts?before=${n}.${index}&limit=100`
