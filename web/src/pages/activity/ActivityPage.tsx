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
import { connectionById, dailyCounts, type Receipt, vendorOf } from '../../data/sample.ts'
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
    <div className="flex min-h-svh flex-col xl:flex-row">
      <div className="min-w-0 flex-1 px-4 pt-6 pb-10 sm:px-8 sm:pt-8 sm:pb-16 xl:px-10">
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
            className="w-full sm:w-[225px]"
          />
          <FilterButton>All connections</FilterButton>
          <FilterButton>This week</FilterButton>
          <TextAction
            className="ml-auto"
            onClick={() => downloadText('permaily-activity.csv', receiptsCsv(shown), 'text/csv')}
          >
            Export CSV
          </TextAction>
        </div>

        {shown.length === 0 ? (
          <p className="py-6 text-body text-ink-2">
            No email from this week matches “{query.trim()}”.
          </p>
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
                    onSelect={() => {
                      setSelectedId(receipt.id)
                      // Below xl the receipt sits after the list, so bring it into view.
                      if (!window.matchMedia('(min-width: 1280px)').matches) {
                        document.getElementById('receipt-panel')?.scrollIntoView({ block: 'start' })
                      }
                    }}
                  />
                ))}
              </ul>
            </section>
          ))
        )}
      </div>

      <aside
        id="receipt-panel"
        aria-label="Receipt"
        className="w-full shrink-0 border-t border-rule bg-raised px-4 pt-6 pb-10 sm:px-8 sm:pt-8 xl:sticky xl:top-0 xl:h-svh xl:w-[420px] xl:overflow-y-auto xl:border-t-0 xl:border-l xl:px-9"
      >
        {selected ? (
          <ReceiptPanel receipt={selected} />
        ) : (
          <p className="text-body text-ink-2">Pick an email to see its receipt.</p>
        )}
      </aside>
    </div>
  )
}

function ReceiptRow({
  receipt,
  selected,
  onSelect,
}: {
  receipt: Receipt
  selected: boolean
  onSelect: () => void
}) {
  const connection = connectionById(receipt.connectionId)
  const vendor = connection && vendorOf(connection)
  return (
    <li className="border-b border-rule">
      <button
        type="button"
        aria-pressed={selected}
        onClick={onSelect}
        className={cx(
          '-mx-2.5 grid w-[calc(100%+20px)] grid-cols-[auto_1fr_auto] items-center gap-x-2 gap-y-0.5 border-l-2 py-2 pr-2.5 pl-2 text-left sm:flex sm:h-[37px] sm:gap-0 sm:py-0',
          selected ? 'border-ink bg-sunken' : 'border-transparent hover:bg-sunken',
          focusRing,
        )}
      >
        <span className="row-start-3 shrink-0 font-mono text-mono text-ink-2 sm:w-[52px]">
          {receipt.time}
        </span>
        <span className="col-span-2 row-start-3 flex min-w-0 items-center gap-1.5 sm:w-[104px] sm:shrink-0">
          <Tile initials={vendor?.initials ?? '?'} size="sm" />
          <span className="truncate">{vendor?.name}</span>
        </span>
        <span className="col-span-2 row-start-1 truncate font-medium sm:w-[104px] sm:shrink-0">
          {receipt.sender}
        </span>
        <span className="col-span-3 row-start-2 min-w-0 truncate sm:flex-1 sm:pr-4">
          {receipt.subject}
        </span>
        <span className="col-start-3 row-start-1 flex justify-end">
          <PartsChip>{receipt.parts}</PartsChip>
        </span>
      </button>
    </li>
  )
}

function EmptyActivity() {
  return (
    <div className="w-full max-w-[720px] px-4 pt-6 pb-10 sm:px-8 sm:pt-8 sm:pb-16 xl:px-10">
      <Headline>Nothing has left your mailbox.</Headline>
      <ButtonLink to="/connections/new" className="mt-6">
        Connect an agent
      </ButtonLink>
    </div>
  )
}
