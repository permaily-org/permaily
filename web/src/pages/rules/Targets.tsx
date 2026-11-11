import { Plus, Search, X } from 'lucide-react'
import { type KeyboardEvent, useEffect, useId, useRef, useState } from 'react'
import { Tile } from '../../components/ui/index.ts'
import { connectionsOf, isConnectionTarget, targetLabel, vendors } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'

type Option = {
  target: string
  label: string
  initials: string
  detail?: string
  nested: boolean
  text: string
}

/** Every vendor, and the connections of a vendor that has more than one, as the search lists them. */
const options: Option[] = vendors.flatMap((vendor) => {
  const list = connectionsOf(vendor.id)
  const own: Option = {
    target: vendor.id,
    label: vendor.name,
    initials: vendor.initials,
    detail: list.length > 1 ? `all ${list.length}` : undefined,
    nested: false,
    text: `${vendor.name} ${vendor.domain}`.toLowerCase(),
  }
  if (list.length < 2) return [own]
  return [
    own,
    ...list.map((c) => ({
      target: c.id,
      label: targetLabel(c.id),
      initials: vendor.initials,
      nested: true,
      text: `${targetLabel(c.id)} ${vendor.domain}`.toLowerCase(),
    })),
  ]
})

/**
 * What a rule applies to: vendors and connections found with a search and kept as chips. With none, it applies to every
 * connection, including ones approved later. A vendor covers all of its connections, so it replaces any picked one by one.
 */
export function TargetSearch({
  targets,
  onChange,
}: {
  targets: string[]
  onChange: (targets: string[]) => void
}) {
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState<string | null>(null)
  const listId = useId()
  const chipsRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const focusAfterRemove = useRef<number | null>(null)

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const removes = chipsRef.current?.querySelectorAll<HTMLElement>('[data-remove]')
    const next = removes?.[index] ?? removes?.[index - 1] ?? inputRef.current
    next?.focus()
  })

  const taken = (target: string) =>
    targets.includes(target) ||
    (isConnectionTarget(target) && targets.includes(target.split('/')[0]!))
  const q = query.trim().toLowerCase()
  const shown = options.filter((o) => !q || o.text.includes(q))
  const pickable = shown.filter((o) => !taken(o.target))
  const current = pickable.find((o) => o.target === active) ?? pickable[0]

  function add(target: string) {
    const kept = isConnectionTarget(target)
      ? targets
      : targets.filter((t) => t.split('/')[0] !== target)
    onChange([...kept, target])
    setQuery('')
    setActive(null)
  }

  function remove(index: number) {
    focusAfterRemove.current = index
    onChange(targets.filter((_, i) => i !== index))
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      setOpen(true)
      if (pickable.length === 0) return
      const at = current ? pickable.indexOf(current) : -1
      const next =
        event.key === 'ArrowDown'
          ? (at + 1) % pickable.length
          : (at - 1 + pickable.length) % pickable.length
      setActive(pickable[next]!.target)
    } else if (event.key === 'Enter') {
      event.preventDefault()
      if (open && current) add(current.target)
    } else if (event.key === 'Escape' && (open || query)) {
      event.preventDefault()
      if (open) setOpen(false)
      else setQuery('')
    }
  }

  return (
    <div className="flex w-full flex-col gap-2">
      <div className="flex items-center gap-1.5">
        <div className="relative flex min-w-0 flex-1 items-center">
          <Search
            aria-hidden
            size={14}
            className="pointer-events-none absolute left-2 text-ink-2"
          />
          <input
            ref={inputRef}
            role="combobox"
            aria-label="Find a vendor or connection"
            aria-expanded={open}
            aria-controls={listId}
            aria-autocomplete="list"
            aria-activedescendant={open && current ? `${listId}-${current.target}` : undefined}
            placeholder="Find a vendor or connection"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value)
              setActive(null)
              setOpen(true)
            }}
            onFocus={() => setOpen(true)}
            onBlur={() => setOpen(false)}
            onKeyDown={onKeyDown}
            className={cx(
              'h-8 w-full min-w-0 rounded-sm border border-ink bg-transparent pr-2 pl-7 text-small placeholder:text-ink-3',
              focusRing,
            )}
          />
        </div>
        <button
          type="button"
          disabled={!q || !current}
          onClick={() => current && add(current.target)}
          aria-label={q && current ? `Add ${current.label}` : 'Add'}
          className={cx(
            'inline-flex h-8 shrink-0 items-center gap-1 rounded-sm border border-ink px-2.5 text-small font-medium hover:bg-sunken disabled:border-rule disabled:text-ink-3 disabled:hover:bg-transparent',
            focusRing,
          )}
        >
          <Plus aria-hidden size={14} />
          Add
        </button>
      </div>

      {/* The results open in place, under the search, so the editor's scrolling column can't clip them. */}
      {open && (
        <div
          id={listId}
          role="listbox"
          aria-label="Vendors and connections"
          className="max-h-60 overflow-y-auto rounded-sm border border-rule-strong bg-raised py-1"
        >
          {shown.length === 0 && (
            <div role="presentation" className="px-3 py-1.5 text-small text-ink-2">
              No vendor or connection matches
            </div>
          )}
          {shown.map((option) => {
            const off = taken(option.target)
            const isActive = !off && current?.target === option.target
            return (
              <div
                key={option.target}
                id={`${listId}-${option.target}`}
                role="option"
                tabIndex={-1}
                aria-selected={isActive}
                aria-disabled={off || undefined}
                // Keeps focus in the search, so picking one leaves it open for the next.
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => !off && add(option.target)}
                onMouseEnter={() => !off && setActive(option.target)}
                className={cx(
                  'flex cursor-default items-center gap-2 py-1.5 pr-3 text-small',
                  option.nested ? 'pl-[44px]' : 'pl-3',
                  off ? 'text-ink-3' : 'text-ink',
                  isActive && 'bg-sunken',
                )}
              >
                {!option.nested && <Tile initials={option.initials} size="sm" />}
                <span className={cx('min-w-0 flex-1 truncate', !option.nested && 'font-medium')}>
                  {option.label}
                </span>
                {option.detail && (
                  <span className="font-mono text-mono text-ink-2">{option.detail}</span>
                )}
              </div>
            )
          })}
        </div>
      )}

      <div
        ref={chipsRef}
        role="group"
        aria-label="Applies to"
        className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-2"
      >
        {targets.length === 0 ? (
          <span className="text-small text-ink-2">Every connection</span>
        ) : (
          targets.map((target, i) => (
            // Like a condition's chip: cut short rather than pushing past its row, with 24px to tap around the remove icon.
            <span
              key={target}
              className="inline-flex h-[22px] max-w-full items-center gap-0.5 rounded-sm bg-sunken pr-1 pl-2 text-small has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink"
            >
              <span className="min-w-0 truncate">{targetLabel(target)}</span>
              <button
                type="button"
                data-remove
                onClick={() => remove(i)}
                aria-label={`Remove ${targetLabel(target)}`}
                className="relative inline-flex size-3.5 shrink-0 items-center justify-center rounded-[1px] text-ink-2 outline-none before:absolute before:-inset-[5px] hover:bg-rule hover:text-ink"
              >
                <X aria-hidden size={12} strokeWidth={2} />
              </button>
            </span>
          ))
        )}
      </div>
    </div>
  )
}
