import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { Bar, Button, Headline, SectionHeading, TextAction, TextLink } from '../../components/ui/index.ts'
import { formatCount, neverShare } from '../../data/sample.ts'
import { actionClass, cx, focusRing } from '../../lib/styles.ts'

type Entry = (typeof neverShare.mine)[number]

function emails(n: number): string {
  return `${formatCount(n)} ${n === 1 ? 'email' : 'emails'}`
}

/** How many emails a new entry covers. A stand-in until the mailbox is searched. */
function sampleCount(value: string): number {
  let hash = 0
  for (const ch of value) hash = (hash * 31 + ch.charCodeAt(0)) % 997
  return (hash % 24) + 1
}

function toEntry(raw: string): Entry | null {
  const value = raw.trim()
  if (!value) return null
  const count = sampleCount(value.toLowerCase())
  if (value.includes('@')) return { id: `person-${value}`, kind: 'person', title: value.toLowerCase(), detail: 'Every email from this address', count }
  if (/^[a-z0-9-]+(\.[a-z0-9-]+)+$/i.test(value)) {
    return { id: `domain-${value}`, kind: 'domain', title: value.toLowerCase(), detail: 'All mail from this domain', count }
  }
  return { id: `label-${value}`, kind: 'label', title: value, detail: 'Gmail label', count }
}

export function NeverSharePage() {
  const [entries, setEntries] = useState<Entry[]>(neverShare.mine)
  const total = [...neverShare.defaults, ...entries].reduce((sum, item) => sum + item.count, 0)

  return (
    <div className="px-14 pt-9 pb-16">
      <Headline>
        <Bar width={88} height={48} />
        {formatCount(total)} emails can never leave.
      </Headline>
      <p className="mt-2 text-lead text-ink-2">These are blocked above every rule and every connection, including ones you approve later.</p>

      <div className="mt-6 grid grid-cols-[minmax(0,1fr)_420px] items-start gap-14">
        <div>
          <AlwaysBlocked />
          <AddedByYou entries={entries} onChange={setEntries} />
        </div>
        <BlockedThisWeek />
      </div>
    </div>
  )
}

function AlwaysBlocked() {
  const id = useId()
  return (
    <section aria-labelledby={id}>
      <SectionHeading id={id} title="Always blocked" />
      <ul>
        {neverShare.defaults.map((item) => (
          <Row key={item.id} title={item.title} detail={item.detail} count={item.count}>
            <span className="font-mono text-mono">always on</span>
          </Row>
        ))}
      </ul>
    </section>
  )
}

function AddedByYou({ entries, onChange }: { entries: Entry[]; onChange: (entries: Entry[]) => void }) {
  const headingId = useId()
  const fieldId = useId()
  const errorId = useId()
  const [value, setValue] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [added, setAdded] = useState('')
  const [removed, setRemoved] = useState<{ entry: Entry; index: number } | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const undoRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (removed) undoRef.current?.focus()
  }, [removed])

  function add(event: FormEvent) {
    event.preventDefault()
    const entry = toEntry(value)
    if (!entry) {
      setError('Type a person, domain or label first.')
      inputRef.current?.focus()
      return
    }
    const taken = entries.some((e) => [e.title, e.detail].some((text) => text.toLowerCase() === entry.title.toLowerCase()))
    if (taken) {
      setError(`${entry.title} is already on Never share.`)
      inputRef.current?.focus()
      return
    }
    onChange([entry, ...entries])
    setValue('')
    setError(null)
    setRemoved(null)
    setAdded(`Added ${entry.title}. Its ${emails(entry.count)} can never leave.`)
  }

  function remove(entry: Entry, index: number) {
    onChange(entries.filter((e) => e.id !== entry.id))
    setRemoved({ entry, index })
    setAdded('')
  }

  function undo() {
    if (!removed) return
    const next = [...entries]
    next.splice(removed.index, 0, removed.entry)
    onChange(next)
    setRemoved(null)
    inputRef.current?.focus()
  }

  return (
    <section aria-labelledby={headingId} className="mt-6">
      <SectionHeading id={headingId} title="Added by you" />
      <form onSubmit={add} className="mt-3 flex gap-2">
        <label htmlFor={fieldId} className="sr-only">
          Add a person, domain or label
        </label>
        <input
          ref={inputRef}
          id={fieldId}
          value={value}
          onChange={(event) => {
            setValue(event.target.value)
            setError(null)
          }}
          placeholder="Add a person, domain or label"
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
          className={cx('min-w-0 flex-1 rounded-sm border border-ink bg-transparent px-3 text-body placeholder:text-ink-3', focusRing)}
        />
        <Button type="submit">Add</Button>
      </form>
      {error && (
        <p id={errorId} className="mt-2 text-small text-danger">
          {error}
        </p>
      )}
      <p role="status" className="sr-only">
        {added}
      </p>

      <ul className="mt-3 border-t border-rule">
        {entries.map((entry, index) => (
          <Row key={entry.id} kind={entry.kind} title={entry.title} detail={entry.detail} count={entry.count}>
            <TextAction aria-label={`Remove ${entry.title}`} onClick={() => remove(entry, index)}>
              Remove
            </TextAction>
          </Row>
        ))}
      </ul>

      {entries.length === 0 && !removed && <p className="mt-3 text-small text-ink-2">Nothing added yet. The three above stay blocked either way.</p>}
      {removed && (
        <p role="status" className="mt-3 flex flex-wrap items-center gap-3 text-small text-ink-2">
          <span>
            Removed {removed.entry.title}. Its {emails(removed.entry.count)} can match your rules again.
          </span>
          <button ref={undoRef} type="button" onClick={undo} className={actionClass()}>
            Undo
          </button>
        </p>
      )}
    </section>
  )
}

function Row({ kind, title, detail, count, children }: { kind?: string; title: string; detail: string; count: number; children: ReactNode }) {
  return (
    <li className="flex items-center gap-4 border-b border-rule py-[9px]">
      {kind && <span className="w-14 shrink-0 font-mono text-mono text-ink-2">{kind}</span>}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-body">{title}</span>
        <span className="truncate text-small text-ink-2">{detail}</span>
      </span>
      <span className="shrink-0 text-right font-mono text-mono text-ink-2">{emails(count)}</span>
      <span className="flex w-[76px] shrink-0 justify-start has-[button]:justify-end">{children}</span>
    </li>
  )
}

function BlockedThisWeek() {
  const id = useId()
  return (
    <section aria-labelledby={id}>
      <SectionHeading id={id} title="Blocked this week" action={<TextLink to="/activity">See all in Activity</TextLink>} />
      <ul>
        {neverShare.blockedThisWeek.map((item) => (
          <li key={`${item.sender}-${item.when}`} className="border-b border-rule py-2.5">
            <div className="flex items-baseline justify-between gap-4">
              <span className="text-body">{item.sender}</span>
              <span className="font-mono text-mono text-ink-2">{item.when}</span>
            </div>
            <div className="mt-1 flex items-center gap-2.5">
              <span className="sr-only">Withheld email,</span>
              <Bar width={item.width} />
              <span className="font-mono text-mono text-danger">{item.reason}</span>
            </div>
          </li>
        ))}
      </ul>
    </section>
  )
}
