import { Tile } from '../../components/ui/index.ts'
import { connections, type Rule } from '../../data/sample.ts'
import { joinNames } from './data.ts'

/** Who a rule applies to: every connection, or the ones picked, with their tiles. */
export function AppliesTo({ appliesTo }: { appliesTo: Rule['appliesTo'] }) {
  if (appliesTo === 'all') return <span className="text-small">Every connection</span>
  const picked = connections.filter((c) => appliesTo.includes(c.id))
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      {picked.map((c) => (
        <Tile key={c.id} initials={c.initials} size="sm" />
      ))}
      <span className="truncate text-small">{joinNames(picked.map((c) => c.name))}</span>
    </span>
  )
}
