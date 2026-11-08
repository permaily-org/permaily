/*
  Live preview for the rule editor. Each sample email carries the keys it can match on:
  its sender address and domain, plus any kind, label or topic it was given in data.ts.
  The preview only knows the sample rows, so mail outside them stays counted as loaded.
*/
import type { ConditionBlock, PreviewMail, RuleSeed } from './data.ts'

export type Draft = { blocks: ConditionBlock[]; butNot: string[] }

/** new and soft both match: new only since this edit. dropped matched before this edit. none never matched. */
export type RowStatus = 'new' | 'soft' | 'excluded' | 'dropped' | 'none'

export type RowView = { row: PreviewMail; status: RowStatus; excludedBy?: string; matchedBy?: string }

export function domainOf(address: string): string {
  return address.split('@')[1]?.toLowerCase() ?? address.toLowerCase()
}

function positiveChips(blocks: ConditionBlock[]): Set<string> {
  return new Set(blocks.flatMap((block) => block.flatMap((group) => group.chips.map((chip) => chip.toLowerCase()))))
}

function exclusionFor(row: PreviewMail, butNot: string[]): string | undefined {
  const address = row.address.toLowerCase()
  const domain = domainOf(address)
  return butNot.find((entry) => {
    const e = entry.toLowerCase()
    return e === address || e === domain
  })
}

function matchingKey(row: PreviewMail, chips: Set<string>): string | undefined {
  return [row.address, domainOf(row.address), ...(row.keys ?? [])].find((k) => chips.has(k.toLowerCase()))
}

function matcher(draft: Draft) {
  const chips = positiveChips(draft.blocks)
  return (row: PreviewMail) => matchingKey(row, chips) !== undefined && !exclusionFor(row, draft.butNot)
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
  const now = matcher(draft)
  const before = matcher(savedDraft(seed, isNew))
  const atLoad = matcher(initialDraft(seed))

  const loadedMatches = seed.rows.filter(atLoad).length
  const loadedNew = seed.rows.filter((r) => atLoad(r) && !before(r)).length
  const chips = positiveChips(draft.blocks)

  let matched = 0
  let fresh = 0
  let dropped = 0
  const rows = seed.rows.map((row): RowView => {
    const isMatch = now(row)
    const wasMatch = before(row)
    if (isMatch) {
      matched += 1
      if (!wasMatch) fresh += 1
      return { row, status: wasMatch ? 'soft' : 'new', matchedBy: matchingKey(row, chips) }
    }
    if (wasMatch) dropped += 1
    const excludedBy = exclusionFor(row, draft.butNot)
    if (excludedBy && matchingKey(row, chips)) return { row, status: 'excluded', excludedBy }
    return { row, status: wasMatch ? 'dropped' : 'none' }
  })

  return {
    rows,
    count: chips.size > 0 ? Math.max(0, seed.count - loadedMatches + matched) : 0,
    newCount: chips.size > 0 ? Math.max(0, seed.newCount - loadedNew + fresh) : 0,
    dropped,
  }
}

export function emails(n: number): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? 'email' : 'emails'}`
}
