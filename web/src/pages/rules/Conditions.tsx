import { Plus, X } from 'lucide-react'
import { Fragment, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { InfoTip, TextAction } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import type { ConditionBlock, GroupKind } from './data.ts'

const placeholders: Record<GroupKind, string> = {
  about: 'in your own words',
  text: 'word or phrase',
  type: 'kind of mail',
  sender: 'domain or address',
  subject: 'word',
  label: 'Gmail label',
}

export function Conditions({
  blocks,
  butNot,
  onBlocksChange,
  onButNotChange,
}: {
  blocks: ConditionBlock[]
  butNot: string[]
  onBlocksChange: (blocks: ConditionBlock[]) => void
  onButNotChange: (butNot: string[]) => void
}) {
  const matchId = useId()
  const notId = useId()
  const usesModel = blocks.some((block) => block.some((g) => (g.kind === 'about' || g.kind === 'text') && g.chips.length > 0))

  function setChips(bi: number, gi: number, chips: string[]) {
    onBlocksChange(blocks.map((block, b) => (b !== bi ? block : block.map((group, g) => (g !== gi ? group : { ...group, chips })))))
  }

  return (
    <>
      <div role="group" aria-labelledby={matchId}>
        <div className="mb-1.5 flex items-center gap-1.5">
          <p id={matchId} className="text-small text-ink-2">
            Match mail that
          </p>
          {usesModel && (
            <InfoTip label="How words and topics are checked" align="start">
              Words and topics are checked by a model inside Permaily's verified service. Your mail doesn't leave it.
            </InfoTip>
          )}
        </div>
        {blocks.map((block, bi) => (
          <Fragment key={bi}>
            {bi > 0 && <p className="px-3 py-1.5 font-mono text-mono">or</p>}
            <Box>
              {block.map((group, gi) => (
                <GroupRow
                  key={group.id}
                  label={gi > 0 ? `and ${group.label}` : group.label}
                  kind={group.kind}
                  chips={group.chips}
                  collapseAfter={group.kind === 'sender' ? 4 : undefined}
                  onChange={(chips) => setChips(bi, gi, chips)}
                />
              ))}
            </Box>
          </Fragment>
        ))}
      </div>

      <div role="group" aria-labelledby={notId} className="mt-4">
        <p id={notId} className="mb-1.5 text-small text-ink-2">
          But not
        </p>
        <Box>
          <GroupRow label="from" kind="sender" chips={butNot} onChange={onButNotChange} />
        </Box>
      </div>

    </>
  )
}

function Box({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-2.5 rounded-sm border border-rule p-3">{children}</div>
}

function GroupRow({
  label,
  kind,
  chips,
  collapseAfter,
  onChange,
}: {
  label: string
  kind: GroupKind
  chips: string[]
  collapseAfter?: number
  onChange: (chips: string[]) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const focusAfterRemove = useRef<number | null>(null)
  const listRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const removes = listRef.current?.querySelectorAll<HTMLElement>('[data-remove]')
    const next = removes?.[index] ?? removes?.[index - 1] ?? listRef.current?.querySelector<HTMLElement>('[data-add]')
    next?.focus()
  })

  const hidden = collapseAfter && !expanded ? Math.max(0, chips.length - collapseAfter) : 0
  const visible = hidden > 0 ? chips.slice(0, collapseAfter) : chips

  function add(value: string) {
    if (chips.some((c) => c.toLowerCase() === value.toLowerCase())) return
    onChange(kind === 'sender' ? [value, ...chips] : [...chips, value])
  }

  function remove(index: number) {
    focusAfterRemove.current = index
    onChange(chips.filter((_, i) => i !== index))
  }

  // On a phone the label goes above its chips, so they get the whole width.
  return (
    <div role="group" aria-label={label} className="flex flex-col gap-1.5 sm:flex-row sm:items-start sm:gap-3">
      <span className="text-small whitespace-nowrap sm:w-[98px] sm:shrink-0 sm:py-0.5">{label}</span>
      <div ref={listRef} className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-2">
        {visible.map((chip, i) => (
          <Chip key={chip} value={chip} onRemove={() => remove(i)} />
        ))}
        {hidden > 0 && (
          <TextAction onClick={() => setExpanded(true)}>
            and {hidden} more
          </TextAction>
        )}
        <AddChip label={`Add to ${label}`} placeholder={placeholders[kind]} onAdd={add} />
      </div>
    </div>
  )
}

/** A long value is cut short rather than pushing past its row. The remove button reaches 24px around its icon. */
function Chip({ value, onRemove }: { value: string; onRemove?: () => void }) {
  return (
    <span
      className={cx(
        'inline-flex h-[22px] max-w-full items-center gap-0.5 rounded-sm bg-sunken pl-2 font-mono text-mono text-ink',
        onRemove ? 'pr-1' : 'pr-2',
        'has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink',
      )}
    >
      <span className="min-w-0 truncate">{value}</span>
      {onRemove && (
        <button
          type="button"
          data-remove
          onClick={onRemove}
          aria-label={`Remove ${value}`}
          className="relative inline-flex size-3.5 shrink-0 items-center justify-center rounded-[1px] text-ink-2 outline-none before:absolute before:-inset-[5px] hover:bg-rule hover:text-ink"
        >
          <X aria-hidden size={12} strokeWidth={2} />
        </button>
      )}
    </span>
  )
}

function AddChip({ label, placeholder, onAdd }: { label: string; placeholder: string; onAdd: (value: string) => void }) {
  const [open, setOpen] = useState(false)
  const [value, setValue] = useState('')
  const returnFocus = useRef(false)
  const skipBlur = useRef(false)
  const buttonRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (open || !returnFocus.current) return
    returnFocus.current = false
    buttonRef.current?.focus()
  }, [open])

  function commit() {
    const trimmed = value.trim()
    if (trimmed) onAdd(trimmed)
    setValue('')
  }

  if (open) {
    return (
      <input
        autoFocus
        aria-label={label}
        placeholder={placeholder}
        value={value}
        onChange={(event) => setValue(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault()
            commit()
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
          commit()
          setOpen(false)
        }}
        className={cx('h-[22px] w-40 max-w-full rounded-sm border border-ink bg-transparent px-1.5 font-mono text-mono placeholder:text-ink-3', focusRing)}
      />
    )
  }

  return (
    <button
      ref={buttonRef}
      type="button"
      data-add
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
