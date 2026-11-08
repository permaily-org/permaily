import { attention, type AttentionItem, type Connection } from '../../data/sample.ts'

export function isHeadersOnly(connection: Connection): boolean {
  return connection.parts === 'Headers only'
}

/** An agent's own searches, written inside a scope, rather than a fixed query. */
export function isOwnSearches(connection: Connection): boolean {
  return connection.query.startsWith('Its own searches')
}

/** The connection's open item on Overview, such as a request for more access. */
export function attentionFor(connection: Connection): AttentionItem | undefined {
  return attention.find((item) => item.area === 'connections' && item.connectionId === connection.id)
}
