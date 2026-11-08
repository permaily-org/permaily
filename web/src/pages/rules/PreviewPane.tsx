import { useEffect, useRef } from 'react'
import { Count, Headline, MailRow, TextAction } from '../../components/ui/index.ts'
import { appliesToText, formatCount, inbox, type Rule, type RuleEffect } from '../../data/sample.ts'
import { emails, type Preview, type RowView } from './draft.ts'

type RowActions = {
  effect: RuleEffect
  onExclude: (rowId: string, address: string) => void
  onInclude: (rowId: string, entry: string) => void
}

/** What the rule takes out of every connection's view: whole emails, or details inside them. A rule never shares anything. */
export function PreviewPane({
  preview,
  effect,
  removes,
  appliesTo,
  onExclude,
  onInclude,
}: {
  preview: Preview
  effect: RuleEffect
  removes: string
  appliesTo: Rule['appliesTo']
  onExclude: (address: string) => void
  onInclude: (entry: string) => void
}) {
  const listRef = useRef<HTMLDivElement>(null)
  const pendingFocus = useRef<string | null>(null)
  const { rows, count, newCount, dropped } = preview

  useEffect(() => {
    const selector = pendingFocus.current
    if (!selector) return
    pendingFocus.current = null
    listRef.current?.querySelector<HTMLElement>(selector)?.focus()
  })

  const actions: RowActions = {
    effect,
    onExclude: (rowId, address) => {
      pendingFocus.current = `[data-include="${rowId}"]`
      onExclude(address)
    },
    onInclude: (rowId, entry) => {
      pendingFocus.current = `[data-exclude="${rowId}"]`
      onInclude(entry)
    },
  }

  const visible = rows.filter((r) => r.status !== 'none')
  const matched = visible.filter((r) => r.status === 'new' || r.status === 'soft').length

  return (
    <section aria-label="Live preview" className="min-h-svh min-w-0 flex-1 border-l border-rule bg-raised px-10 pt-8 pb-12">
      <p className="font-mono text-mono text-ink-2">Live preview, updating as you edit</p>
      <Headline as="h2" className="mt-3">
        <Count tone="bar">{formatCount(count)}</Count> of {formatCount(inbox.total)} emails
      </Headline>

      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1">
        {newCount > 0 && <span className="font-mono text-mono">+{formatCount(newCount)} from this edit</span>}
        <p className="text-small text-ink-2">{note(count, dropped, effect, removes, appliesTo)}</p>
      </div>

      <div ref={listRef} className="mt-4 border-t border-rule-strong">
        {visible.map((view) => (
          <PreviewRow key={view.row.id} view={view} actions={actions} />
        ))}
      </div>
      {visible.length > 0 && matched < count && (
        <p className="mt-3 text-small text-ink-2">
          Showing {matched} of {formatCount(count)}.
        </p>
      )}
    </section>
  )
}

function note(count: number, dropped: number, effect: RuleEffect, removes: string, appliesTo: Rule['appliesTo']): string {
  if (count === 0) return 'Nothing matches yet. Add a condition on the left.'
  const target = appliesToText(appliesTo)
  const parts =
    effect === 'hold'
      ? [
          `Held back from ${appliesTo === 'all' ? 'every connection, including ones you approve later' : target}.`,
          appliesTo !== 'all' && appliesTo.length === 1 ? "It isn't told." : "They aren't told.",
        ]
      : [
          `${capitalize(removes.trim() || 'the matching details')} ${removes.trim().endsWith('s') ? 'are' : 'is'} taken out before ${appliesTo === 'all' ? 'any connection' : target} gets them.`,
          'The rest of each email goes through.',
        ]
  if (dropped > 0) parts.push(`${emails(dropped)} no longer ${dropped === 1 ? 'matches' : 'match'}.`)
  return parts.join(' ')
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

const hoverAction = 'absolute top-1/2 right-2 -translate-y-1/2 opacity-0 group-hover:opacity-100 focus-visible:opacity-100'
const hoverRow = 'group relative hover:bg-sunken focus-within:bg-sunken'

function PreviewRow({ view, actions }: { view: RowView; actions: RowActions }) {
  const { row, status, excludedBy } = view

  if (status === 'excluded' && excludedBy) {
    return (
      <div className={hoverRow}>
        <MailRow kind="excluded" date={row.date} sender={row.sender} reason={`excluded: ${excludedBy}`} barWidth={130} />
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

  const trailing = (
    <>
      {status === 'new' && (
        <span className="shrink-0 pr-2 font-mono text-mono font-medium text-ink group-hover:invisible group-focus-within:invisible">new</span>
      )}
      <TextAction
        size="sm"
        data-exclude={row.id}
        aria-label={`Exclude sender ${row.address}`}
        onClick={() => actions.onExclude(row.id, row.address)}
        className={hoverAction}
      >
        Exclude sender
      </TextAction>
    </>
  )

  return (
    <div className={hoverRow}>
      {actions.effect === 'hold' ? (
        <MailRow kind="held" date={row.date} sender={row.sender} reason={`held back: ${view.matchedBy ?? row.subject}`} barWidth={100} trailing={trailing} />
      ) : (
        <MailRow kind="trimmed" date={row.date} sender={row.sender} snippet={row.snippet ?? `${row.subject} [details]`} trailing={trailing} />
      )}
    </div>
  )
}
