/*
  Live preview for the rule editor. Each sample email can match on its sender address and domain, its subject, the text
  of its snippet, and any kind, label or topic it was given in data.ts. The preview only knows the sample rows, so mail
  outside them stays counted as loaded.
*/
import {
  type CriteriaGroup,
  type Criterion,
  operators,
  type PreviewMail,
  type RuleSeed,
} from './data.ts'

/** The conditions being edited: mail that meets every condition of any one group matches. */
export type Draft = { groups: CriteriaGroup[] }

/** new and soft both match: new only since this edit. dropped matched before this edit. none never matched. */
export type RowStatus = 'new' | 'soft' | 'excluded' | 'dropped' | 'none'

export type RowView = {
  row: PreviewMail
  status: RowStatus
  excludedBy?: string
  excludedBySender?: boolean
  matchedBy?: string
}

export function domainOf(address: string): string {
  return address.split('@')[1]?.toLowerCase() ?? address.toLowerCase()
}

/** Whether an email has a value, in the field the condition looks at. */
function has(row: PreviewMail, criterion: Criterion, value: string): boolean {
  const v = value.toLowerCase()
  const keys = (row.keys ?? []).map((k) => k.toLowerCase())
  switch (criterion.field) {
    case 'sender':
      return row.address.toLowerCase() === v || domainOf(row.address) === v
    case 'subject':
      return row.subject.toLowerCase().includes(v)
    case 'message':
      if (criterion.operator === 'contains' || criterion.operator === 'notContains') {
        return [row.subject, row.snippet ?? '', ...keys].some((text) =>
          text.toLowerCase().includes(v),
        )
      }
      // What it's about or mentions: the topics the model finds, given in data.ts.
      return keys.includes(v)
    default:
      return keys.includes(v)
  }
}

type GroupResult = {
  matches: boolean
  matchedBy?: string
  blockedBy?: { value: string; sender: boolean }
}

/**
 * A group matches when every condition with values holds. A negative condition that fails is what blocked an email the
 * rest of its group would have matched, so the preview can show it as excluded.
 */
function evaluate(group: CriteriaGroup, row: PreviewMail): GroupResult {
  const active = group.criteria.filter((c) => c.values.length > 0)
  if (active.length === 0) return { matches: false }
  let matchedBy: string | undefined
  let blockedBy: GroupResult['blockedBy']
  for (const criterion of active) {
    const hit = criterion.values.find((v) => has(row, criterion, v))
    if (operators[criterion.operator].negative) {
      if (hit && !blockedBy) blockedBy = { value: hit, sender: criterion.field === 'sender' }
    } else if (!hit) {
      return { matches: false }
    } else {
      matchedBy ??= hit
    }
  }
  return blockedBy ? { matches: false, blockedBy } : { matches: true, matchedBy }
}

function judge(draft: Draft, row: PreviewMail): GroupResult {
  let blocked: GroupResult['blockedBy']
  for (const group of draft.groups) {
    const result = evaluate(group, row)
    if (result.matches) return result
    blocked ??= result.blockedBy
  }
  return { matches: false, blockedBy: blocked }
}

function hasConditions(draft: Draft): boolean {
  return draft.groups.some((g) => g.criteria.some((c) => c.values.length > 0))
}

/** The rule as it was before this edit: values added in this edit removed, and nothing at all for a new rule. */
export function savedDraft(seed: RuleSeed, isNew: boolean): Draft {
  if (isNew) return { groups: [] }
  const unsaved = new Set(seed.unsaved)
  return {
    groups: seed.groups.map((g) => ({
      ...g,
      criteria: g.criteria.map((c) => ({ ...c, values: c.values.filter((v) => !unsaved.has(v)) })),
    })),
  }
}

export function initialDraft(seed: RuleSeed): Draft {
  return { groups: seed.groups }
}

export type Preview = { rows: RowView[]; count: number; newCount: number; dropped: number }

export function preview(seed: RuleSeed, isNew: boolean, draft: Draft): Preview {
  const saved = savedDraft(seed, isNew)
  const loaded = initialDraft(seed)
  const matches = (d: Draft) => (row: PreviewMail) => judge(d, row).matches

  const loadedMatches = seed.rows.filter(matches(loaded)).length
  const loadedNew = seed.rows.filter((r) => matches(loaded)(r) && !matches(saved)(r)).length

  let matched = 0
  let fresh = 0
  let dropped = 0
  const rows = seed.rows.map((row): RowView => {
    const now = judge(draft, row)
    const wasMatch = matches(saved)(row)
    if (now.matches) {
      matched += 1
      if (!wasMatch) fresh += 1
      return { row, status: wasMatch ? 'soft' : 'new', matchedBy: now.matchedBy }
    }
    if (wasMatch) dropped += 1
    if (now.blockedBy)
      return {
        row,
        status: 'excluded',
        excludedBy: now.blockedBy.value,
        excludedBySender: now.blockedBy.sender,
      }
    return { row, status: wasMatch ? 'dropped' : 'none' }
  })

  const any = hasConditions(draft)
  return {
    rows,
    count: any ? Math.max(0, seed.count - loadedMatches + matched) : 0,
    newCount: any ? Math.max(0, seed.newCount - loadedNew + fresh) : 0,
    dropped,
  }
}

export function emails(n: number): string {
  return `${n.toLocaleString('en-US')} ${n === 1 ? 'email' : 'emails'}`
}
