/*
  Live preview for the rule editor. Each sample email carries the keys it can match on:
  its sender address and domain, plus any data type or subject word it was given in data.ts.
  The preview only knows the sample rows, so mail outside them stays counted as loaded.
*/
import type { ConditionBlock, PreviewRow, RuleSeed } from './data.ts'

export type Draft = { blocks: ConditionBlock[]; butNot: string[] }

type MailRow = Extract<PreviewRow, { kind: 'mail' }>

export type RowStatus = 'new' | 'soft' | 'excluded' | 'dropped' | 'withheld'

export type RowView =
  | { kind: 'other'; widths: [number, number, number] }
  | { kind: 'blocked'; id: string; date: string; sender: string; reason: string }
  | { kind: 'mail'; row: MailRow; status: RowStatus; excludedBy?: string }

export function domainOf(address: string): string {
  return address.split('@')[1]?.toLowerCase() ?? address.toLowerCase()
}

function positiveChips(blocks: ConditionBlock[]): Set<string> {
  return new Set(blocks.flatMap((block) => block.flatMap((group) => group.chips.map((chip) => chip.toLowerCase()))))
}

function exclusionFor(row: MailRow, butNot: string[]): string | undefined {
  const address = row.address.toLowerCase()
  const domain = domainOf(address)
  return butNot.find((entry) => {
    const e = entry.toLowerCase()
    return e === address || e === domain
  })
}

function hitsChips(row: MailRow, chips: Set<string>): boolean {
  const keys = [row.address, domainOf(row.address), ...(row.keys ?? [])].map((k) => k.toLowerCase())
  return keys.some((k) => chips.has(k))
}

function matcher(draft: Draft, matchAll: boolean) {
  const chips = positiveChips(draft.blocks)
  return (row: MailRow) => (matchAll || hitsChips(row, chips)) && !exclusionFor(row, draft.butNot)
}

/** The rule as it was before this edit: chips added in this edit removed, and nothing at all for a new rule. */
export function savedDraft(seed: RuleSeed, isNew: boolean): Draft {
  if (isNew) return { blocks: [], butNot: [] }
  const unsaved = new Set(seed.unsaved)
  return {
    blocks: seed.blocks.map((block) => block.map((group) => ({ ...group, chips: group.chips.filter((c) => !unsaved.has(c)) }))),
    butNot: seed.butNot,
  }
}

export function initialDraft(seed: RuleSeed): Draft {
  return { blocks: seed.blocks, butNot: seed.butNot }
}

export type Preview = { rows: RowView[]; count: number; newCount: number; dropped: number }

export function preview(seed: RuleSeed, isNew: boolean, draft: Draft): Preview {
  const matchAll = Boolean(seed.readOnly)
  const mailRows = seed.rows.filter((r): r is MailRow => r.kind === 'mail')
  const now = matcher(draft, matchAll)
  const before = matcher(savedDraft(seed, isNew), matchAll)
  const atLoad = matcher(initialDraft(seed), matchAll)

  const loadedMatches = mailRows.filter(atLoad).length
  const loadedNew = mailRows.filter((r) => atLoad(r) && !before(r)).length
  const chips = positiveChips(draft.blocks)
  const hasChips = matchAll || chips.size > 0

  let matched = 0
  let fresh = 0
  let dropped = 0
  const rows = seed.rows.map((row): RowView => {
    if (row.kind !== 'mail') return row
    const isMatch = now(row)
    const wasMatch = before(row)
    if (isMatch) {
      matched += 1
      if (!wasMatch) fresh += 1
      return { kind: 'mail', row, status: wasMatch ? 'soft' : 'new' }
    }
    if (wasMatch) dropped += 1
    const excludedBy = exclusionFor(row, draft.butNot)
    if (excludedBy && (matchAll || hitsChips(row, chips))) return { kind: 'mail', row, status: 'excluded', excludedBy }
    return { kind: 'mail', row, status: wasMatch ? 'dropped' : 'withheld' }
  })

  return {
    rows,
    count: hasChips ? Math.max(0, seed.count - loadedMatches + matched) : 0,
    newCount: hasChips ? Math.max(0, seed.newCount - loadedNew + fresh) : 0,
    dropped,
  }
}

export function emails(n: number): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? 'email' : 'emails'}`
}
