/*
  Sample data for the screens, matching the numbers in the designs so they add up across pages.
  Addresses for people use reserved example domains. Company senders are illustrative.

  Two things decide what a connection gets, in this order:
  1. Rules, which the person writes. They run first, on the whole mailbox, and hold back emails or
     remove details from them. Connections are never told about them.
  2. Queries, which connections bring. Each one runs on what the rules left, and the approved query
     caps what that connection can fetch.
*/

export const user = { name: 'Marta Ruiz', email: 'marta.ruiz@example.com' }

/** withheld: held back from every connection by rules. trimmed: emails that rules remove details from. */
export const inbox = { total: 12304, readable: 647, headersOnly: 11980, withheld: 324, trimmed: 449 }

/** Kinds of mail a connection can ask for. A consent screen shows a query in these words. */
export type QueryId = 'flights-and-hotels' | 'invoices' | 'receipts' | 'bank-statements' | 'newsletters'

export type QueryKind = { id: QueryId; name: string; detail: string; matches: number }

export const queryKinds: QueryKind[] = [
  { id: 'flights-and-hotels', name: 'Flights and hotels', detail: 'Flight, hotel, rail and car bookings', matches: 47 },
  { id: 'invoices', name: 'Invoices', detail: 'Invoices and bills', matches: 212 },
  { id: 'receipts', name: 'Receipts', detail: 'Order and payment receipts', matches: 388 },
  { id: 'bank-statements', name: 'Bank statements', detail: 'Bank and card statements', matches: 96 },
  { id: 'newsletters', name: 'Newsletters', detail: 'Mail sent to a mailing list', matches: 1204 },
]

export function queryKind(id: QueryId): QueryKind {
  return queryKinds.find((q) => q.id === id) ?? queryKinds[0]
}

/** One query a connection ran, as its page lists it. */
export type QueryRun = { when: string; query: string; result: string; refused?: boolean }

export type Connection = {
  id: string
  name: string
  initials: string
  domain: string
  kind: 'App' | 'Agent'
  delivery: 'API' | 'MCP'
  /** The approved query in plain words. For an agent, the scope its own searches stay inside. */
  asksFor: string
  /** The approved query as it runs. An agent writes its own searches inside its scope. */
  query: string
  readCount: number
  readLabel: string
  hasRead: number
  parts: string
  actions: string
  time: string
  lastUsed: string
  lastUsedShort: string
  unused?: boolean
  ends: string
  overviewDetail: string
  connectedVia: string
  flag?: string
  runs: QueryRun[]
}

export const connections: Connection[] = [
  {
    id: 'trip-app',
    name: 'Trip App',
    initials: 'TA',
    domain: 'trip-app.com',
    kind: 'App',
    delivery: 'API',
    asksFor: 'Flights and hotels',
    query: 'from:(iberia.com OR booking.com OR renfe.es OR airbnb.com …) subject:(booking OR reservation OR itinerary)',
    readCount: 47,
    readLabel: '47 emails',
    hasRead: 9,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 18 May',
    lastUsed: '2 hours ago',
    lastUsedShort: '2 hours ago',
    ends: '18 May',
    overviewDetail: 'API, used 2 hours ago',
    connectedVia: 'App connected through the API',
    runs: [
      { when: 'Today', query: 'Its query, new mail since Sunday', result: '1 email' },
      { when: 'Sun', query: 'Its query, new mail since Thursday', result: '1 email' },
      { when: 'Thu', query: 'Its query, new mail since Monday', result: 'Nothing new' },
    ],
  },
  {
    id: 'claude',
    name: 'Claude',
    initials: 'CL',
    domain: 'claude.ai',
    kind: 'Agent',
    delivery: 'MCP',
    asksFor: 'Flights and hotels, Invoices',
    query: 'Its own searches, inside that scope',
    readCount: 259,
    readLabel: '259 emails',
    hasRead: 38,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 4 June',
    lastUsed: '12 minutes ago',
    lastUsedShort: '12 min ago',
    ends: '4 June',
    overviewDetail: 'MCP, used 12 minutes ago',
    connectedVia: 'Agent connected through MCP',
    runs: [
      { when: 'Today', query: '"attachment of Invoice F-2291"', result: 'Refused: attachments are not part of its access', refused: true },
      { when: 'Today', query: '"invoice holded february"', result: '1 email' },
      { when: 'Today', query: '"vodafone bill march"', result: '1 email' },
      { when: 'Mon', query: '"car insurance renewal"', result: '0 emails' },
      { when: 'Mon', query: '"trips to lisbon or valencia"', result: '2 emails' },
    ],
  },
  {
    id: 'ledgerly',
    name: 'Ledgerly',
    initials: 'LE',
    domain: 'ledgerly.app',
    kind: 'App',
    delivery: 'API',
    asksFor: 'Invoices, Receipts',
    query: 'from:(holded.com OR vodafone.es OR iberdrola.es …) OR subject:(invoice OR factura OR receipt OR recibo)',
    readCount: 600,
    readLabel: '600 emails',
    hasRead: 112,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 2 Sep',
    lastUsed: 'Today, 09:14',
    lastUsedShort: 'Today, 09:14',
    ends: '2 Sep',
    overviewDetail: 'API, asking for more',
    connectedVia: 'App connected through the API',
    flag: 'Wants bank statements',
    runs: [
      { when: 'Today', query: 'Its query, new mail since yesterday', result: '1 email' },
      { when: 'Mon', query: 'Bank statements since January', result: 'Refused: outside its query. It asked you to add them', refused: true },
      { when: 'Mon', query: 'Its query, new mail since Sunday', result: '1 email' },
    ],
  },
  {
    id: 'inbox-tidy',
    name: 'Inbox Tidy',
    initials: 'IT',
    domain: 'inboxtidy.io',
    kind: 'App',
    delivery: 'API',
    asksFor: 'All mail',
    query: 'in:anywhere -in:spam -in:trash',
    readCount: 11980,
    readLabel: '11,980 headers',
    hasRead: 0,
    parts: 'Headers only',
    actions: 'Label, archive',
    time: 'All mail, until 30 Apr',
    lastUsed: '64 days ago',
    lastUsedShort: '64 days ago',
    unused: true,
    ends: '30 Apr',
    overviewDetail: 'API, unused for 64 days',
    connectedVia: 'App connected through the API',
    flag: 'Unused for 64 days',
    runs: [],
  },
]

export const formerConnections = [{ id: 'mailwise', name: 'Mailwise', revoked: '3 February', held: 120 }]

export type AttentionItem = {
  id: string
  area: 'connections' | 'rules'
  kind: 'decision' | 'suggestion'
  title: string
  detail: string
  action: string
  /** Where the action goes. */
  to: string
}

export const attention: AttentionItem[] = [
  {
    id: 'ledgerly-widening',
    area: 'connections',
    kind: 'decision',
    title: 'Ledgerly wants to add bank statements',
    detail: 'Would add 96 emails to what it can read',
    action: 'Review',
    to: '/consent/ledgerly',
  },
  {
    id: 'inbox-tidy-unused',
    area: 'connections',
    kind: 'suggestion',
    title: "Inbox Tidy hasn't used its access in 64 days",
    detail: 'You can revoke it without losing anything',
    action: 'Revoke',
    to: '/connections/inbox-tidy',
  },
  {
    id: 'private-new-address',
    area: 'rules',
    kind: 'suggestion',
    title: 'Your doctor wrote from a new address',
    detail: 'Private covers e.vidal@clinic.example, not clinicanova.example',
    action: 'Check rule',
    to: '/rules/private?add=clinicanova.example',
  },
]

/** hold: the whole email is held back. remove: only the matching details are taken out, and the rest goes through. */
export type RuleEffect = 'hold' | 'remove'

export type Rule = {
  id: string
  name: string
  description: string
  effect: RuleEffect
  /** What a remove rule takes out, such as "the address". */
  removes?: string
  /** Every connection, including ones approved later, or only these connection ids. */
  appliesTo: 'all' | string[]
  basedOnText: string
  matches: number
  matchNote?: string
  changed: string
}

export const rules: Rule[] = [
  {
    id: 'sign-in-and-security',
    name: 'Sign-in and security',
    description: 'Password resets, login links, one-time codes and security alerts',
    effect: 'hold',
    appliesTo: 'all',
    basedOnText: 'Suggested by Permaily',
    matches: 300,
    changed: '9 Jan',
  },
  {
    id: 'private',
    name: 'Private',
    description: 'My doctor, my bank and the Family label',
    effect: 'hold',
    appliesTo: 'all',
    basedOnText: 'Your own rule',
    matches: 24,
    matchNote: 'new sender',
    changed: '2 Feb',
  },
  {
    id: 'home-address',
    name: 'Home address',
    description: 'My address, wherever it appears',
    effect: 'remove',
    removes: 'the address',
    appliesTo: 'all',
    basedOnText: 'Your own rule',
    matches: 412,
    changed: '14 Feb',
  },
  {
    id: 'children',
    name: 'Children',
    description: "Lucía's and Pablo's names",
    effect: 'remove',
    removes: 'their names',
    appliesTo: 'all',
    basedOnText: 'Your own rule',
    matches: 37,
    changed: '20 Feb',
  },
  {
    id: 'car',
    name: 'Car',
    description: 'Insurance, inspections and fines for my car',
    effect: 'hold',
    appliesTo: ['claude'],
    basedOnText: 'Your own rule',
    matches: 18,
    changed: '3 Mar',
  },
]

/** "Holds back the email" or "Removes the address" */
export function effectText(rule: Pick<Rule, 'effect' | 'removes'>): string {
  return rule.effect === 'hold' ? 'Holds back the email' : `Removes ${rule.removes ?? 'the details'}`
}

/** "Every connection" or "Claude", from the connection ids a rule applies to. */
export function appliesToText(appliesTo: Rule['appliesTo']): string {
  if (appliesTo === 'all') return 'Every connection'
  const names = appliesTo.map((id) => connections.find((c) => c.id === id)?.name ?? id)
  return names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}

/**
 * The rule Permaily suggests on every new account. It's an ordinary rule that holds back the email
 * from every connection, so people can change it or delete it later.
 */
export const suggestedRule = {
  id: 'sign-in-and-security',
  name: 'Sign-in and security',
  why: 'Anyone who reads these can take over the account they belong to.',
  covers: [
    { title: 'Password resets and login links', count: 88 },
    { title: 'One-time codes', count: 171 },
    { title: 'Security alerts', count: 41 },
  ],
}

export type Receipt = {
  id: string
  day: 'Today' | 'Monday' | 'Sunday' | 'Saturday' | 'Last week'
  time: string
  connectionId: string
  sender: string
  from: string
  subject: string
  parts: string
  bodyChars: number
  attachment: 'withheld' | 'none'
  /** The query that fetched it: an agent's own search, or an app's approved query. */
  query: string
  /** Details a rule took out before it left. */
  removed?: { what: string; ruleId: string }
  approved: string
  request: string
}

/** This week's receipts, newest first, plus a few from last week for connection pages. */
export const receipts: Receipt[] = [
  { id: 'r_8f21c4', day: 'Today', time: '14:02', connectionId: 'claude', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2291, February', parts: 'subject, body', bodyChars: 1204, attachment: 'withheld', query: '"invoice holded february"', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_8f21c3', day: 'Today', time: '14:02', connectionId: 'claude', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', query: '"flight to lisbon"', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_7d10a9', day: 'Today', time: '11:40', connectionId: 'trip-app', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', query: 'Flights and hotels', approved: 'Approved by you on 17 Feb', request: 'messages.list' },
  { id: 'r_7c0b12', day: 'Today', time: '09:31', connectionId: 'claude', sender: 'Vodafone', from: 'facturas@vodafone.es', subject: 'Your March bill is ready', parts: 'subject, body', bodyChars: 860, attachment: 'withheld', query: '"vodafone bill march"', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_7b9e04', day: 'Today', time: '09:14', connectionId: 'ledgerly', sender: 'Amazon', from: 'auto-confirm@amazon.es', subject: 'Your receipt, order 402-881', parts: 'subject, body', bodyChars: 1530, attachment: 'none', query: 'Invoices, Receipts', removed: { what: 'the address', ruleId: 'home-address' }, approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_6f33a1', day: 'Monday', time: '18:47', connectionId: 'claude', sender: 'Booking.com', from: 'noreply@booking.com', subject: 'Hotel reservation, 14 to 17 March', parts: 'subject, body', bodyChars: 2875, attachment: 'none', query: '"hotel in lisbon"', removed: { what: "Lucía's and Pablo's names", ruleId: 'children' }, approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_6f339e', day: 'Monday', time: '18:46', connectionId: 'claude', sender: 'Renfe', from: 'billetes@renfe.es', subject: 'Madrid to Valencia, 6 April', parts: 'subject, body', bodyChars: 1102, attachment: 'none', query: '"trips to lisbon or valencia"', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_6e0c77', day: 'Monday', time: '11:20', connectionId: 'ledgerly', sender: 'Iberdrola', from: 'facturas@iberdrola.es', subject: 'Factura de luz, febrero', parts: 'subject, body', bodyChars: 940, attachment: 'withheld', query: 'Invoices, Receipts', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_5a91d0', day: 'Sunday', time: '20:03', connectionId: 'trip-app', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', query: 'Flights and hotels', approved: 'Approved by you on 17 Feb', request: 'messages.list' },
  { id: 'r_5a8f2b', day: 'Sunday', time: '09:12', connectionId: 'ledgerly', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', query: 'Invoices, Receipts', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_4c22e8', day: 'Saturday', time: '16:40', connectionId: 'ledgerly', sender: 'Endesa', from: 'facturas@endesa.es', subject: 'Factura de gas, enero', parts: 'subject, body', bodyChars: 990, attachment: 'withheld', query: 'Invoices, Receipts', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_3b10f5', day: 'Last week', time: 'Thu 16:08', connectionId: 'claude', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', query: '"airbnb porto"', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_3a07c9', day: 'Last week', time: 'Tue 10:15', connectionId: 'claude', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', query: '"invoice holded january"', approved: 'Approved by you on 6 March', request: 'get_message' },
]

/** Emails that left per day for the last 14 days, oldest first. The last 7 add up to 74. */
export const dailyCounts = [5, 8, 3, 6, 9, 4, 7, 5, 11, 31, 6, 8, 9, 4]
export const spike = { index: 9, label: 'Fri: 31 emails, 3 times usual' }

export type Mailbox = {
  id: 'personal' | 'work'
  address: string
  label: string
  method: 'forwarding' | 'google'
  receives: string
  history: string
  actions: string
  usedBy: string[]
  status: string
  statusDetail: string
}

export const mailboxes: Mailbox[] = [
  {
    id: 'personal',
    address: 'marta.ruiz@example.com',
    label: 'Personal, forwarding',
    method: 'forwarding',
    receives: 'Only mail your connections ask for',
    history: 'New mail only',
    actions: 'None',
    usedBy: ['TA'],
    status: 'Receiving',
    statusDetail: 'Last mail 4 minutes ago',
  },
  {
    id: 'work',
    address: 'marta@estudioruiz.example',
    label: 'Work, Google account',
    method: 'google',
    receives: 'Fetched when a connection asks, never stored',
    history: 'All mail since 2019',
    actions: 'Label, archive, draft',
    usedBy: ['TA', 'CL', 'LE', 'IT'],
    status: 'Connected',
    statusDetail: 'Token renewed today',
  },
]

export function connectionById(id: string | undefined): Connection | undefined {
  return connections.find((c) => c.id === id)
}

export function attentionCount(area: AttentionItem['area']): number {
  return attention.filter((item) => item.area === area).length
}

export function formatCount(n: number): string {
  return n.toLocaleString('en-US')
}
