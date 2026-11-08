import { useState, type ReactNode } from 'react'
import { Headline, MailRow, TextAction, ViewSwitch } from '../../components/ui/index.ts'
import { formatCount } from '../../data/sample.ts'
import type { PreviewRow } from './data.ts'

type SharedRow = Extract<PreviewRow, { kind: 'shared' | 'soft' | 'new' }>
type View = 'whole' | 'only'

const pageSize = 13

export function PreviewEyebrow({ children }: { children: ReactNode }) {
  return <p className="font-mono text-mono text-ink-2">{children}</p>
}

/** The inbox as the requester would see it, with a switch to only what it gets and a way to see all of it. */
export function InboxPreview({
  eyebrow,
  headline,
  caption,
  rows,
  focus,
  switchLabel,
  rowAction,
}: {
  eyebrow: string
  headline: ReactNode
  caption: ReactNode
  rows: PreviewRow[]
  focus: 'shared' | 'new'
  switchLabel: string
  rowAction?: (row: SharedRow) => ReactNode
}) {
  const [view, setView] = useState<View>('whole')
  const [expanded, setExpanded] = useState(false)
  const total = rows.filter((row) => row.kind === focus).length
  const visible = visibleRows(rows, total > 0 ? view : 'whole', expanded, focus)
  const shown = visible.filter((row) => row.kind === focus).length
  const canExpand = expanded || shown < total

  return (
    <>
      <PreviewEyebrow>{eyebrow}</PreviewEyebrow>
      <Headline as="h2" size="display-l" className="mt-2">
        {headline}
      </Headline>
      <p className="mt-2.5 text-ink-2">{caption}</p>

      {total > 0 && (
        <div className="mt-3.5 flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
          <ViewSwitch
            label="Rows to show"
            value={view}
            onChange={setView}
            options={[
              { value: 'whole', label: 'Whole inbox' },
              { value: 'only', label: switchLabel },
            ]}
          />
          <div className="flex items-center gap-2.5">
            <p aria-live="polite" className="font-mono text-mono text-ink-2">
              Showing {formatCount(shown)} of {formatCount(total)} {focus}
            </p>
            {canExpand && <TextAction onClick={() => setExpanded((v) => !v)}>{expanded ? 'Show fewer' : 'Show all'}</TextAction>}
          </div>
        </div>
      )}

      <ul aria-label="Preview rows" className="mt-4 overflow-hidden border-t border-rule-strong">
        {visible.map((row) => (
          <li key={row.id}>
            <Row row={row} action={rowAction} />
          </li>
        ))}
      </ul>
    </>
  )
}

function visibleRows(rows: PreviewRow[], view: View, expanded: boolean, focus: 'shared' | 'new'): PreviewRow[] {
  if (view === 'only') {
    const list = rows.filter((row) => row.kind === focus || row.kind === 'excluded')
    return expanded ? list : list.slice(0, pageSize)
  }
  const size = rows.some((row) => row.kind !== 'withheld') ? pageSize : pageSize + 1
  if (!expanded) return rows.slice(0, size)
  const last = rows.findLastIndex((row) => row.kind === focus)
  return rows.slice(0, Math.max(size, last + 1))
}

function Row({ row, action }: { row: PreviewRow; action?: (row: SharedRow) => ReactNode }) {
  if (action && (row.kind === 'shared' || row.kind === 'soft' || row.kind === 'new')) {
    return (
      <div className="group hover:bg-sunken focus-within:bg-sunken">
        <MailRow {...row} trailing={<span className="pr-2 opacity-0 group-focus-within:opacity-100 group-hover:opacity-100">{action(row)}</span>} />
      </div>
    )
  }
  return <MailRow {...row} />
}
