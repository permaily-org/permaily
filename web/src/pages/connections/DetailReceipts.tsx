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
        <p className="py-3.5 text-body text-ink-2">
          {revoked ? `${connection.name} never read any ${unit}.` : `${connection.name} hasn't read any ${unit} yet.`}
        </p>
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
    <li className="flex h-11 items-center gap-4 border-b border-rule">
      <span className="w-[72px] shrink-0 font-mono text-mono text-ink-2">{receipt.time}</span>
      <span className="w-28 shrink-0 truncate font-medium">{receipt.sender}</span>
      <span className="min-w-0 flex-1 truncate">{receipt.subject}</span>
      <PartsChip>
        <span className="sr-only">Read: </span>
        {receipt.parts}
      </PartsChip>
    </li>
  )
}
