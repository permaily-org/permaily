/*
  Sample data for the screens, matching the numbers in the designs so they add up across pages.
  Addresses for people use reserved example domains. Company senders are illustrative.
*/
import type { Cell } from '../components/ui/marks.tsx'

export const user = { name: 'Marta Ruiz', email: 'marta.ruiz@example.com' }

export const inbox = { total: 12304, readable: 647, headersOnly: 11980, withheld: 324 }

/** Overview columns, in order. Rules withheld from all come after these as one last column that nobody can read. */
export const ruleColumns = [
  { id: 'flights-and-hotels', name: 'Flights and hotels', count: 47 },
  { id: 'invoices', name: 'Invoices', count: 212 },
  { id: 'receipts', name: 'Receipts', count: 388 },
  { id: 'newsletters', name: 'Newsletters', count: 1204 },
  { id: 'everything-else', name: 'Everything else', count: 10129 },
] as const

export type Connection = {
  id: string
  name: string
  initials: string
  domain: string
  kind: 'App' | 'Agent'
  delivery: 'API' | 'MCP'
  /** One cell per Overview column, not counting the withheld rules. */
  cells: Cell[]
  readCount: number
  readLabel: string
  hasRead: number
  ruleNames: string
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
}

export const connections: Connection[] = [
  {
    id: 'trip-app',
    name: 'Trip App',
    initials: 'TA',
    domain: 'trip-app.com',
    kind: 'App',
    delivery: 'API',
    cells: ['shared', 'withheld', 'withheld', 'withheld', 'withheld'],
    readCount: 47,
    readLabel: '47 emails',
    hasRead: 9,
    ruleNames: 'Flights and hotels',
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 18 May',
    lastUsed: '2 hours ago',
    lastUsedShort: '2 hours ago',
    ends: '18 May',
    overviewDetail: 'API, used 2 hours ago',
    connectedVia: 'App connected through the API',
  },
  {
    id: 'claude',
    name: 'Claude',
    initials: 'CL',
    domain: 'claude.ai',
    kind: 'Agent',
    delivery: 'MCP',
    cells: ['shared', 'shared', 'withheld', 'withheld', 'withheld'],
    readCount: 259,
    readLabel: '259 emails',
    hasRead: 38,
    ruleNames: 'Flights and hotels, Invoices',
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 4 June',
    lastUsed: '12 minutes ago',
    lastUsedShort: '12 min ago',
    ends: '4 June',
    overviewDetail: 'MCP, used 12 minutes ago',
    connectedVia: 'Agent connected through MCP',
  },
  {
    id: 'ledgerly',
    name: 'Ledgerly',
    initials: 'LE',
    domain: 'ledgerly.app',
    kind: 'App',
    delivery: 'API',
    cells: ['withheld', 'shared', 'shared', 'withheld', 'withheld'],
    readCount: 600,
    readLabel: '600 emails',
    hasRead: 112,
    ruleNames: 'Invoices, Receipts',
    parts: 'Subject and body text',
    actions: 'Read only',
    time: 'Mail since 1 Jan 2026, until 2 Sep',
    lastUsed: 'Today, 09:14',
    lastUsedShort: 'Today, 09:14',
    ends: '2 Sep',
    overviewDetail: 'API, asking for more',
    connectedVia: 'App connected through the API',
    flag: 'Wants bank statements',
  },
  {
    id: 'inbox-tidy',
    name: 'Inbox Tidy',
    initials: 'IT',
    domain: 'inboxtidy.io',
    kind: 'App',
    delivery: 'API',
    cells: ['headers', 'headers', 'headers', 'headers', 'headers'],
    readCount: 11980,
    readLabel: '11,980 headers',
    hasRead: 0,
    ruleNames: 'All mail',
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
  /** Where the action goes. "pack-update" opens the pack update dialog on Overview. */
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
    id: 'travel-bookings-v4',
    area: 'rules',
    kind: 'decision',
    title: 'travel.bookings has a new version',
    detail: 'Adds 2 senders, so Trip App and Claude need your approval',
    action: 'Review',
    to: 'pack-update',
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
    id: 'invoices-spike',
    area: 'rules',
    kind: 'suggestion',
    title: 'Invoices matched 3 times more mail than usual this week',
    detail: 'Worth checking before more is shared',
    action: 'Check rule',
    to: '/rules/invoices',
  },
]

export type Rule = {
  id: string
  name: string
  description: string
  pack?: string
  basedOnText?: string
  basedOnNote?: string
  basedOnFlag?: boolean
  matches: number
  matchNote?: string
  usedBy: string[]
  usedByNote?: string
  /** Withheld from every connection. Wins over any rule a connection uses. */
  withheld?: boolean
  changed: string
}

export const rules: Rule[] = [
  {
    id: 'flights-and-hotels',
    name: 'Flights and hotels',
    description: 'Booking data, or travel senders with booking words',
    pack: 'travel.bookings@3',
    basedOnNote: 'plus 1 sender, version 4 ready',
    basedOnFlag: true,
    matches: 47,
    usedBy: ['TA', 'CL'],
    changed: '12 Mar',
  },
  {
    id: 'invoices',
    name: 'Invoices',
    description: 'Billing senders, or subject has invoice or factura',
    pack: 'finance.invoices@2',
    matches: 212,
    matchNote: '3x on Fri',
    usedBy: ['CL', 'LE'],
    changed: '20 Feb',
  },
  {
    id: 'receipts',
    name: 'Receipts',
    description: 'Order and payment receipts',
    pack: 'finance.receipts@1',
    matches: 388,
    usedBy: ['LE'],
    changed: '20 Feb',
  },
  {
    id: 'newsletters',
    name: 'Newsletters',
    description: 'Anything with a mailing list header',
    basedOnText: 'Your own rule',
    matches: 1204,
    usedBy: [],
    usedByNote: 'Not used',
    changed: '9 Jan',
  },
  {
    id: 'bank-statements',
    name: 'Bank statements',
    description: 'Statements from your banks',
    pack: 'finance.statements@1',
    matches: 96,
    usedBy: [],
    usedByNote: 'Ledgerly is asking',
    changed: '16 Mar',
  },
  {
    id: 'everything',
    name: 'All mail',
    description: "Everything in your mailboxes, minus what's withheld",
    basedOnText: 'Built in',
    matches: 11980,
    usedBy: ['IT'],
    usedByNote: 'headers only',
    changed: 'Built in',
  },
  {
    id: 'sign-in-and-security',
    name: 'Sign-in and security',
    description: 'Password resets, login links, one-time codes and security alerts',
    basedOnText: 'Suggested by Permaily',
    matches: 300,
    usedBy: [],
    withheld: true,
    changed: '9 Jan',
  },
  {
    id: 'private',
    name: 'Private',
    description: 'My doctor, my bank and the Family label',
    basedOnText: 'Your own rule',
    matches: 24,
    usedBy: [],
    withheld: true,
    changed: '2 Feb',
  },
]

/** Rules withheld from every connection. Their matches add up to inbox.withheld. */
export const withheldRules = rules.filter((rule) => rule.withheld)

/** "Sign-in and security and Private" */
export const withheldNames = withheldRules.map((rule) => rule.name).join(' and ')

/**
 * The rule Permaily suggests on every new account. It's an ordinary rule, withheld from every
 * connection, so people can change it or turn it off later.
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

export type Pack = { id: string; description: string; yours: number; state: 'update' | 'in-use' | 'available' }

export const packs: Pack[] = [
  { id: 'travel.bookings', description: 'Flight, hotel, rail and car bookings', yours: 47, state: 'update' },
  { id: 'finance.invoices', description: 'Invoices from common billing services', yours: 212, state: 'in-use' },
  { id: 'finance.receipts', description: 'Order and payment receipts', yours: 388, state: 'in-use' },
  { id: 'finance.statements', description: 'Bank and card statements', yours: 96, state: 'available' },
  { id: 'shopping.orders', description: 'Orders and delivery updates', yours: 143, state: 'available' },
  { id: 'utilities.bills', description: 'Power, water, phone and internet', yours: 64, state: 'available' },
  { id: 'subscriptions.renewals', description: 'Renewals and price changes', yours: 37, state: 'available' },
  { id: 'events.tickets', description: 'Tickets for concerts and events', yours: 18, state: 'available' },
]

export const packCategories = ['All', 'Travel', 'Finance', 'Shopping', 'Bills', 'Events'] as const

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
  ruleId: string
  ruleTag: string
  approved: string
  request: string
}

/** This week's receipts, newest first, plus a few from last week for connection pages. */
export const receipts: Receipt[] = [
  { id: 'r_8f21c4', day: 'Today', time: '14:02', connectionId: 'claude', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2291, February', parts: 'subject, body', bodyChars: 1204, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_8f21c3', day: 'Today', time: '14:02', connectionId: 'claude', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_7d10a9', day: 'Today', time: '11:40', connectionId: 'trip-app', sender: 'Iberia', from: 'confirmaciones@iberia.com', subject: 'Your flight to Lisbon is confirmed', parts: 'subject, body', bodyChars: 2310, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 17 Feb', request: 'messages.list' },
  { id: 'r_7c0b12', day: 'Today', time: '09:31', connectionId: 'claude', sender: 'Vodafone', from: 'facturas@vodafone.es', subject: 'Your March bill is ready', parts: 'subject, body', bodyChars: 860, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_7b9e04', day: 'Today', time: '09:14', connectionId: 'ledgerly', sender: 'Amazon', from: 'auto-confirm@amazon.es', subject: 'Your receipt, order 402-881', parts: 'subject, body', bodyChars: 1530, attachment: 'none', ruleId: 'receipts', ruleTag: 'receipts', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_6f33a1', day: 'Monday', time: '18:47', connectionId: 'claude', sender: 'Booking.com', from: 'noreply@booking.com', subject: 'Hotel reservation, 14 to 17 March', parts: 'subject, body', bodyChars: 2875, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_6f339e', day: 'Monday', time: '18:46', connectionId: 'claude', sender: 'Renfe', from: 'billetes@renfe.es', subject: 'Madrid to Valencia, 6 April', parts: 'subject, body', bodyChars: 1102, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_6e0c77', day: 'Monday', time: '11:20', connectionId: 'ledgerly', sender: 'Iberdrola', from: 'facturas@iberdrola.es', subject: 'Factura de luz, febrero', parts: 'subject, body', bodyChars: 940, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_5a91d0', day: 'Sunday', time: '20:03', connectionId: 'trip-app', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 17 Feb', request: 'messages.list' },
  { id: 'r_5a8f2b', day: 'Sunday', time: '09:12', connectionId: 'ledgerly', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_4c22e8', day: 'Saturday', time: '16:40', connectionId: 'ledgerly', sender: 'Endesa', from: 'facturas@endesa.es', subject: 'Factura de gas, enero', parts: 'subject, body', bodyChars: 990, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 4 March', request: 'messages.list' },
  { id: 'r_3b10f5', day: 'Last week', time: 'Thu 16:08', connectionId: 'claude', sender: 'Airbnb', from: 'automated@airbnb.com', subject: 'Reservation confirmed in Porto', parts: 'subject, body', bodyChars: 3120, attachment: 'none', ruleId: 'flights-and-hotels', ruleTag: 'flights-and-hotels', approved: 'Approved by you on 6 March', request: 'get_message' },
  { id: 'r_3a07c9', day: 'Last week', time: 'Tue 10:15', connectionId: 'claude', sender: 'Holded', from: 'facturas@holded.com', subject: 'Invoice F-2277, January', parts: 'subject, body', bodyChars: 1188, attachment: 'withheld', ruleId: 'invoices', ruleTag: 'invoices', approved: 'Approved by you on 6 March', request: 'get_message' },
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
    receives: 'Only mail matching your rules',
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
