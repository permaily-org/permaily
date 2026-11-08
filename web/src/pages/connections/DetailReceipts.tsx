import { DayHeading, PartsChip, SectionHeading, TextLink } from '../../components/ui/index.ts'
import { formatCount, type Connection, type Receipt } from '../../data/sample.ts'
import { groupByDay } from './DetailData.ts'

/** What the connection has read, newest first, grouped by day. */
export function DetailReceipts({
  connection,
  items,
  unit,
  revoked,
}: {
  connection: Connection
  items: Receipt[]
  unit: string
  revoked: boolean
}) {
  const action =
    items.length > 0 ? (
      <TextLink to="/activity">{revoked ? `See all ${formatCount(connection.hasRead)}` : 'See all in Activity'}</TextLink>
    ) : undefined

  return (
    <section aria-labelledby="read-heading">
      <SectionHeading id="read-heading" title={revoked ? 'What it may still hold' : 'What it has read'} action={action} />
      {items.length === 0 ? (
        <p className="py-3.5 text-body text-ink-2">{revoked ? `It never read any ${unit}` : `No ${unit} yet`}</p>
      ) : (
        groupByDay(items).map((group) => (
          <div key={group.label}>
            <DayHeading>{group.label}</DayHeading>
            <ul>
              {group.items.map((receipt) => (
                <ReceiptRow key={receipt.id} receipt={receipt} />
              ))}
            </ul>
          </div>
        ))
      )}
    </section>
  )
}

function ReceiptRow({ receipt }: { receipt: Receipt }) {
  return (
    <li className="flex min-h-11 items-center gap-3 border-b border-rule py-2 sm:h-11 sm:gap-4 sm:py-0">
      <span className="w-[44px] shrink-0 font-mono text-mono text-ink-2 sm:w-[72px]">{receipt.time}</span>
      {/* On phones the sender sits over the subject. */}
      <span className="flex min-w-0 flex-1 flex-col sm:flex-row sm:items-center sm:gap-4">
        <span className="truncate font-medium sm:w-28 sm:shrink-0">{receipt.sender}</span>
        <span className="min-w-0 truncate sm:flex-1">{receipt.subject}</span>
      </span>
      <PartsChip>
        <span className="sr-only">Read: </span>
        {receipt.parts}
      </PartsChip>
    </li>
  )
}
