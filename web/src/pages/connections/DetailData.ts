import { user, type Connection, type Receipt, type Vendor } from '../../data/sample.ts'

/** Revoking happens at a fixed time so it sits after today's sample receipts. */
export const revokedAt = '14:22'

/** The sample "now" is a Tuesday afternoon, so Monday reads as yesterday. */
export const dayLabels: Record<Receipt['day'], string> = {
  Today: 'Today',
  Monday: 'Yesterday',
  Sunday: 'Sunday',
  Saturday: 'Saturday',
  'Last week': 'Last week',
}

const changeRoutes: Record<string, string> = {
  'trip-app/bookings': '/consent/trip-app',
  'claude/chat': '/consent/claude',
  'ledgerly/bookkeeping': '/consent/ledgerly',
}

/** Where to change what a connection can do. Connections without a consent screen in the sample have none. */
export function changeRoute(id: string): string | undefined {
  return changeRoutes[id]
}

export function groupByDay(items: Receipt[]): Array<{ label: string; items: Receipt[] }> {
  const groups: Array<{ label: string; items: Receipt[] }> = []
  for (const item of items) {
    const label = dayLabels[item.day]
    const last = groups.at(-1)
    if (last?.label === label) last.items.push(item)
    else groups.push({ label, items: [item] })
  }
  return groups
}

/** The text copied by "Copy a deletion request". The wording still needs a legal check. */
export function deletionRequest(vendor: Vendor, connection: Connection, items: Receipt[], headersOnly: boolean): string {
  const what = headersOnly ? `the headers of ${connection.hasRead} of my emails` : `${connection.hasRead} of my emails`
  return [
    `To the team behind ${vendor.name} (${vendor.domain}),`,
    '',
    `I revoked the access of ${vendor.name}'s ${connection.name} connection to my mailbox through Permaily today at ${revokedAt}. Before that, it read ${what}.`,
    'Please delete them, along with anything made from them, and confirm when that is done.',
    '',
    ...(items.length > 0
      ? ['The most recent ones are below. I can share the Permaily receipt for each one on request.', ...items.map((r) => `- ${r.sender}, "${r.subject}" (receipt ${r.id})`), '']
      : []),
    user.name,
    user.email,
  ].join('\n')
}
