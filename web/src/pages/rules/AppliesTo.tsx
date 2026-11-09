import { Tile } from '../../components/ui/index.ts'
import {
  joinNames,
  type AppliesTo as RuleScope,
  targetLabel,
  targetVendor,
  type Vendor,
} from '../../data/sample.ts'

/** Who a rule applies to: every connection with any exceptions, or the vendors and connections picked. */
export function AppliesTo({ appliesTo }: { appliesTo: RuleScope }) {
  if (appliesTo.to === 'all') {
    if (appliesTo.except.length === 0) return <span className="text-small">Every connection</span>
    return (
      <span className="flex min-w-0 flex-col">
        <span className="text-small">Every connection</span>
        <span className="truncate text-small text-ink-2">
          except {joinNames(appliesTo.except.map(targetLabel))}
        </span>
      </span>
    )
  }
  if (appliesTo.only.length === 0)
    return <span className="text-small text-ink-2">No connection</span>
  const tiles = [
    ...new Map(
      appliesTo.only
        .map(targetVendor)
        .filter((v): v is Vendor => !!v)
        .map((v) => [v.id, v]),
    ).values(),
  ]
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      {tiles.map((vendor) => (
        <Tile key={vendor.id} initials={vendor.initials} size="sm" />
      ))}
      <span className="truncate text-small">{joinNames(appliesTo.only.map(targetLabel))}</span>
    </span>
  )
}
