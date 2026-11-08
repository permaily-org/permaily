import { Tile } from '../../components/ui/index.ts'
import { connectionNames, joinNames } from './data.ts'

/** Initials of the connections that use a rule, with an optional note such as "headers only". */
export function UsedBy({ initials, note }: { initials: string[]; note?: string }) {
  return (
    <span className="flex items-center gap-1">
      {initials.map((i) => (
        <Tile key={i} initials={i} size="sm" />
      ))}
      {initials.length > 0 && <span className="sr-only">{joinNames(connectionNames(initials))}</span>}
      {note && <span className={initials.length > 0 ? 'ml-1 text-small text-ink-2' : 'text-small text-ink-2'}>{note}</span>}
    </span>
  )
}
