import type { Cell } from '../../components/ui/index.ts'
import { attention, ruleColumns, withheldNames, type AttentionItem, type Connection } from '../../data/sample.ts'

const columnNames = [...ruleColumns.map((column) => column.name), withheldNames]

/** The six strip cells: one per Overview column, with the rules withheld from all always withheld. */
export function stripCells(connection: Connection): Cell[] {
  return [...connection.cells, 'withheld']
}

export function stripLabel(connection: Connection): string {
  const groups: Record<Cell, string[]> = { shared: [], headers: [], withheld: [] }
  stripCells(connection).forEach((cell, i) => groups[cell].push(columnNames[i]))
  return [
    groups.shared.length > 0 && `Can read ${groups.shared.join(', ')}`,
    groups.headers.length > 0 && `Headers only for ${groups.headers.join(', ')}`,
    groups.withheld.length > 0 && `Withheld: ${groups.withheld.join(', ')}`,
  ]
    .filter(Boolean)
    .join('. ')
}

export function isHeadersOnly(connection: Connection): boolean {
  return connection.cells.includes('headers') && !connection.cells.includes('shared')
}

/** The connection's open item on Overview, such as a request for more access. */
export function attentionFor(connection: Connection): AttentionItem | undefined {
  return attention.find((item) => item.area === 'connections' && item.id.startsWith(`${connection.id}-`))
}
