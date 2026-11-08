import { ChevronRight } from 'lucide-react'
import { Link } from 'react-router'
import { ButtonLink, TextLink, Tile, VisibilityStrip } from '../../components/ui/index.ts'
import { connections, type Connection } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { attentionFor, stripCells, stripLabel } from './ConnectionsAccess.ts'

const headings = ['Connection', 'Can read', 'Parts and actions', 'Last used', 'Ends']

export function ConnectionsTable() {
  return (
    <table aria-label="Connections and what each one can read" className="w-full table-fixed border-collapse">
      <colgroup>
        <col className="w-[260px]" />
        <col />
        <col className="w-[166px]" />
        <col className="w-[116px]" />
        <col className="w-[72px]" />
        <col className="w-[70px]" />
      </colgroup>
      <thead>
        <tr className="border-b border-rule-strong">
          {headings.map((heading) => (
            <th key={heading} scope="col" className="pb-2 text-left text-small font-normal text-ink-2">
              {heading}
            </th>
          ))}
          <th scope="col" className="pb-2">
            <span className="sr-only">Action</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {connections.map((connection) => (
          <ConnectionRow key={connection.id} connection={connection} />
        ))}
      </tbody>
    </table>
  )
}

function ConnectionRow({ connection: c }: { connection: Connection }) {
  return (
    <tr className="relative h-[75px] border-b border-rule hover:bg-sunken">
      <td className="py-[10px] pr-4">
        <div className="flex items-center gap-2.5">
          <Tile initials={c.initials} />
          <div className="flex min-w-0 flex-col">
            <Link to={`/connections/${c.id}`} className={cx('self-start text-body font-medium after:absolute after:inset-0', focusRing)}>
              {c.name}
            </Link>
            <span className="truncate font-mono text-micro text-ink-2">
              {c.domain}, {c.delivery}
            </span>
            {c.flag && <span className="text-small text-attention">{c.flag}</span>}
          </div>
        </div>
      </td>
      <td className="py-[10px] pr-4">
        <div className="flex">
          <VisibilityStrip cells={stripCells(c)} label={stripLabel(c)} />
        </div>
        <p className="mt-1 truncate text-small">{c.ruleNames}</p>
        <p className="font-mono text-micro text-ink-2">{c.readLabel}</p>
      </td>
      <td className="py-[10px] pr-4 text-small">
        <p>{c.parts}</p>
        <p className="text-ink-2">{c.actions}</p>
      </td>
      <td className={cx('py-[10px] pr-4 font-mono text-mono', c.unused ? 'text-attention' : 'text-ink')}>{c.lastUsedShort}</td>
      <td className="py-[10px] pr-4 font-mono text-mono">{c.ends}</td>
      <td className="py-[10px] text-right">
        <RowAction connection={c} />
      </td>
    </tr>
  )
}

/** A request or a stale grant gets its action on the row. Other rows show that they open. */
function RowAction({ connection }: { connection: Connection }) {
  const item = attentionFor(connection)
  if (!item) return <ChevronRight aria-hidden size={16} className="ml-auto text-ink-2" />
  const label = `${item.action} ${connection.name}`
  return item.kind === 'decision' ? (
    <ButtonLink to={item.to} variant="secondary" size="sm" aria-label={label} className="relative z-10">
      {item.action}
    </ButtonLink>
  ) : (
    <TextLink to={item.to} aria-label={label} className="relative z-10">
      {item.action}
    </TextLink>
  )
}
