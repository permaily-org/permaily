import { useEffect, useRef, useState } from 'react'
import { Count, Headline, MailRow, PartsChip, TextAction, ViewSwitch } from '../../components/ui/index.ts'
import { formatCount, inbox } from '../../data/sample.ts'
import { emails, type Preview, type RowView } from './draft.ts'

type View = 'all' | 'matches' | 'new'

type RowActions = {
  readOnly?: boolean
  onExclude: (rowId: string, address: string) => void
  onInclude: (rowId: string, entry: string) => void
}

export function PreviewPane({
  preview,
  readOnly,
  withheld,
  onExclude,
  onInclude,
}: {
  preview: Preview
  readOnly?: boolean
  /** The rule is withheld from every connection, so the preview lists what it holds back, not what it shares. */
  withheld?: boolean
  onExclude: (address: string) => void
  onInclude: (entry: string) => void
}) {
  const [view, setView] = useState<View>('all')
  const listRef = useRef<HTMLDivElement>(null)
  const pendingFocus = useRef<string | null>(null)
  const { rows, count, newCount, dropped } = preview
  const current: View = (view === 'new' && newCount === 0) || count === 0 ? 'all' : view

  useEffect(() => {
    const selector = pendingFocus.current
    if (!selector) return
    pendingFocus.current = null
    listRef.current?.querySelector<HTMLElement>(selector)?.focus()
  })

  const actions: RowActions = {
    readOnly,
    onExclude: (rowId, address) => {
      pendingFocus.current = `[data-include="${rowId}"]`
      onExclude(address)
    },
    onInclude: (rowId, entry) => {
      pendingFocus.current = `[data-exclude="${rowId}"]`
      onInclude(entry)
    },
  }

  const options: Array<{ value: View; label: string }> = [
    { value: 'all', label: 'Whole inbox' },
    { value: 'matches', label: `Only the ${formatCount(count)}` },
  ]
  if (newCount > 0) options.push({ value: 'new', label: `Only the ${formatCount(newCount)} new` })

  const visible = rows.filter((r) => {
    if (withheld) return r.kind === 'mail' && r.status !== 'withheld'
    if (current === 'all') return true
    if (r.kind !== 'mail') return false
    return r.status === 'new' || (current === 'matches' && r.status === 'soft')
  })
  const total = withheld ? count : current === 'new' ? newCount : count
  const filtered = withheld || current !== 'all'

  return (
    <section aria-label="Live preview" className="min-h-svh min-w-0 flex-1 border-l border-rule bg-raised px-10 pt-8 pb-12">
      <p className="font-mono text-mono text-ink-2">Live preview, updating as you edit</p>
      <Headline as="h2" className="mt-3">
        <Count tone={withheld ? 'bar' : 'shared'}>{formatCount(count)}</Count> of {formatCount(inbox.total)} emails
      </Headline>

      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
        {newCount > 0 &&
          (withheld ? (
            <span className="font-mono text-mono">+{formatCount(newCount)} from this edit</span>
          ) : (
            <PartsChip>+{formatCount(newCount)} from this edit</PartsChip>
          ))}
        <p className="text-small text-ink-2">{withheld ? withheldNote(count, newCount, dropped) : deltaNote(count, newCount, dropped)}</p>
      </div>

      {count > 0 && !withheld && (
        <div className="mt-3">
          <ViewSwitch label="Show in preview" options={options} value={current} onChange={setView} />
        </div>
      )}

      <div ref={listRef} className="mt-3 border-t border-rule-strong">
        {visible.map((row, i) => (
          <PreviewRow
            key={row.kind === 'other' ? `other-${i}` : row.kind === 'mail' ? row.row.id : row.id}
            view={row}
            actions={actions}
            withheld={withheld}
          />
        ))}
      </div>
      {filtered && visible.length === 0 && <p className="mt-3 text-small text-ink-2">Nothing matches yet. Add a condition on the left.</p>}
      {filtered && visible.length > 0 && visible.length < total && (
        <p className="mt-3 text-small text-ink-2">
          Showing {visible.length} of {formatCount(total)}.
        </p>
      )}
    </section>
  )
}

function deltaNote(count: number, newCount: number, dropped: number): string {
  if (count === 0) return 'Nothing matches yet. Add a condition on the left.'
  const parts: string[] = []
  if (newCount > 0) parts.push('Bright is new.')
  if (dropped > 0) parts.push(`${emails(dropped)} no longer ${dropped === 1 ? 'matches' : 'match'}.`)
  if (newCount === 0 && dropped === 0) parts.push('No changes yet.')
  parts.push('Pale already matched.')
  return parts.join(' ')
}

function withheldNote(count: number, newCount: number, dropped: number): string {
  if (count === 0) return 'Nothing matches yet. Add a condition on the left.'
  const parts = ['No connection gets these, whatever its own rules match.']
  if (newCount > 0) parts.push('Marked new from this edit.')
  if (dropped > 0) parts.push(`${emails(dropped)} no longer ${dropped === 1 ? 'matches' : 'match'}.`)
  return parts.join(' ')
}

const hoverAction = 'absolute top-1/2 right-2 -translate-y-1/2 opacity-0 group-hover:opacity-100 focus-visible:opacity-100'
const hoverRow = 'group relative hover:bg-sunken focus-within:bg-sunken'

function PreviewRow({ view, actions, withheld }: { view: RowView; actions: RowActions; withheld?: boolean }) {
  if (view.kind === 'other') return <MailRow kind="withheld" widths={view.widths} />
  if (view.kind === 'blocked') return <MailRow kind="blocked" date={view.date} sender={view.sender} reason={view.reason} barWidth={100} />

  const { row, status, excludedBy } = view
  if (status === 'excluded' && excludedBy) {
    const reason = <MailRow kind="excluded" date={row.date} sender={row.sender} reason={`excluded: ${excludedBy}`} barWidth={130} />
    if (actions.readOnly) return reason
    return (
      <div className={hoverRow}>
        {reason}
        <TextAction
          size="sm"
          data-include={row.id}
          aria-label={`Include sender ${excludedBy}`}
          onClick={() => actions.onInclude(row.id, excludedBy)}
          className={hoverAction}
        >
          Include sender
        </TextAction>
      </div>
    )
  }
  if (status === 'dropped') return <MailRow kind="excluded" date={row.date} sender={row.sender} reason="no longer matches" barWidth={130} />
  if (status === 'withheld' || status === 'excluded') {
    const widths: [number, number, number] = [44, Math.min(120, 40 + row.sender.length * 6), Math.min(340, 120 + row.subject.length * 6)]
    return <MailRow kind="withheld" widths={widths} />
  }

  const exclude = actions.readOnly ? undefined : (
    <ExcludeAction rowId={row.id} address={row.address} actions={actions} />
  )
  if (withheld) {
    const isNew = status === 'new'
    return (
      <div className={hoverRow}>
        <MailRow
          kind="held"
          date={row.date}
          sender={row.sender}
          reason={`withheld: ${view.matchedBy ?? row.subject}`}
          barWidth={100}
          trailing={
            <>
              {isNew && <span className="shrink-0 pr-2 font-mono text-mono font-medium text-ink group-hover:invisible group-focus-within:invisible">new</span>}
              {exclude}
            </>
          }
        />
      </div>
    )
  }
  return (
    <div className={hoverRow}>
      <MailRow kind={status === 'new' ? 'new' : 'soft'} date={row.date} sender={row.sender} subject={row.subject} trailing={exclude} />
    </div>
  )
}

function ExcludeAction({ rowId, address, actions }: { rowId: string; address: string; actions: RowActions }) {
  return (
    <TextAction
      size="sm"
      data-exclude={rowId}
      aria-label={`Exclude sender ${address}`}
      onClick={() => actions.onExclude(rowId, address)}
      className={hoverAction}
    >
      Exclude sender
    </TextAction>
  )
}
