import { Plus, X } from 'lucide-react'
import { useEffect, useId, useRef, useState } from 'react'
import { cx, focusRing } from '../../lib/styles.ts'

/**
 * Values as chips with a + to add more, for a partial rule's attachments and data. empty is what shows when there are
 * none, such as "All". The one just switched on can open its field straight away.
 */
export function ValueList({
  label,
  values,
  onChange,
  placeholder,
  literal = false,
  suggestions,
  empty,
  autoOpen = false,
}: {
  label: string
  values: string[]
  onChange: (values: string[]) => void
  placeholder: string
  literal?: boolean
  suggestions?: string[]
  empty?: string
  autoOpen?: boolean
}) {
  const listRef = useRef<HTMLDivElement>(null)
  const focusAfterRemove = useRef<number | null>(null)

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const removes = listRef.current?.querySelectorAll<HTMLElement>('[data-remove]')
    const next =
      removes?.[index] ??
      removes?.[index - 1] ??
      listRef.current?.querySelector<HTMLElement>('[data-add-value]')
    next?.focus()
  })

  function add(value: string) {
    if (values.some((v) => v.toLowerCase() === value.toLowerCase())) return
    onChange([...values, value])
  }

  function remove(index: number) {
    focusAfterRemove.current = index
    onChange(values.filter((_, i) => i !== index))
  }

  return (
    <div
      ref={listRef}
      role="group"
      aria-label={label}
      className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-2"
    >
      {values.length === 0 && empty && <span className="text-small text-ink-2">{empty}</span>}
      {values.map((value, i) => (
        <Chip key={value} value={value} literal={literal} onRemove={() => remove(i)} />
      ))}
      <AddValue
        label={`Add to ${label.toLowerCase()}`}
        placeholder={placeholder}
        literal={literal}
        suggestions={suggestions}
        autoOpen={autoOpen}
        onAdd={add}
      />
    </div>
  )
}

/** A long value is cut short rather than pushing past its row. The remove button reaches 24px around its icon. */
export function Chip({
  value,
  literal,
  onRemove,
}: {
  value: string
  literal: boolean
  onRemove: () => void
}) {
  return (
    <span
      className={cx(
        'inline-flex h-[22px] max-w-full items-center gap-0.5 rounded-sm bg-sunken pr-1 pl-2 text-ink',
        literal ? 'font-mono text-mono' : 'text-small',
        'has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink',
      )}
    >
      <span className="min-w-0 truncate">{value}</span>
      <button
        type="button"
        data-remove
        onClick={onRemove}
        aria-label={`Remove ${value}`}
        className="relative inline-flex size-3.5 shrink-0 items-center justify-center rounded-[1px] text-ink-2 outline-none before:absolute before:-inset-[5px] hover:bg-rule hover:text-ink"
      >
        <X aria-hidden size={12} strokeWidth={2} />
      </button>
    </span>
  )
}

/** Adds values one at a time: type one and press Enter, then the next. Escape, or leaving the field, closes it. */
export function AddValue({
  label,
  placeholder,
  literal,
  suggestions,
  autoOpen = false,
  onAdd,
}: {
  label: string
  placeholder: string
  literal: boolean
  suggestions?: string[]
  autoOpen?: boolean
  onAdd: (value: string) => void
}) {
  const [open, setOpen] = useState(autoOpen)
  const [value, setValue] = useState('')
  const listId = useId()
  const returnFocus = useRef(false)
  const skipBlur = useRef(false)
  const buttonRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (open || !returnFocus.current) return
    returnFocus.current = false
    buttonRef.current?.focus()
  }, [open])

  function commit(next: string) {
    const trimmed = next.trim()
    if (trimmed) onAdd(trimmed)
    setValue('')
  }

  if (open) {
    return (
      <>
        <input
          autoFocus
          aria-label={label}
          placeholder={placeholder}
          list={suggestions ? listId : undefined}
          value={value}
          onChange={(event) => {
            // Picking a suggestion adds it straight away. Typing waits for Enter.
            const native = event.nativeEvent
            const picked =
              !(native instanceof InputEvent) || native.inputType === 'insertReplacementText'
            if (suggestions?.includes(event.target.value) && picked) commit(event.target.value)
            else setValue(event.target.value)
          }}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault()
              commit(value)
            }
            if (event.key === 'Escape') {
              event.preventDefault()
              skipBlur.current = true
              setValue('')
              returnFocus.current = true
              setOpen(false)
            }
          }}
          onBlur={() => {
            if (skipBlur.current) {
              skipBlur.current = false
              return
            }
            commit(value)
            setOpen(false)
          }}
          className={cx(
            'h-[22px] w-44 max-w-full rounded-sm border border-ink bg-transparent px-1.5 placeholder:text-ink-3',
            literal ? 'font-mono text-mono' : 'text-small',
            focusRing,
          )}
        />
        {suggestions && (
          <datalist id={listId}>
            {suggestions.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        )}
      </>
    )
  }

  return (
    <button
      ref={buttonRef}
      type="button"
      data-add-value
      aria-label={label}
      onClick={() => setOpen(true)}
      className={cx(
        'relative inline-flex size-[22px] items-center justify-center rounded-sm border border-rule text-ink-2 before:absolute before:-inset-0.5 hover:border-ink hover:text-ink',
        focusRing,
      )}
    >
      <Plus aria-hidden size={14} />
    </button>
  )
}
