/*
  A rule's criteria in the editor's terms (ADR 0040, "The screens"; ADR 0027, "Rules: any of their criteria"): a rule
  matches mail that meets any of its criteria; each criteria is a set of conditions, all of which must hold; each
  condition is a field (one of the language's variables), how it compares, and values, any of which may match. A
  negative condition ("isn't", "doesn't contain") matches mail that has none of its values.

  In the language, a criteria is a formula: an AND of groups, each an OR of conditions. A positive condition of the
  editor is one group of one condition a value; a negative one is a group of one negated condition for each value. A
  group the editor cannot show as one condition (conditions of several fields joined by "or", as a proposal may have)
  is kept as it is and shown by the server's own line for it, so nothing is lost or reworded by the editor.
*/
import type {
  QueryCondition,
  QueryFormula,
  QueryOperator,
  QuerySentence,
  QueryVariable,
} from '../../lib/api.ts'

/** A condition the editor shows: a field, how it compares, and its values. */
export type Condition = {
  kind: 'condition'
  id: string
  field: QueryVariable
  op: QueryOperator
  not: boolean
  values: string[]
}

/** A group the editor keeps as the server wrote it, shown by its line of the sentence. */
export type Kept = {
  kind: 'kept'
  id: string
  group: { any: QueryCondition[] }
  line?: QuerySentence['lines'][number]
}

export type Row = Condition | Kept

/** One criteria: mail meets it when it meets every row. None matches every message. */
export type Criteria = { id: string; rows: Row[] }

/** A field of the language, the operators it takes, and how its values are typed. */
export type FieldSpec = {
  field: QueryVariable
  label: string
  /** Never empty: the first is where a new condition of the field starts. */
  ops: [QueryOperator, ...QueryOperator[]]
  /** A value's placeholder; none for `exists`. */
  placeholder?: string
}

/** Every field, in the order the editor offers them, with the operators the language allows for it (ADR 0027). */
const messageField: FieldSpec = {
  field: 'message',
  label: 'The message',
  ops: ['about', 'mentions'],
  placeholder: 'my health',
}

export const fields: readonly FieldSpec[] = [
  messageField,
  { field: 'subject', label: 'Subject', ops: ['contains', 'about'], placeholder: 'invoice' },
  { field: 'body', label: 'Text', ops: ['contains'], placeholder: 'Calle Falsa 123' },
  {
    field: 'from_address',
    label: 'Sender address',
    ops: ['is'],
    placeholder: 'ana@example.org',
  },
  { field: 'from_domain', label: 'Sender domain', ops: ['is'], placeholder: 'example.org' },
  {
    field: 'recipient_address',
    label: 'Recipient address',
    ops: ['is'],
    placeholder: 'me@example.org',
  },
  {
    field: 'recipient_domain',
    label: 'Recipient domain',
    ops: ['is'],
    placeholder: 'example.org',
  },
  { field: 'signed_by', label: 'Signed by', ops: ['is'], placeholder: 'bank.example' },
  { field: 'attachments', label: 'Attachments', ops: ['exists'] },
  {
    field: 'attachment_name',
    label: 'Attachment name',
    ops: ['contains'],
    placeholder: 'payslip',
  },
  {
    field: 'attachment_type',
    label: 'Attachment type',
    ops: ['is'],
    placeholder: 'application/pdf',
  },
  { field: 'list', label: 'Mailing list', ops: ['is', 'exists'], placeholder: 'news.example.org' },
  {
    field: 'received',
    label: 'Received',
    ops: ['since', 'before'],
    placeholder: '2026-11-20T00:00:00Z',
  },
  { field: 'size', label: 'Size in bytes', ops: ['at_least', 'below'], placeholder: '5000000' },
  { field: 'mailbox', label: 'Mailbox', ops: ['is'], placeholder: 'mbx_…' },
]

/** How an operator reads, and how it reads negated; none for those the editor doesn't negate. */
export const operatorWords: Record<QueryOperator, { is: string; not?: string }> = {
  is: { is: 'is', not: "isn't" },
  contains: { is: 'contains', not: "doesn't contain" },
  exists: { is: 'there are some', not: 'there are none' },
  since: { is: 'since' },
  before: { is: 'before' },
  at_least: { is: 'is at least' },
  below: { is: 'is below' },
  about: { is: 'is about', not: "isn't about" },
  mentions: { is: 'mentions', not: "doesn't mention" },
}

/** The spec of a field. */
export function fieldSpec(field: QueryVariable): FieldSpec {
  return fields.find((f) => f.field === field) ?? messageField
}

/** Whether a model decides an operator. */
export function isModel(op: QueryOperator): boolean {
  return op === 'about' || op === 'mentions'
}

let lastId = 0

/** A row's or a criteria's own identifier, so rows keep their state while others come and go. */
export function newId(prefix: string): string {
  lastId += 1
  return `${prefix}-${lastId}`
}

/** A new condition of a field, with its first operator and no value. */
export function newCondition(field: QueryVariable): Condition {
  return {
    kind: 'condition',
    id: newId('condition'),
    field,
    op: fieldSpec(field).ops[0],
    not: false,
    values: [],
  }
}

/** Whether a condition is complete: `exists` needs no value, every other operator at least one. */
export function complete(row: Condition): boolean {
  return row.op === 'exists' || row.values.length > 0
}

function condition(row: Condition, value?: string): QueryCondition {
  return {
    var: row.field,
    op: row.op,
    ...(value === undefined ? {} : { value }),
    ...(row.not ? { not: true } : {}),
  }
}

/** The formula of one criteria: an incomplete condition is left out, a kept group goes as it was. */
export function toFormula(criteria: Criteria): QueryFormula {
  const all: { any: QueryCondition[] }[] = []
  for (const row of criteria.rows) {
    if (row.kind === 'kept') {
      all.push(row.group)
      continue
    }
    if (!complete(row)) continue
    if (row.op === 'exists') all.push({ any: [condition(row)] })
    else if (row.not) for (const v of row.values) all.push({ any: [condition(row, v)] })
    else all.push({ any: row.values.map((v) => condition(row, v)) })
  }
  return { v: 1, all }
}

/** The criteria of a draft. */
export function toCriteria(list: Criteria[]): QueryFormula[] {
  return list.map(toFormula)
}

/**
 * The editor's rows of a formula the server wrote. Groups of one field and operator become a condition; negated
 * single conditions of one field and operator are joined into one negative condition; anything else is kept, with the
 * line of `sentence` (the server's sentence of this formula) that says it.
 */
export function fromFormula(formula: QueryFormula, sentence?: QuerySentence): Criteria {
  const rows: Row[] = []
  formula.all.forEach((group, i) => {
    const [first] = group.any
    const same =
      first !== undefined &&
      group.any.every((c) => c.var === first.var && c.op === first.op && !c.not)
    if (same && first.op !== 'exists') {
      rows.push({
        kind: 'condition',
        id: newId('condition'),
        field: first.var,
        op: first.op,
        not: false,
        values: group.any.map((c) => c.value ?? ''),
      })
      return
    }
    if (first !== undefined && group.any.length === 1 && (first.op === 'exists' || first.not)) {
      const negative = first.not === true
      const joined = rows.find(
        (r): r is Condition =>
          r.kind === 'condition' &&
          negative &&
          r.not &&
          r.field === first.var &&
          r.op === first.op &&
          first.op !== 'exists',
      )
      if (joined) {
        joined.values.push(first.value ?? '')
        return
      }
      rows.push({
        kind: 'condition',
        id: newId('condition'),
        field: first.var,
        op: first.op,
        not: negative,
        values: first.op === 'exists' ? [] : [first.value ?? ''],
      })
      return
    }
    const line = sentence?.lines[i]
    rows.push({ kind: 'kept', id: newId('kept'), group, ...(line ? { line } : {}) })
  })
  return { id: newId('criteria'), rows }
}

/** The editor's criteria of a draft's, each with its line of the server's sentence. */
export function fromCriteria(
  formulas: QueryFormula[],
  sentences: QuerySentence[] = [],
): Criteria[] {
  return formulas.map((f, i) => fromFormula(f, sentences[i]))
}

/** Whether any condition of the criteria is one a model decides. */
export function usesModel(list: Criteria[]): boolean {
  return list.some((c) =>
    c.rows.some((r) =>
      r.kind === 'condition' ? isModel(r.op) : r.group.any.some((x) => isModel(x.op)),
    ),
  )
}
