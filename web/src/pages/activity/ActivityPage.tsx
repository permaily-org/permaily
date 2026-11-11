import { useEffect, useState, useSyncExternalStore } from 'react'
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

const xl = '(min-width: 80rem)'

function subscribeToXl(onChange: () => void) {
  const query = window.matchMedia(xl)
  query.addEventListener('change', onChange)
  return () => query.removeEventListener('change', onChange)
}

/** From xl the receipt has its own column beside the list. Below it, a receipt opens under its row. */
function useWide(): boolean {
  return useSyncExternalStore(subscribeToXl, () => window.matchMedia(xl).matches)
}

function Ledger() {
  const [query, setQuery] = useState('')
  const wide = useWide()
  // Nothing is picked until someone taps a row. Until then the side column shows the first receipt.
  const [pickedId, setPickedId] = useState<string | null>(null)
  const shown = thisWeek.filter((r) => matchesSearch(r, query))
  const picked = shown.find((r) => r.id === pickedId)
  const selected = wide ? (picked ?? shown[0]) : picked
  const leftThisWeek = dailyCounts.slice(7).reduce((total, n) => total + n, 0)

  // Opening a receipt closes the one before it, which can move the tapped row up and out of view, so keep it on screen.
  useEffect(() => {
    if (wide || !pickedId) return
    document.querySelector(`[data-receipt="${pickedId}"]`)?.scrollIntoView({ block: 'nearest' })
  }, [pickedId, wide])

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
                    inline={!wide}
                    // From xl a click picks the receipt. Below it a tap opens it under the row, and another tap closes it.
                    onSelect={() =>
                      setPickedId(wide || receipt.id !== pickedId ? receipt.id : null)
                    }
                  />
                ))}
              </ul>
            </section>
          ))
        )}
      </div>

      {wide && (
        <aside
          aria-label="Receipt"
          className="sticky top-0 h-svh w-[420px] shrink-0 overflow-y-auto border-l border-rule bg-raised px-9 pt-8 pb-10"
        >
          {selected ? (
            <ReceiptPanel receipt={selected} />
          ) : (
            <p className="text-body text-ink-2">Pick an email to see its receipt.</p>
          )}
        </aside>
      )}
    </div>
  )
}

function ReceiptRow({
  receipt,
  selected,
  inline,
  onSelect,
}: {
  receipt: Receipt
  selected: boolean
  inline: boolean
  onSelect: () => void
}) {
  const connection = connectionById(receipt.connectionId)
  const vendor = connection && vendorOf(connection)
  const panelId = `receipt-${receipt.id}`
  return (
    <li className="border-b border-rule">
      <button
        type="button"
        data-receipt={receipt.id}
        // Picked from xl. Below it the row opens and closes its receipt.
        aria-pressed={inline ? undefined : selected}
        aria-expanded={inline ? selected : undefined}
        aria-controls={inline && selected ? panelId : undefined}
        onClick={onSelect}
        className={cx(
          '-mx-2.5 grid w-[calc(100%+20px)] scroll-mt-16 grid-cols-[auto_1fr_auto] items-center gap-x-2 gap-y-0.5 border-l-2 py-2 pr-2.5 pl-2 text-left sm:flex sm:h-[37px] sm:gap-0 sm:py-0 md:scroll-mt-4',
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
      {/* Below xl the receipt opens here, under its row, carrying on the row's rule at the left. */}
      {inline && selected && (
        <div
          id={panelId}
          role="region"
          aria-label="Receipt"
          className="-mx-2.5 border-l-2 border-ink bg-raised px-4 pt-5 pb-6 sm:px-6"
        >
          <ReceiptPanel receipt={receipt} />
        </div>
      )}
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
