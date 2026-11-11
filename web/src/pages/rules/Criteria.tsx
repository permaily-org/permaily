import { ChevronDown, Plus, X } from 'lucide-react'
import { Fragment, useEffect, useId, useRef, useState } from 'react'
import { InfoTip, TextAction } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import {
  type CriteriaGroup,
  type Criterion,
  type Field,
  fieldLabel,
  fields,
  type Operator,
  operators,
  starters,
  valueStyle,
  withField,
} from './data.ts'
import { MenuButton, MenuItem } from './Menu.tsx'
import { AddValue, Chip } from './Values.tsx'

let lastId = 0

/** Conditions and groups added in the editor get ids of their own, so rows keep their state when others come and go. */
function newId(prefix: string): string {
  lastId += 1
  return `${prefix}-${lastId}`
}

/**
 * What a rule matches: criteria, each a set of conditions people can name. Mail matches when it meets every condition of
 * any one criteria, so criteria read with "and" inside and "or" between them. A condition is a field, how it's compared,
 * and values.
 */
export function Criteria({
  groups,
  onChange,
}: {
  groups: CriteriaGroup[]
  onChange: (groups: CriteriaGroup[]) => void
}) {
  const titleId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  // A condition just added opens its value field. After one is removed, focus moves to the next, or the one before.
  const [opening, setOpening] = useState<string | null>(null)
  const focusAfterRemove = useRef<number | null>(null)
  const usesModel = groups.some((g) =>
    g.criteria.some((c) => c.values.length > 0 && valueStyle(c).model),
  )

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const rows = rootRef.current?.querySelectorAll<HTMLElement>('select[data-field]')
    const adds = rootRef.current?.querySelectorAll<HTMLElement>('[data-add]')
    const next = rows?.[index] ?? rows?.[index - 1] ?? adds?.[adds.length - 1]
    next?.focus()
  })

  function add(groupIndex: number | null, field: Field, operator: Operator) {
    const criterion: Criterion = { id: newId('condition'), field, operator, values: [] }
    setOpening(criterion.id)
    onChange(
      groupIndex === null
        ? [...groups, { id: newId('criteria'), name: '', criteria: [criterion] }]
        : groups.map((g, i) =>
            i === groupIndex ? { ...g, criteria: [...g.criteria, criterion] } : g,
          ),
    )
  }

  function rename(groupIndex: number, name: string) {
    onChange(groups.map((g, i) => (i === groupIndex ? { ...g, name } : g)))
  }

  function update(groupIndex: number, next: Criterion) {
    onChange(
      groups.map((g, i) =>
        i === groupIndex
          ? { ...g, criteria: g.criteria.map((c) => (c.id === next.id ? next : c)) }
          : g,
      ),
    )
  }

  function remove(groupIndex: number, index: number) {
    focusAfterRemove.current =
      groups.slice(0, groupIndex).reduce((n, g) => n + g.criteria.length, 0) + index
    const criteria = groups[groupIndex]!.criteria.filter((_, i) => i !== index)
    // Criteria left without conditions go too.
    onChange(
      criteria.length > 0
        ? groups.map((g, i) => (i === groupIndex ? { ...g, criteria } : g))
        : groups.filter((_, i) => i !== groupIndex),
    )
  }

  return (
    <div ref={rootRef} role="group" aria-labelledby={titleId}>
      <div className="mb-1.5 flex items-center gap-1.5">
        <p id={titleId} className="text-small text-ink-2">
          Match mail where
        </p>
        {usesModel && (
          <InfoTip label="How words and topics are checked" align="start">
            Words and topics are checked by a model inside Permaily's verified service. Your mail
            doesn't leave it.
          </InfoTip>
        )}
      </div>

      {groups.map((group, gi) => {
        const title = group.name.trim() || `Criteria ${gi + 1}`
        return (
          <Fragment key={group.id}>
            {gi > 0 && <p className="py-2 font-mono text-mono">or</p>}
            <div
              role="group"
              aria-label={`${title}, where every condition matches`}
              className="rounded-sm border border-rule"
            >
              {/* The name reads as a title, and turns into a field on hover or focus. */}
              <div className="px-3 pt-2">
                <input
                  aria-label={`Name of criteria ${gi + 1}`}
                  placeholder={`Criteria ${gi + 1}`}
                  value={group.name}
                  onChange={(event) => rename(gi, event.target.value)}
                  className={cx(
                    '-mx-1 w-[calc(100%+8px)] rounded-sm border border-transparent bg-transparent px-1 py-0.5 text-small font-medium text-ink placeholder:text-ink-3 hover:border-rule',
                    focusRing,
                  )}
                />
              </div>
              {group.criteria.map((criterion, ci) => (
                <Fragment key={criterion.id}>
                  {ci > 0 && <Joiner word="and" />}
                  <CriterionRow
                    criterion={criterion}
                    opening={opening === criterion.id}
                    onChange={(next) => update(gi, next)}
                    onRemove={() => remove(gi, ci)}
                  />
                </Fragment>
              ))}
              <div className="px-3 pb-2">
                <AddMenu
                  text="And"
                  label={`And: add a condition to ${title}`}
                  onPick={(field, operator) => add(gi, field, operator)}
                />
              </div>
            </div>
          </Fragment>
        )
      })}

      {/* New criteria start with the condition picked here, and match as an alternative to the others. */}
      <div className={groups.length > 0 ? 'mt-2' : undefined}>
        <AddMenu
          text="Add criteria"
          label="Add criteria"
          onPick={(field, operator) => add(null, field, operator)}
        />
      </div>
    </div>
  )
}

/** "and" on the rule between two conditions of a group. */
function Joiner({ word }: { word: string }) {
  return (
    <div className="flex items-center gap-2 px-3">
      <span className="font-mono text-micro text-ink-2">{word}</span>
      <span aria-hidden className="h-px flex-1 bg-rule" />
    </div>
  )
}

/** Starts a condition from the ones people usually need, each with a value it could take. */
function AddMenu({
  text,
  label,
  onPick,
}: {
  text: string
  label: string
  onPick: (field: Field, operator: Operator) => void
}) {
  return (
    <MenuButton
      label={label}
      buttonLabel={label}
      button={
        <>
          <Plus aria-hidden size={14} />
          {text}
        </>
      }
      buttonClassName={cx(
        '-ml-1.5 inline-flex h-7 items-center gap-1.5 rounded-sm px-1.5 text-small font-medium text-ink hover:bg-sunken',
        focusRing,
      )}
    >
      {starters.map((s) => (
        // The new row opens its value field, so focus goes there rather than back to the button.
        <MenuItem
          key={`${s.field}:${s.operator}`}
          refocus={false}
          onSelect={() => onPick(s.field, s.operator)}
        >
          <span className="min-w-0 flex-1 truncate">
            {fieldLabel(s.field)} {operators[s.operator].label}
          </span>
          <span
            className={cx(
              'shrink-0 text-ink-2',
              valueStyle(s).literal ? 'font-mono text-mono' : 'text-small',
            )}
          >
            {s.example}
          </span>
        </MenuItem>
      ))}
    </MenuButton>
  )
}

function CriterionRow({
  criterion,
  opening,
  onChange,
  onRemove,
}: {
  criterion: Criterion
  opening: boolean
  onChange: (criterion: Criterion) => void
  onRemove: () => void
}) {
  const style = valueStyle(criterion)
  const allowed = fields.find((f) => f.field === criterion.field)?.operators ?? []
  const name = `${fieldLabel(criterion.field)} ${operators[criterion.operator].label}`
  const [expanded, setExpanded] = useState(false)
  const focusAfterRemove = useRef<number | null>(null)
  const valuesRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const removes = valuesRef.current?.querySelectorAll<HTMLElement>('[data-remove]')
    const next =
      removes?.[index] ??
      removes?.[index - 1] ??
      valuesRef.current?.querySelector<HTMLElement>('[data-add-value]')
    next?.focus()
  })

  // A long list of senders shows the first few until asked for the rest. A new sender goes first, so it's in view.
  const collapseAfter = criterion.field === 'sender' ? 4 : undefined
  const hidden =
    collapseAfter && !expanded ? Math.max(0, criterion.values.length - collapseAfter) : 0
  const visible = hidden > 0 ? criterion.values.slice(0, collapseAfter) : criterion.values

  function add(value: string) {
    if (criterion.values.some((v) => v.toLowerCase() === value.toLowerCase())) return
    onChange({
      ...criterion,
      values:
        criterion.field === 'sender' ? [value, ...criterion.values] : [...criterion.values, value],
    })
  }

  function removeValue(index: number) {
    focusAfterRemove.current = index
    onChange({ ...criterion, values: criterion.values.filter((_, i) => i !== index) })
  }

  // Field, operator and values read as one line, and the values wrap under it when they don't fit.
  return (
    <div role="group" aria-label={name} className="flex items-start gap-2 px-3 py-2.5">
      <div ref={valuesRef} className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-2">
        <Select
          label="Field"
          marker
          value={criterion.field}
          options={fields.map((f) => ({ value: f.field, label: f.label }))}
          onChange={(field) => onChange(withField(criterion, field))}
        />
        <Select
          label="Operator"
          value={criterion.operator}
          options={allowed.map((o) => ({ value: o, label: operators[o].label }))}
          onChange={(operator) => onChange({ ...criterion, operator })}
        />
        {visible.map((value, i) => (
          <Chip key={value} value={value} literal={style.literal} onRemove={() => removeValue(i)} />
        ))}
        {hidden > 0 && <TextAction onClick={() => setExpanded(true)}>and {hidden} more</TextAction>}
        <AddValue
          label={`Add to ${name}`}
          placeholder={style.placeholder}
          literal={style.literal}
          suggestions={style.suggestions}
          autoOpen={opening}
          onAdd={add}
        />
      </div>
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove condition: ${name}`}
        className={cx(
          'inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-ink-2 hover:bg-sunken hover:text-ink',
          focusRing,
        )}
      >
        <X aria-hidden size={14} />
      </button>
    </div>
  )
}

/** A compact native select, so it works with a keyboard and opens the phone's own picker. */
function Select<T extends string>({
  label,
  marker = false,
  value,
  options,
  onChange,
}: {
  label: string
  /** Marks the row's first control, where focus lands after a row is removed. */
  marker?: boolean
  value: T
  options: { value: T; label: string }[]
  onChange: (value: T) => void
}) {
  return (
    <span className="relative inline-flex">
      <select
        data-field={marker || undefined}
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.target.value as T)}
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
