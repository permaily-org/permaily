/*
  Sample data for the screens, matching the numbers in the designs so they add up across pages.
  Addresses for people use reserved example domains. Company senders are illustrative.

  A vendor is the company behind an app, agent or service, such as Claude. It holds one or more
  connections, and each connection is one query on the person's mail with its own access. Claude's
  Morning summary and its Finances are two connections, and rules can target either one.

  Two things decide what a connection gets, in this order:
  1. Rules, which the person writes. They run first, on the whole mailbox, and hold back emails or
     remove details from them. Connections are never told about them.
  2. The connection's query. It runs on what the rules left, and the approved query caps what that
     connection can fetch.
*/

export const user = { name: 'Marta Ruiz', email: 'marta.ruiz@example.com' }

/**
 * readable: emails at least one connection can read in full, the union of the queries below.
 * withheld: held back from every connection by rules. trimmed: emails that rules remove details from.
 */
export const inbox = { total: 12304, readable: 752, headersOnly: 11980, withheld: 324, trimmed: 449 }

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

export type Vendor = {
  id: string
  name: string
  initials: string
  domain: string
  kind: 'App' | 'Agent'
  delivery: 'API' | 'MCP'
}

export const vendors: Vendor[] = [
  { id: 'claude', name: 'Claude', initials: 'CL', domain: 'claude.ai', kind: 'Agent', delivery: 'MCP' },
  { id: 'trip-app', name: 'Trip App', initials: 'TA', domain: 'trip-app.com', kind: 'App', delivery: 'API' },
  { id: 'ledgerly', name: 'Ledgerly', initials: 'LE', domain: 'ledgerly.app', kind: 'App', delivery: 'API' },
  { id: 'inbox-tidy', name: 'Inbox Tidy', initials: 'IT', domain: 'inboxtidy.io', kind: 'App', delivery: 'API' },
]

/**
 * When a connection's query runs: when the vendor asks, on a schedule the vendor declared when it
 * asked for access, or when new mail matches, which Permaily sends to the vendor's webhook.
 */
export type Trigger = { kind: 'ask' } | { kind: 'schedule'; at: string } | { kind: 'new-mail' }

/** One query a connection ran, as its page lists it. */
export type QueryRun = { when: string; query: string; result: string; refused?: boolean }

export type Connection = {
  /** "vendor/connection", which is also its path under /connections. */
  id: string
  vendorId: string
  /** The name the vendor gave it when it asked, such as "Morning summary". */
  name: string
  /** The approved query in plain words. For an agent's own searches, the scope they stay inside. */
  asksFor: string
  /** The approved query as it runs. An agent writes its own searches inside its scope. */
  query: string
  trigger: Trigger
  /** Where Permaily sends new matching mail, for connections that run on new mail. */
  webhook?: { url: string; lastCall: string }
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
  approved: string
  flag?: string
  runs: QueryRun[]
}

export const connections: Connection[] = [
  {
    id: 'claude/chat',
    vendorId: 'claude',
    name: 'Chat',
    asksFor: 'Flights and hotels, Invoices',
    query: 'Its own searches, inside that scope',
    trigger: { kind: 'ask' },
    readCount: 259,
    readLabel: '259 emails',
    hasRead: 38,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 4 June',
    lastUsed: '12 minutes ago',
    lastUsedShort: '12 min ago',
    ends: '4 June',
    approved: 'Approved by you on 6 March',
    runs: [
      { when: 'Today', query: '"attachment of Invoice F-2291"', result: 'Refused, attachments are not part of its access', refused: true },
      { when: 'Today', query: '"invoice holded february"', result: '1 email' },
      { when: 'Today', query: '"vodafone bill march"', result: '1 email' },
      { when: 'Mon', query: '"car insurance renewal"', result: '0 emails' },
      { when: 'Mon', query: '"trips to lisbon or valencia"', result: '2 emails' },
    ],
  },
  {
    id: 'claude/morning-summary',
    vendorId: 'claude',
    name: 'Morning summary',
    asksFor: 'All new mail',
    query: 'in:inbox category:primary newer_than:1d',
    trigger: { kind: 'schedule', at: '07:00' },
    readCount: 9,
    readLabel: '9 emails',
    hasRead: 64,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'New mail only, until 2 June',
    lastUsed: 'Today, 07:00',
    lastUsedShort: 'Today, 07:00',
    ends: '2 June',
    approved: 'Approved by you on 2 March',
    runs: [
      { when: 'Today', query: '07:00, new mail since yesterday', result: '9 emails' },
      { when: 'Mon', query: '07:00, new mail since Sunday', result: '7 emails' },
      { when: 'Sun', query: '07:00, new mail since Saturday', result: '4 emails' },
    ],
  },
  {
    id: 'claude/finances',
    vendorId: 'claude',
    name: 'Finances',
    asksFor: 'Invoices, Receipts, Bank statements',
    query: 'from:(holded.com OR amazon.es OR santander.es …) OR subject:(invoice OR receipt OR extracto)',
    trigger: { kind: 'new-mail' },
    webhook: { url: 'https://claude.ai/hooks/…4f2a', lastCall: 'Today, 11:02' },
    readCount: 696,
    readLabel: '696 emails',
    hasRead: 23,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'New mail only, until 4 June',
    lastUsed: 'Today, 11:02',
    lastUsedShort: 'Today, 11:02',
    ends: '4 June',
    approved: 'Approved by you on 6 March',
    runs: [
      { when: 'Today', query: 'Webhook call, new mail since Monday', result: '1 email' },
      { when: 'Mon', query: 'Webhook call, new mail since Saturday', result: '1 email' },
      { when: 'Sat', query: 'Webhook call, new mail since Thursday', result: '2 emails' },
    ],
  },
  {
    id: 'trip-app/bookings',
    vendorId: 'trip-app',
    name: 'Bookings',
    asksFor: 'Flights and hotels',
    query: 'from:(iberia.com OR booking.com OR renfe.es OR airbnb.com …) subject:(booking OR reservation OR itinerary)',
    trigger: { kind: 'new-mail' },
    webhook: { url: 'https://api.trip-app.com/hooks/…91c0', lastCall: 'Today, 11:40' },
    readCount: 47,
    readLabel: '47 emails',
    hasRead: 9,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 18 May',
    lastUsed: '2 hours ago',
    lastUsedShort: '2 hours ago',
    ends: '18 May',
    approved: 'Approved by you on 17 Feb',
    runs: [
      { when: 'Today', query: 'Webhook call, new mail since Sunday', result: '1 email' },
      { when: 'Sun', query: 'Webhook call, new mail since Thursday', result: '1 email' },
    ],
  },
  {
    id: 'ledgerly/bookkeeping',
    vendorId: 'ledgerly',
    name: 'Bookkeeping',
    asksFor: 'Invoices, Receipts',
    query: 'from:(holded.com OR vodafone.es OR iberdrola.es …) OR subject:(invoice OR factura OR receipt OR recibo)',
    trigger: { kind: 'schedule', at: '09:00' },
    readCount: 600,
    readLabel: '600 emails',
    hasRead: 112,
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 2 Sep',
    lastUsed: 'Today, 09:14',
    lastUsedShort: 'Today, 09:14',
    ends: '2 Sep',
    approved: 'Approved by you on 4 March',
    flag: 'Wants bank statements',
    runs: [
      { when: 'Today', query: '09:00, new mail since yesterday', result: '1 email' },
      { when: 'Mon', query: 'Bank statements since January', result: 'Refused, outside its query', refused: true },
      { when: 'Mon', query: '09:00, new mail since Sunday', result: '1 email' },
    ],
  },
  {
    id: 'inbox-tidy/cleanup',
    vendorId: 'inbox-tidy',
    name: 'Cleanup',
    asksFor: 'All mail',
    query: 'in:anywhere -in:spam -in:trash',
    trigger: { kind: 'ask' },
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
    approved: 'Approved by you on 3 Jan',
    flag: 'Unused for 64 days',
    runs: [],
  },
]

export const formerVendors = [{ id: 'mailwise', name: 'Mailwise', revoked: '3 Feb', held: 120 }]

export function vendorById(id: string | undefined): Vendor | undefined {
  return vendors.find((v) => v.id === id)
}

export function connectionById(id: string | undefined): Connection | undefined {
  return connections.find((c) => c.id === id)
}

export function connectionsOf(vendorId: string): Connection[] {
  return connections.filter((c) => c.vendorId === vendorId)
}

export function vendorOf(connection: Connection): Vendor {
  const vendor = vendorById(connection.vendorId)
  if (!vendor) throw new Error(`Unknown vendor ${connection.vendorId}`)
  return vendor
}

/** "Claude · Morning summary" */
export function connectionLabel(connection: Connection): string {
  return `${vendorOf(connection).name} · ${connection.name}`
}

/** "When asked", "Daily, 07:00" or "On new mail". */
export function triggerShort(trigger: Trigger): string {
  if (trigger.kind === 'schedule') return `Daily, ${trigger.at}`
  return trigger.kind === 'new-mail' ? 'On new mail' : 'When asked'
}

/** "When asked", "Every day at 07:00" or "When new mail matches". */
export function triggerText(trigger: Trigger): string {
  if (trigger.kind === 'schedule') return `Every day at ${trigger.at}`
  return trigger.kind === 'new-mail' ? 'When new mail matches' : 'When asked'
}

export type AttentionItem = {
  id: string
  area: 'connections' | 'rules'
  kind: 'decision' | 'suggestion'
  /** The connection it's about, so its row and page can repeat it. */
  connectionId?: string
  title: string
  detail?: string
  action: string
  /** Where the action goes. */
  to: string
}

export const attention: AttentionItem[] = [
  {
    id: 'ledgerly-widening',
    area: 'connections',
    kind: 'decision',
    connectionId: 'ledgerly/bookkeeping',
    title: 'Ledgerly wants to add bank statements',
    detail: '96 more emails',
    action: 'Review',
    to: '/consent/ledgerly',
  },
  {
    id: 'inbox-tidy-unused',
    area: 'connections',
    kind: 'suggestion',
    connectionId: 'inbox-tidy/cleanup',
    title: "Inbox Tidy hasn't used its access in 64 days",
    action: 'Revoke',
    to: '/connections/inbox-tidy/cleanup',
  },
  {
    id: 'private-new-address',
    area: 'rules',
    kind: 'suggestion',
    title: 'Your doctor wrote from a new address',
    detail: 'clinicanova.example',
    action: 'Check rule',
    to: '/rules/private?add=clinicanova.example',
  },
]

/** hold: the whole email is held back. remove: only the matching details are taken out, and the rest goes through. */
export type RuleEffect = 'hold' | 'remove'

/**
 * Who a rule applies to. A target is a vendor id, such as "claude", which covers all of that
 * vendor's connections, including ones it adds later, or a connection id, such as
 * "claude/morning-summary", which covers only that connection. "all" includes connections
 * approved later, minus any exceptions.
 */
export type AppliesTo = { to: 'all'; except: string[] } | { to: 'some'; only: string[] }

export const everyConnection: AppliesTo = { to: 'all', except: [] }

export type Rule = {
  id: string
  name: string
  description: string
  effect: RuleEffect
  /** What a remove rule takes out, such as "the address". */
  removes?: string
  appliesTo: AppliesTo
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
    appliesTo: everyConnection,
    basedOnText: 'Suggested by Permaily',
    matches: 300,
    changed: '9 Jan',
  },
  {
    id: 'private',
    name: 'Private',
    description: 'My doctor, my bank and the Family label',
    effect: 'hold',
    appliesTo: everyConnection,
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
    // Morning summary checks that orders carry the right address, so it gets the address.
    appliesTo: { to: 'all', except: ['claude/morning-summary'] },
    basedOnText: 'Your own rule',
    matches: 412,
    changed: '3 Mar',
  },
  {
    id: 'children',
    name: 'Children',
    description: "Leo's and Alba's names",
    effect: 'remove',
    removes: 'their names',
    appliesTo: everyConnection,
    basedOnText: 'Your own rule',
    matches: 37,
    changed: '20 Feb',
  },
  {
    id: 'car',
    name: 'Car',
    description: 'Insurance, inspections and fines for my car',
    effect: 'hold',
    appliesTo: { to: 'some', only: ['claude'] },
    basedOnText: 'Your own rule',
    matches: 18,
    changed: '3 Mar',
  },
]

/** "Holds back the email" or "Removes the address" */
export function effectText(rule: Pick<Rule, 'effect' | 'removes'>): string {
  return rule.effect === 'hold' ? 'Holds back the email' : `Removes ${rule.removes ?? 'the details'}`
}

/** "Trip App", "Trip App and Claude", "Trip App, Claude and Ledgerly" */
export function joinNames(names: string[]): string {
  if (names.length < 2) return names.join('')
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}

/** A vendor target covers all its connections. A connection target covers itself. */
export function isConnectionTarget(target: string): boolean {
  return target.includes('/')
}

/** "Claude" for a vendor, "Claude · Morning summary" for one connection. */
export function targetLabel(target: string): string {
  if (!isConnectionTarget(target)) return vendorById(target)?.name ?? target
  const connection = connectionById(target)
  return connection ? connectionLabel(connection) : target
}

/** The vendor a target belongs to, for its tile. */
export function targetVendor(target: string): Vendor | undefined {
  return vendorById(target.split('/')[0])
}

function covers(target: string, connection: Connection): boolean {
  return isConnectionTarget(target) ? connection.id === target : connection.vendorId === target
}

/** The connections a rule applies to today. */
export function ruleConnections(appliesTo: AppliesTo): Connection[] {
  return appliesTo.to === 'all'
    ? connections.filter((c) => !appliesTo.except.some((t) => covers(t, c)))
    : connections.filter((c) => appliesTo.only.some((t) => covers(t, c)))
}

/** Whether a rule applies to this connection now. */
export function ruleCovers(appliesTo: AppliesTo, connection: Connection): boolean {
  return ruleConnections(appliesTo).includes(connection)
}

/** Targets with this connection taken out. A vendor that covered it is replaced by its other connections. */
function without(targets: string[], connection: Connection): string[] {
  const siblings = connections.filter((c) => c.vendorId === connection.vendorId && c.id !== connection.id).map((c) => c.id)
  return [...new Set(targets.flatMap((t) => (!covers(t, connection) ? [t] : isConnectionTarget(t) ? [] : siblings)))]
}

/** The same rule, applied to this connection or not, as when it's ticked on the connection's page. */
export function withConnection(appliesTo: AppliesTo, connection: Connection, on: boolean): AppliesTo {
  if (on === ruleCovers(appliesTo, connection)) return appliesTo
  if (appliesTo.to === 'all') {
    return on ? { to: 'all', except: without(appliesTo.except, connection) } : { to: 'all', except: [...appliesTo.except, connection.id] }
  }
  return on ? { to: 'some', only: [...appliesTo.only, connection.id] } : { to: 'some', only: without(appliesTo.only, connection) }
}

/** "Every connection", "Every connection except Claude · Morning summary", or "Claude and Trip App". */
export function appliesToText(appliesTo: AppliesTo): string {
  if (appliesTo.to === 'some') return appliesTo.only.length === 0 ? 'No connection' : joinNames(appliesTo.only.map(targetLabel))
  if (appliesTo.except.length === 0) return 'Every connection'
  return `Every connection except ${joinNames(appliesTo.except.map(targetLabel))}`
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
  /** The query that fetched it: an agent's own search, or the connection's approved query. */
  query: string
  /** Details a rule took out before it left. */
  removed?: { what: string; ruleId: string }
  request: string
}

/** This week's receipts, newest first, plus a few from last week for connection pages. */
export const receipts: Receipt[] = [
  { id: 'r_8f21c4', day: 'Today', time: '14:02', connectionId: 'claude/chat', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2291, February', parts: 'subject, body', bodyChars: 1204, attachment: 'withheld', query: '"invoice holded february"', request: 'get_message' },
  { id: 'r_8f21c3', day: 'Today', time: '14:02', connectionId: 'claude/chat', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', query: '"flight to lisbon"', request: 'get_message' },
  { id: 'r_7d10a9', day: 'Today', time: '11:40', connectionId: 'trip-app/bookings', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', query: 'Flights and hotels', request: 'webhook' },
  { id: 'r_7d02b7', day: 'Today', time: '11:02', connectionId: 'claude/finances', sender: 'Iberdrola', from: 'facturas@iberdrola.es', subject: 'Factura de luz, marzo', parts: 'subject, body', bodyChars: 960, attachment: 'withheld', query: 'Invoices, Receipts, Bank statements', removed: { what: 'the address', ruleId: 'home-address' }, request: 'webhook' },
  { id: 'r_7c0b12', day: 'Today', time: '09:31', connectionId: 'claude/chat', sender: 'Vodafone', from: 'facturas@vodafone.es', subject: 'Your March bill is ready', parts: 'subject, body', bodyChars: 860, attachment: 'withheld', query: '"vodafone bill march"', request: 'get_message' },
  { id: 'r_7b9e04', day: 'Today', time: '09:14', connectionId: 'ledgerly/bookkeeping', sender: 'Amazon', from: 'auto-confirm@amazon.es', subject: 'Your receipt, order 402-881', parts: 'subject, body', bodyChars: 1530, attachment: 'none', query: 'Invoices, Receipts', removed: { what: 'the address', ruleId: 'home-address' }, request: 'messages.list' },
  { id: 'r_7a0e51', day: 'Today', time: '07:00', connectionId: 'claude/morning-summary', sender: 'Zalando', from: 'info@zalando.es', subject: 'Your order is on its way', parts: 'subject, body', bodyChars: 1410, attachment: 'none', query: 'All new mail', request: 'messages.list' },
  { id: 'r_7a0e4f', day: 'Today', time: '07:00', connectionId: 'claude/morning-summary', sender: 'Colegio San José', from: 'secretaria@colegio-sanjose.example', subject: 'Class trip on Friday', parts: 'subject, body', bodyChars: 720, attachment: 'none', query: 'All new mail', removed: { what: "Leo's and Alba's names", ruleId: 'children' }, request: 'messages.list' },
  { id: 'r_6f33a1', day: 'Monday', time: '18:47', connectionId: 'claude/chat', sender: 'Booking.com', from: 'noreply@booking.com', subject: 'Hotel reservation, 14 to 17 March', parts: 'subject, body', bodyChars: 2875, attachment: 'none', query: '"hotel in lisbon"', removed: { what: "Leo's and Alba's names", ruleId: 'children' }, request: 'get_message' },
  { id: 'r_6f339e', day: 'Monday', time: '18:46', connectionId: 'claude/chat', sender: 'Renfe', from: 'billetes@renfe.es', subject: 'Madrid to Valencia, 6 April', parts: 'subject, body', bodyChars: 1102, attachment: 'none', query: '"trips to lisbon or valencia"', request: 'get_message' },
  { id: 'r_6e0c77', day: 'Monday', time: '11:20', connectionId: 'ledgerly/bookkeeping', sender: 'Iberdrola', from: 'facturas@iberdrola.es', subject: 'Factura de luz, febrero', parts: 'subject, body', bodyChars: 940, attachment: 'withheld', query: 'Invoices, Receipts', request: 'messages.list' },
  { id: 'r_6e5b10', day: 'Monday', time: '08:15', connectionId: 'claude/finances', sender: 'Santander', from: 'avisos@santander.es', subject: 'Extracto de febrero', parts: 'subject, body', bodyChars: 1880, attachment: 'withheld', query: 'Invoices, Receipts, Bank statements', request: 'webhook' },
  { id: 'r_6e2a03', day: 'Monday', time: '07:00', connectionId: 'claude/morning-summary', sender: 'Correos', from: 'avisos@correos.es', subject: 'Your parcel is waiting', parts: 'subject, body', bodyChars: 640, attachment: 'none', query: 'All new mail', request: 'messages.list' },
  { id: 'r_5a91d0', day: 'Sunday', time: '20:03', connectionId: 'trip-app/bookings', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', query: 'Flights and hotels', request: 'webhook' },
  { id: 'r_5a8f2b', day: 'Sunday', time: '09:12', connectionId: 'ledgerly/bookkeeping', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', query: 'Invoices, Receipts', request: 'messages.list' },
  { id: 'r_4c22e8', day: 'Saturday', time: '16:40', connectionId: 'ledgerly/bookkeeping', sender: 'Endesa', from: 'facturas@endesa.es', subject: 'Factura de gas, enero', parts: 'subject, body', bodyChars: 990, attachment: 'withheld', query: 'Invoices, Receipts', request: 'messages.list' },
  { id: 'r_3b10f5', day: 'Last week', time: 'Thu 16:08', connectionId: 'claude/chat', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', query: '"airbnb porto"', request: 'get_message' },
  { id: 'r_3b0c11', day: 'Last week', time: 'Thu 09:12', connectionId: 'claude/finances', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', query: 'Invoices, Receipts, Bank statements', request: 'webhook' },
  { id: 'r_3ad7e0', day: 'Last week', time: 'Wed 18:20', connectionId: 'claude/finances', sender: 'Amazon', from: 'auto-confirm@amazon.es', subject: 'Your receipt, order 402-117', parts: 'subject, body', bodyChars: 1490, attachment: 'none', query: 'Invoices, Receipts, Bank statements', removed: { what: 'the address', ruleId: 'home-address' }, request: 'webhook' },
  { id: 'r_3a07c9', day: 'Last week', time: 'Tue 10:15', connectionId: 'claude/chat', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', query: '"invoice holded january"', request: 'get_message' },
  { id: 'r_3a01f8', day: 'Last week', time: 'Tue 08:40', connectionId: 'claude/finances', sender: 'Endesa', from: 'facturas@endesa.es', subject: 'Factura de gas, enero', parts: 'subject, body', bodyChars: 990, attachment: 'withheld', query: 'Invoices, Receipts, Bank statements', request: 'webhook' },
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
  /** Connection ids. */
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
    usedBy: ['trip-app/bookings'],
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
    usedBy: connections.map((c) => c.id),
    status: 'Connected',
    statusDetail: 'Token renewed today',
  },
]

export function attentionCount(area: AttentionItem['area']): number {
  return attention.filter((item) => item.area === area).length
}

export function formatCount(n: number): string {
  return n.toLocaleString('en-US')
}
