import { ChevronDown, Plus, X } from 'lucide-react'
import { Fragment, useId, useState } from 'react'
import { Button, TextAction } from '../../components/ui/index.ts'
import type * as api from '../../lib/api.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Sentence } from '../connections/words.tsx'
import {
  type Condition,
  type Criteria,
  fieldSpec,
  fields,
  isModel,
  newCondition,
  newId,
  operatorWords,
  type Row,
} from './criteria.ts'

/**
 * What a rule matches: any of its criteria, each a set of conditions that must all hold, each a field, how it compares,
 * and values, any of which may match (ADR 0027's language, its variables and operators; no "Kind" or "Label"). A group a
 * proposal wrote that the editor can't show as one condition is shown by the server's own words, and can be removed.
 */
export function CriteriaEditor({
  criteria,
  mailboxes,
  onChange,
}: {
  criteria: Criteria[]
  mailboxes: api.Mailbox[]
  onChange: (criteria: Criteria[]) => void
}) {
  const titleId = useId()

  function update(gi: number, rows: Row[]) {
    onChange(criteria.map((c, i) => (i === gi ? { ...c, rows } : c)))
  }

  return (
    <div role="group" aria-labelledby={titleId}>
      <p id={titleId} className="mb-1.5 text-small text-ink-2">
        Matches mail where
      </p>
      {criteria.length === 0 && (
        <p className="mb-2 text-small text-ink-2">
          No criteria yet. A rule needs at least one; criteria with no condition match all mail.
        </p>
      )}
      {criteria.map((group, gi) => (
        <Fragment key={group.id}>
          {gi > 0 && <p className="py-2 font-mono text-mono">or</p>}
          <div
            role="group"
            aria-label={`Criteria ${gi + 1}, where every condition holds`}
            className="rounded-sm border border-rule"
          >
            {group.rows.length === 0 && (
              <p className="px-3 pt-2.5 text-small text-ink-2">All mail</p>
            )}
            {group.rows.map((row, ri) => (
              <Fragment key={row.id}>
                {ri > 0 && (
                  <div className="flex items-center gap-2 px-3">
                    <span className="font-mono text-micro text-ink-2">and</span>
                    <span aria-hidden className="h-px flex-1 bg-rule" />
                  </div>
                )}
                {row.kind === 'condition' ? (
                  <ConditionRow
                    row={row}
                    mailboxes={mailboxes}
                    onChange={(next) =>
                      update(
                        gi,
                        group.rows.map((r) => (r.id === row.id ? next : r)),
                      )
                    }
                    onRemove={() =>
                      update(
                        gi,
                        group.rows.filter((r) => r.id !== row.id),
                      )
                    }
                  />
                ) : (
                  <div className="flex items-start gap-2 px-3 py-2.5">
                    <span className="min-w-0 flex-1 text-small">
                      <Sentence
                        sentence={{
                          lang: 'en',
                          text: '',
                          lead: [],
                          lines: row.line
                            ? [row.line]
                            : [{ parts: [{ text: 'A condition the model proposed' }] }],
                        }}
                      />
                    </span>
                    <RemoveButton
                      label="Remove condition"
                      onClick={() =>
                        update(
                          gi,
                          group.rows.filter((r) => r.id !== row.id),
                        )
                      }
                    />
                  </div>
                )}
              </Fragment>
            ))}
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 px-3 pt-1 pb-2">
              <TextAction onClick={() => update(gi, [...group.rows, newCondition('message')])}>
                <span className="inline-flex items-center gap-1">
                  <Plus aria-hidden size={14} />
                  And<span className="sr-only">: add a condition to criteria {gi + 1}</span>
                </span>
              </TextAction>
              <TextAction
                tone="danger"
                onClick={() => onChange(criteria.filter((c) => c.id !== group.id))}
              >
                Remove criteria {gi + 1}
              </TextAction>
            </div>
          </div>
        </Fragment>
      ))}
      <div className="mt-2">
        <Button
          variant="secondary"
          size="sm"
          onClick={() =>
            onChange([...criteria, { id: newId('criteria'), rows: [newCondition('message')] }])
          }
        >
          <Plus aria-hidden size={14} />
          {criteria.length === 0 ? 'Add criteria' : 'Or: add criteria'}
        </Button>
      </div>
    </div>
  )
}

function ConditionRow({
  row,
  mailboxes,
  onChange,
  onRemove,
}: {
  row: Condition
  mailboxes: api.Mailbox[]
  onChange: (row: Condition) => void
  onRemove: () => void
}) {
  const spec = fieldSpec(row.field)
  const words = operatorWords[row.op]
  const name = `${spec.label} ${row.not ? words.not : words.is}`
  const ops = spec.ops.flatMap((op) => {
    const { is, not } = operatorWords[op]
    return [{ value: op, label: is }, ...(not ? [{ value: `not:${op}`, label: not }] : [])]
  })
  return (
    <div role="group" aria-label={name} className="flex items-start gap-2 px-3 py-2.5">
      <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-2">
        <Select
          label="Field"
          value={row.field}
          options={fields.map((f) => ({ value: f.field, label: f.label }))}
          onChange={(field) => {
            const next = fieldSpec(field as api.QueryVariable)
            onChange({
              ...row,
              field: next.field,
              op: next.ops[0],
              not: false,
              values: [],
            })
          }}
        />
        <Select
          label="Compares"
          value={row.not ? `not:${row.op}` : row.op}
          options={ops}
          onChange={(value) => {
            const not = value.startsWith('not:')
            const op = (not ? value.slice(4) : value) as api.QueryOperator
            onChange({ ...row, op, not, values: op === 'exists' ? [] : row.values })
          }}
        />
        {isModel(row.op) && (
          <span className="font-mono text-micro text-ink-2">(a model decides)</span>
        )}
        {row.op !== 'exists' &&
          row.values.map((value, i) => (
            <span
              // biome-ignore lint/suspicious/noArrayIndexKey: a value is listed once, but two may read the same while typed
              key={`${i}:${value}`}
              className="inline-flex h-[22px] max-w-full items-center gap-0.5 rounded-sm bg-sunken pr-1 pl-2 text-small has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink"
            >
              <bdi className="min-w-0 truncate">{mailboxName(row, value, mailboxes)}</bdi>
              <button
                type="button"
                aria-label={`Remove ${mailboxName(row, value, mailboxes)} from ${name}`}
                onClick={() => onChange({ ...row, values: row.values.filter((_, j) => j !== i) })}
                className="relative inline-flex size-3.5 shrink-0 items-center justify-center rounded-[1px] text-ink-2 outline-none before:absolute before:-inset-[5px] hover:bg-rule hover:text-ink"
              >
                <X aria-hidden size={12} strokeWidth={2} />
              </button>
            </span>
          ))}
        {row.op !== 'exists' && (
          <AddValue
            row={row}
            name={name}
            placeholder={spec.placeholder ?? ''}
            mailboxes={mailboxes}
            onAdd={(value) => {
              if (row.values.includes(value)) return
              onChange({ ...row, values: [...row.values, value] })
            }}
          />
        )}
      </div>
      <RemoveButton label={`Remove condition: ${name}`} onClick={onRemove} />
    </div>
  )
}

/** A mailbox value by its label, as the person knows it; every other value as written. */
function mailboxName(row: Condition, value: string, mailboxes: api.Mailbox[]): string {
  if (row.field !== 'mailbox') return value
  const mailbox = mailboxes.find((m) => m.id === value)
  return mailbox ? mailbox.label || mailbox.address || value : value
}

/** Adds a value: typed and added with Enter or "Add", or for a mailbox chosen from the account's. */
function AddValue({
  row,
  name,
  placeholder,
  mailboxes,
  onAdd,
}: {
  row: Condition
  name: string
  placeholder: string
  mailboxes: api.Mailbox[]
  onAdd: (value: string) => void
}) {
  const [value, setValue] = useState('')
  if (row.field === 'mailbox' && mailboxes.length > 0) {
    return (
      <Select
        label={`Add to ${name}`}
        value=""
        options={[
          { value: '', label: 'Add a mailbox' },
          ...mailboxes.map((m) => ({ value: m.id, label: m.label || m.address || m.id })),
        ]}
        onChange={(id) => {
          if (id) onAdd(id)
        }}
      />
    )
  }
  function add() {
    const v = value.trim()
    if (!v) return
    onAdd(v)
    setValue('')
  }
  return (
    <span className="inline-flex items-center gap-1">
      <input
        aria-label={`Add to ${name}`}
        value={value}
        placeholder={placeholder}
        maxLength={256}
        onChange={(event) => setValue(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault()
            add()
          }
        }}
        className={cx(
          'h-[26px] w-44 rounded-sm border border-rule bg-transparent px-2 text-small placeholder:text-ink-3 hover:border-ink',
          focusRing,
        )}
      />
      <TextAction onClick={add}>
        Add <span className="sr-only">to {name}</span>
      </TextAction>
    </span>
  )
}

function RemoveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className={cx(
        'inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-ink-2 hover:bg-sunken hover:text-ink',
        focusRing,
      )}
    >
      <X aria-hidden size={14} />
    </button>
  )
}

/** A compact native select, so it works with a keyboard and opens the phone's own picker. */
export function Select({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string
  options: { value: string; label: string }[]
  onChange: (value: string) => void
}) {
  return (
    <span className="relative inline-flex">
      <select
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={cx(
          'h-[26px] appearance-none rounded-sm border border-rule bg-transparent pr-7 pl-2 text-small text-ink hover:border-ink',
          focusRing,
        )}
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
      <ChevronDown
        aria-hidden
        size={14}
        className="pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 text-ink-2"
      />
    </span>
  )
}
