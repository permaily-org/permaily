import { useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  ButtonLink,
  Count,
  DayHeading,
  FilterButton,
  Headline,
  PartsChip,
  SearchField,
  TextAction,
  Tile,
} from '../../components/ui/index.ts'
import { connectionById, dailyCounts, type Receipt } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { downloadText, groupByDay, matchesSearch, receiptsCsv, thisWeek } from './data.ts'
import { Histogram } from './Histogram.tsx'
import { ReceiptPanel } from './ReceiptPanel.tsx'

export function ActivityPage() {
  const [params] = useSearchParams()
  return params.get('state') === 'empty' ? <EmptyActivity /> : <Ledger />
}

function Ledger() {
  const [query, setQuery] = useState('')
  const [selectedId, setSelectedId] = useState(thisWeek[0]?.id)
  const shown = thisWeek.filter((r) => matchesSearch(r, query))
  const selected = shown.find((r) => r.id === selectedId) ?? shown[0]
  const leftThisWeek = dailyCounts.slice(7).reduce((total, n) => total + n, 0)

  return (
    <div className="flex min-h-svh">
      <div className="min-w-0 flex-1 px-10 pt-8 pb-16">
        <Headline>
          <Count>{leftThisWeek}</Count> emails left this week.
        </Headline>

        <Histogram />

        <div className="mt-3.5 flex flex-wrap items-center gap-2 border-b border-rule-strong pb-3.5">
          <SearchField
            label="Search sender or subject"
            placeholder="Search sender or subject"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="w-[225px]"
          />
          <FilterButton>All connections</FilterButton>
          <FilterButton>All rules</FilterButton>
          <FilterButton>This week</FilterButton>
          <TextAction className="ml-auto" onClick={() => downloadText('permaily-activity.csv', receiptsCsv(shown), 'text/csv')}>
            Export CSV
          </TextAction>
        </div>

        {shown.length === 0 ? (
          <p className="py-6 text-body text-ink-2">No email from this week matches “{query.trim()}”.</p>
        ) : (
          groupByDay(shown).map(({ day, rows }) => (
            <section key={day} aria-label={day}>
              <DayHeading>{day}</DayHeading>
              <ul>
                {rows.map((receipt) => (
                  <ReceiptRow
                    key={receipt.id}
                    receipt={receipt}
                    selected={receipt.id === selected?.id}
                    onSelect={() => setSelectedId(receipt.id)}
                  />
                ))}
              </ul>
            </section>
          ))
        )}
      </div>

      <aside aria-label="Receipt" className="sticky top-0 h-svh w-[420px] shrink-0 overflow-y-auto border-l border-rule bg-raised px-9 pt-8 pb-10">
        {selected ? <ReceiptPanel receipt={selected} /> : <p className="text-body text-ink-2">Pick an email to see its receipt.</p>}
      </aside>
    </div>
  )
}

function ReceiptRow({ receipt, selected, onSelect }: { receipt: Receipt; selected: boolean; onSelect: () => void }) {
  const connection = connectionById(receipt.connectionId)
  return (
    <li className="border-b border-rule">
      <button
        type="button"
        aria-pressed={selected}
        onClick={onSelect}
        className={cx(
          '-mx-2.5 flex h-[37px] w-[calc(100%+20px)] items-center border-l-2 pr-2.5 pl-2 text-left',
          selected ? 'border-ink bg-sunken' : 'border-transparent hover:bg-sunken',
          focusRing,
        )}
      >
        <span className="w-[52px] shrink-0 font-mono text-mono text-ink-2">{receipt.time}</span>
        <span className="flex w-[104px] shrink-0 items-center gap-1.5">
          <Tile initials={connection?.initials ?? '?'} size="sm" />
          <span className="truncate">{connection?.name}</span>
        </span>
        <span className="w-[104px] shrink-0 truncate font-medium">{receipt.sender}</span>
        <span className="min-w-0 flex-1 truncate pr-4">{receipt.subject}</span>
        <PartsChip>{receipt.parts}</PartsChip>
      </button>
    </li>
  )
}

function EmptyActivity() {
  return (
    <div className="max-w-[720px] px-10 pt-8 pb-16">
      <Headline>Nothing has left your mailbox.</Headline>
      <p className="mt-3 text-lead text-ink-2">
        Each email a connection reads appears here, with a receipt showing who got it, which parts, and why.
      </p>
      <ButtonLink to="/connections/new" className="mt-6">
        Connect an agent
      </ButtonLink>
    </div>
  )
}
