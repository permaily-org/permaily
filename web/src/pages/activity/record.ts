import { useCallback, useRef, useState } from 'react'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'

/*
  The account's record as the Activity screen lists it (ADR 0041, "What it lists", "Filters and pages"): the walk's
  pages from `GET /api/v1/receipts`, from the head and then from each page's `next`, turned into rows in the walk's
  order. Nothing of it is stored: it lives in the page's memory, and a reload reads it again.
*/

/** Receipts a page asks for. */
export const PAGE_SIZE = 100
/** With a filter, one press reads at most this many pages… */
export const PRESS_PAGES = 5
/** …or until this many rows match. */
export const PRESS_ROWS = 50

type Base = {
  /** Unique among the rows: its link, its receipt and its item. */
  key: string
  /** The receipt it belongs to, or its link: rows of one receipt share it. */
  group: string
  link: api.ReceiptsLink
}

/** One row of the list. A break, a receipt not found as recorded and one this app can't read pass every filter. */
export type Row =
  | (Base & { type: 'link' })
  | (Base & { type: 'break' })
  | (Base & { type: 'missing'; entry: api.ReceiptsEntry })
  | (Base & { type: 'unknown'; entry: api.ReceiptsEntry })
  | (Base & {
      type: 'receipt'
      entry: api.ReceiptsEntry
      receipt: api.Receipt
      /** Which part of the receipt the row is: a message made available or held, or the receipt as a whole. */
      item: { delivered: number } | { held: number } | 'nothing' | 'whole'
    })

/** The time a row is grouped by: its receipt's own, else its link's. */
export function rowTime(row: Row): string | undefined {
  return row.type === 'receipt' ? row.receipt.at : row.link.at
}

const brokenLink: readonly api.LinkVerdict[] = ['lost', 'forked', 'unreadable', 'prev']

/** A page's rows, in the walk's order: a link not kept first, then its receipts, then a break under it. */
export function rowsOf(page: api.ReceiptsPage): Row[] {
  const rows: Row[] = []
  for (const link of page.links) {
    const l = `${link.n}`
    if (brokenLink.includes(link.verdict))
      rows.push({ type: 'link', key: `${l}:link`, group: l, link })
    for (const entry of link.receipts) {
      const group = `${l}.${entry.index}`
      if (entry.verdict !== 'ok') {
        rows.push({ type: 'missing', key: group, group, link, entry })
        continue
      }
      const receipt = api.readReceipt(entry.receipt)
      if (!receipt) {
        rows.push({ type: 'unknown', key: group, group, link, entry })
        continue
      }
      const of = (item: Extract<Row, { type: 'receipt' }>['item'], key: string): Row => ({
        type: 'receipt',
        key: `${group}:${key}`,
        group,
        link,
        entry,
        receipt,
        item,
      })
      if (receipt.kind === 'search' || receipt.kind === 'read' || receipt.kind === 'attachment') {
        receipt.delivered.forEach((_, i) => {
          rows.push(of({ delivered: i }, `d${i}`))
        })
        receipt.held.forEach((_, i) => {
          rows.push(of({ held: i }, `h${i}`))
        })
        if (receipt.delivered.length === 0 && receipt.held.length === 0)
          rows.push(of('nothing', 'nothing'))
      } else rows.push(of('whole', 'whole'))
    }
    if (link.break) rows.push({ type: 'break', key: `${l}:break`, group: `${l}:break`, link })
  }
  return rows
}

/** What the screen may be filtered to, beside a connection. */
export type KindFilter = 'all' | 'made' | 'held' | 'decided'

export const kindFilters: { value: KindFilter; label: string }[] = [
  { value: 'all', label: 'Everything' },
  { value: 'made', label: 'Made available' },
  { value: 'held', label: 'Held or refused' },
  { value: 'decided', label: 'Approvals and ends' },
]

export type Filter = { connection: string | null; kind: KindFilter }

/** Whether a filter picks anything out: a press then reads more than one page. */
export function filtering(filter: Filter): boolean {
  return filter.connection !== null || filter.kind !== 'all'
}

/** Whether a row is shown under a filter. What was found broken, or can't be read, always is. */
export function matches(row: Row, filter: Filter): boolean {
  if (row.type !== 'receipt') return true
  if (filter.connection !== null && row.receipt.connection !== filter.connection) return false
  switch (filter.kind) {
    case 'all':
      return true
    case 'made':
      return typeof row.item === 'object' ? 'delivered' in row.item : row.item === 'nothing'
    case 'held':
      return (
        (typeof row.item === 'object' && 'held' in row.item) ||
        row.receipt.kind === 'refused' ||
        row.receipt.kind === 'refusals'
      )
    case 'decided':
      return row.receipt.kind === 'consent' || row.receipt.kind === 'ended'
  }
}

/** Says whether a is strictly before b on the chain, as the next page's start always is. */
export function below(a: api.ReceiptsPosition, b: api.ReceiptsPosition): boolean {
  return a.n < b.n || (a.n === b.n && a.index < b.index)
}

export type Walked = {
  head: api.ReceiptsHead | null
  rows: Row[]
  next: api.ReceiptsPosition | null
  done: boolean
  /** Receipts the walk has listed so far, for "in the newest 500". */
  examined: number
}

/** What the last press found: rows of the filter (counted by receipt), links read, and whether a filter was on. */
export type Press = { found: number; links: number; filtered: boolean }

const empty: Walked = { head: null, rows: [], next: null, done: false, examined: 0 }

/** Why the record could not be read, in the screen's words, and whether trying again can help. */
export type Failure = { code: api.ApiErrorCode }

/**
 * The record read so far, and reading more of it. `read(filter)` reads from the head the first time and from the last
 * page's `next` after; with a filter one press reads up to `PRESS_PAGES` pages or until `PRESS_ROWS` rows match. A
 * page that does not move on is refused (`other`) rather than asked for ever; `invalid_before` starts again from the
 * head. Rows already read stay when a later page fails.
 */
export function useRecord() {
  const { failed } = useSession()
  const [record, setRecord] = useState<Walked>(empty)
  const [loading, setLoading] = useState(false)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [press, setPress] = useState<Press | null>(null)
  const [restarted, setRestarted] = useState(false)
  const now = useRef(record)
  const busy = useRef(false)

  const read = useCallback(
    async (filter: Filter, fromHead = false): Promise<void> => {
      if (busy.current) return
      busy.current = true
      setLoading(true)
      setFailure(null)
      let at: Walked = fromHead ? empty : now.current
      const filtered = filtering(filter)
      const found = new Set<string>()
      let links = 0
      let error: unknown = null
      try {
        for (let pages = 0; ; ) {
          const before = at.next ?? undefined
          const page = await api.receipts({ ...(before ? { before } : {}), limit: PAGE_SIZE })
          if (!page.done && before && !below(page.next, before)) throw new api.ApiError('other')
          const rows = rowsOf(page)
          for (const row of rows) if (matches(row, filter)) found.add(row.group)
          links += page.links.length
          at = {
            head: at.head ?? page.head,
            rows: [...at.rows, ...rows],
            next: page.next,
            done: page.done,
            examined: at.examined + page.links.reduce((n, l) => n + l.receipts.length, 0),
          }
          pages++
          if (page.done || !filtered || pages >= PRESS_PAGES || found.size >= PRESS_ROWS) break
        }
      } catch (problem) {
        error = problem
      }
      now.current = at
      setRecord(at)
      setPress({ found: found.size, links, filtered })
      busy.current = false
      setLoading(false)
      if (error === null) return
      const code = failed(error)
      if (code === 'invalid_before' && !fromHead) {
        // The chain moved under the page the screen was reading: it starts again from the newest.
        setRestarted(true)
        await read(filter, true)
        return
      }
      if (code) setFailure({ code })
    },
    [failed],
  )

  return { record, loading, failure, press, restarted, read }
}
