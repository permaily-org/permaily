import { user, type Connection, type Receipt } from '../../data/sample.ts'

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

export const neverSummary = 'Password resets, login codes and security alerts'

/** Requests each connection made that Permaily turned down. */
export const refusedRequests: Record<string, Array<{ when: string; text: string }>> = {
  claude: [
    { when: 'Today', text: 'Attachment on Invoice F-2291. Not part of this access.' },
    { when: 'Mon', text: 'Search outside its rules. 0 results returned.' },
  ],
  'trip-app': [
    { when: 'Today', text: 'Password reset from Booking.com. On Never share.' },
    { when: 'Mon', text: 'Login link from Iberia. On Never share.' },
  ],
  ledgerly: [
    { when: 'Sun', text: 'Mail from bbva.es. You put it on Never share.' },
    { when: 'Sat', text: 'Security alert from Revolut. On Never share.' },
  ],
}

const changeRoutes: Record<string, string> = {
  'trip-app': '/consent/trip-app',
  claude: '/consent/claude',
  ledgerly: '/consent/ledgerly',
  'inbox-tidy': '/rules/everything',
}

export function changeRoute(id: string): string {
  return changeRoutes[id] ?? '/rules'
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
export function deletionRequest(connection: Connection, items: Receipt[], headersOnly: boolean): string {
  const what = headersOnly ? `the headers of ${connection.hasRead} of my emails` : `${connection.hasRead} of my emails`
  return [
    `To the team behind ${connection.name} (${connection.domain}),`,
    '',
    `I revoked ${connection.name}'s access to my mailbox through Permaily today at ${revokedAt}. Before that, it read ${what}.`,
    'Please delete them, along with anything made from them, and confirm when that is done.',
    '',
    ...(items.length > 0
      ? ['The most recent ones are below. I can share the Permaily receipt for each one on request.', ...items.map((r) => `- ${r.sender}, "${r.subject}" (receipt ${r.id})`), '']
      : []),
    user.name,
    user.email,
  ].join('\n')
}
