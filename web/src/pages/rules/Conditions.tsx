import { Plus, X } from 'lucide-react'
import { Fragment, useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { TextAction } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { withheldRuleNames, type ConditionBlock, type GroupKind } from './data.ts'

const placeholders: Record<GroupKind, string> = {
  data: 'type of data',
  type: 'kind of mail',
  sender: 'domain or address',
  subject: 'word',
  header: 'header name',
  label: 'Gmail label',
  mailbox: 'address',
}

export function Conditions({
  blocks,
  butNot,
  packChips,
  readOnly,
  onBlocksChange,
  onButNotChange,
}: {
  blocks: ConditionBlock[]
  butNot: string[]
  /** Chips that came from the pack. Anything else in a positive group is outlined as your own. */
  packChips: Set<string> | null
  readOnly?: boolean
  onBlocksChange: (blocks: ConditionBlock[]) => void
  onButNotChange: (butNot: string[]) => void
}) {
  const matchId = useId()
  const notId = useId()

  function setChips(bi: number, gi: number, chips: string[]) {
    onBlocksChange(blocks.map((block, b) => (b !== bi ? block : block.map((group, g) => (g !== gi ? group : { ...group, chips })))))
  }

  return (
    <>
      <div role="group" aria-labelledby={matchId}>
        <p id={matchId} className="mb-1.5 text-small text-ink-2">
          Match mail that
        </p>
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
                  isOutlined={(chip) => Boolean(packChips && !packChips.has(chip.toLowerCase()))}
                  readOnly={readOnly}
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
          {readOnly ? (
            <GroupRow label="is withheld by" kind="sender" chips={withheldRuleNames} readOnly onChange={() => {}} />
          ) : (
            <GroupRow label="from" kind="sender" chips={butNot} onChange={onButNotChange} />
          )}
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
  isOutlined,
  readOnly,
  onChange,
}: {
  label: string
  kind: GroupKind
  chips: string[]
  collapseAfter?: number
  isOutlined?: (chip: string) => boolean
  readOnly?: boolean
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

  return (
    <div role="group" aria-label={label} className="flex items-start gap-3">
      <span className="w-[98px] shrink-0 py-0.5 text-small whitespace-nowrap">{label}</span>
      <div ref={listRef} className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-2">
        {visible.map((chip, i) => (
          <Chip key={chip} value={chip} outlined={isOutlined?.(chip)} onRemove={readOnly ? undefined : () => remove(i)} />
        ))}
        {hidden > 0 && (
          <TextAction onClick={() => setExpanded(true)}>
            and {hidden} more
          </TextAction>
        )}
        {!readOnly && <AddChip label={`Add to ${label}`} placeholder={placeholders[kind]} onAdd={add} />}
      </div>
    </div>
  )
}

function Chip({ value, outlined, onRemove }: { value: string; outlined?: boolean; onRemove?: () => void }) {
  return (
    <span
      className={cx(
        'inline-flex h-[22px] items-center gap-0.5 rounded-sm bg-sunken font-mono text-mono text-ink',
        outlined ? 'border border-ink pl-[7px]' : 'pl-2',
        onRemove ? (outlined ? 'pr-[3px]' : 'pr-1') : outlined ? 'pr-[7px]' : 'pr-2',
        'has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink',
      )}
    >
      {value}
      {outlined && <span className="sr-only">, your own</span>}
      {onRemove && (
        <button
          type="button"
          data-remove
          onClick={onRemove}
          aria-label={`Remove ${value}`}
          className="inline-flex size-3.5 items-center justify-center rounded-[1px] text-ink-2 outline-none hover:bg-rule hover:text-ink"
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
        className={cx('h-[22px] w-40 rounded-sm border border-ink bg-transparent px-1.5 font-mono text-mono placeholder:text-ink-3', focusRing)}
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
      className={cx('inline-flex size-[22px] items-center justify-center rounded-sm border border-rule text-ink-2 hover:border-ink hover:text-ink', focusRing)}
    >
      <Plus aria-hidden size={14} />
    </button>
  )
}
