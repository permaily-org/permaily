import { Check, ChevronDown } from 'lucide-react'
import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { mailboxes, type Mailbox } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'

const box = 'inline-flex max-w-full items-center gap-1.5 rounded-sm border border-rule px-2.5 py-[5px] font-mono text-mono'

/** The mailbox an access belongs to, as a small menu. */
export function MailboxPicker({ value, onChange }: { value: Mailbox['id']; onChange: (id: Mailbox['id']) => void }) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const trigger = useRef<HTMLButtonElement>(null)
  const items = useRef<Array<HTMLButtonElement | null>>([])
  const current = mailboxes.find((m) => m.id === value) ?? mailboxes[0]

  useEffect(() => {
    if (!open) return
    items.current[mailboxes.findIndex((m) => m.id === value)]?.focus()
    const close = (event: MouseEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    return () => document.removeEventListener('mousedown', close)
  }, [open, value])

  const choose = (id: Mailbox['id']) => {
    onChange(id)
    setOpen(false)
    trigger.current?.focus()
  }

  const onMenuKey = (event: KeyboardEvent) => {
    const index = items.current.findIndex((item) => item === document.activeElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const step = event.key === 'ArrowDown' ? 1 : -1
      items.current[(index + step + mailboxes.length) % mailboxes.length]?.focus()
    } else if (event.key === 'Escape') {
      event.preventDefault()
      setOpen(false)
      trigger.current?.focus()
    } else if (event.key === 'Tab') {
      setOpen(false)
    }
  }

  return (
    <div ref={root} className="relative min-w-0">
      <button
        ref={trigger}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Mailbox: ${current.address}`}
        onClick={() => setOpen((v) => !v)}
        className={cx(box, 'hover:border-ink', focusRing)}
      >
        <span className="truncate">{current.address}</span>
        <ChevronDown aria-hidden size={14} className="shrink-0 text-ink-2" />
      </button>
      {open && (
        <div
          role="menu"
          aria-label="Mailbox"
          onKeyDown={onMenuKey}
          className="absolute top-full right-0 z-10 mt-1 flex w-72 max-w-[calc(100vw-32px)] flex-col border border-rule bg-raised py-1"
        >
          {mailboxes.map((mailbox, i) => (
            <button
              key={mailbox.id}
              ref={(el) => {
                items.current[i] = el
              }}
              type="button"
              role="menuitemradio"
              aria-checked={mailbox.id === value}
              onClick={() => choose(mailbox.id)}
              className={cx('flex items-center gap-3 px-3 py-2 text-left hover:bg-sunken', focusRing)}
            >
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="truncate font-mono text-mono">{mailbox.address}</span>
                <span className="text-small text-ink-2">{mailbox.label}</span>
              </span>
              {mailbox.id === value && <Check aria-hidden size={14} />}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

/** The mailbox shown without a menu, when the access already belongs to it. */
export function MailboxLabel({ id }: { id: Mailbox['id'] }) {
  const mailbox = mailboxes.find((m) => m.id === id) ?? mailboxes[0]
  return (
    <span className={cx(box, 'min-w-0')}>
      <span className="sr-only">Mailbox: </span>
      <span className="truncate">{mailbox.address}</span>
    </span>
  )
}
