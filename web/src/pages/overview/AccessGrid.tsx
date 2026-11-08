import { ChevronRight } from 'lucide-react'
import { Link } from 'react-router'
import { Legend, Tile, type Cell } from '../../components/ui/index.ts'
import { connections, formatCount, inbox, ruleColumns, withheldRules, type Connection } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'

const legend: Array<{ cell: Cell; label: string }> = [
  { cell: 'shared', label: 'Can read' },
  { cell: 'headers', label: 'Headers only' },
  { cell: 'withheld', label: 'Withheld' },
]

/** Who can read what: one row per connection, one column per rule, and the rules withheld from all as one block over every row. */
export function AccessGrid() {
  return (
    <div>
      <table aria-label="What each connection can read, by rule" className="-m-1 w-[calc(100%+8px)] table-fixed border-separate border-spacing-1">
        <colgroup>
          <col className="w-[232px]" />
          {ruleColumns.map((column) => (
            <col key={column.id} />
          ))}
          <col />
        </colgroup>
        <thead>
          <tr>
            <th scope="col" className="text-left align-top text-small font-normal text-ink-2">
              Connection
            </th>
            {ruleColumns.map((column) => (
              <ColumnHeading key={column.id} name={column.name} count={column.count} />
            ))}
            <ColumnHeading name="Withheld from all" count={inbox.withheld} />
          </tr>
        </thead>
        <tbody>
          {connections.map((connection, row) => (
            <tr key={connection.id}>
              <th scope="row" className="p-0 text-left font-normal">
                <ConnectionLink connection={connection} />
              </th>
              {connection.cells.map((cell, i) => (
                <AccessCell key={ruleColumns[i].id} cell={cell} count={ruleColumns[i].count} />
              ))}
              {row === 0 && <WithheldBlock rows={connections.length} />}
            </tr>
          ))}
        </tbody>
      </table>
      <div className="mt-3">
        <Legend items={legend} />
      </div>
    </div>
  )
}

function ColumnHeading({ name, count }: { name: string; count: number }) {
  return (
    <th scope="col" className="text-left align-top font-normal">
      <span className="block truncate text-small text-ink">{name}</span>
      <span className="block font-mono text-micro text-ink-2">{formatCount(count)}</span>
    </th>
  )
}

function ConnectionLink({ connection }: { connection: Connection }) {
  return (
    <Link
      to={`/connections/${connection.id}`}
      className={cx('group flex h-13 items-center gap-2.5 rounded-sm', focusRing)}
    >
      <Tile initials={connection.initials} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-body font-medium">{connection.name}</span>
        <span className={cx('truncate font-mono text-micro', connection.flag ? 'text-attention' : 'text-ink-2')}>
          {connection.overviewDetail}
        </span>
      </span>
      <ChevronRight aria-hidden size={16} className="shrink-0 text-ink-2 group-hover:text-ink" />
    </Link>
  )
}

function AccessCell({ cell, count }: { cell: Cell; count: number }) {
  if (cell === 'withheld') {
    return (
      <td className="h-13 bg-bar p-0">
        <span className="sr-only">Withheld</span>
      </td>
    )
  }
  const shared = cell === 'shared'
  return (
    <td
      className={cx(
        'h-13 px-3 font-mono text-mono',
        shared ? 'bg-highlight text-on-highlight' : 'bg-highlight-soft text-on-highlight-soft',
      )}
    >
      {shared ? (
        <>
          <span className="sr-only">Can read </span>
          {formatCount(count)}
        </>
      ) : (
        'headers'
      )}
    </td>
  )
}

function WithheldBlock({ rows }: { rows: number }) {
  return (
    <td rowSpan={rows} className="bg-bar px-3 align-middle text-on-bar">
      <span className="block font-mono text-mono">never</span>
      <span className="mt-2 block text-mono">{withheldRules.map((rule) => rule.name).join(', ')}</span>
    </td>
  )
}
