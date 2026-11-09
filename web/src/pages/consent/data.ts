/*
  Preview mail for the consent screens. Each screen has its own timeline: a written head that
  matches the design, and a generated tail so every count can be checked row by row.
*/
import { type QueryId, queryKind } from '../../data/sample.ts'

export type { QueryId }

export type InboxEmail = {
  id: string
  date: string
  sender: string
  subject: string
  /** The kind of mail this email is. Mail no query asks for is never shared. */
  query?: QueryId
  /**
   * One of the person's rules holds it back. The connection is never told, so on consent screens it
   * looks like any other mail the connection doesn't get.
   */
  held?: boolean
  widths: [number, number, number]
}

export type Timeline = { head: InboxEmail[]; tail: InboxEmail[] }

export type RowKind = 'withheld' | 'shared' | 'soft' | 'new' | 'excluded'

export type PreviewRow =
  | { id: string; kind: 'withheld'; widths: [number, number, number] }
  | { id: string; kind: 'shared' | 'soft' | 'new'; date: string; sender: string; subject: string }
  | { id: string; kind: 'excluded'; date: string; sender: string; reason: string; barWidth: number }

export type Lens = (email: InboxEmail) => RowKind

export const mailSince = 'Mail since 1 Jan 2026'
export const endOptions = ['15 June', '15 May', '15 April'] as const
export type EndDate = (typeof endOptions)[number]

/** Kinds of mail a person can let an agent search. */
export const agentQueryIds: QueryId[] = [
  'flights-and-hotels',
  'invoices',
  'receipts',
  'bank-statements',
  'newsletters',
]

export function queryName(id: QueryId): string {
  return queryKind(id).name
}

/** How many emails a query gets today, after the person's rules. */
export function queryCount(id: QueryId): number {
  return queryKind(id).matches
}

/* Requesters, as their consent screens introduce them. */

export const requesters = {
  tripApp: {
    name: 'Trip App',
    initials: 'TA',
    domain: 'trip-app.com',
    facts: ['Domain verified', 'Registered 2 years ago', 'Used by 41,200 people'],
  },
  claude: {
    name: 'Claude',
    initials: 'CL',
    domain: 'claude.ai',
    facts: ['Domain verified', 'Connected through MCP'],
  },
  ledgerly: {
    name: 'Ledgerly',
    initials: 'LE',
    domain: 'ledgerly.app',
    facts: ['Domain verified', 'Registered 4 years ago', 'Used by 18,300 people'],
  },
}

/* Choices behind each Change action. */

export type Choice = { value: string; label: string; note?: string }

export const appPartsChoices: Choice[] = [
  {
    value: 'Subject and body text, no attachments',
    label: 'Subject and body text, no attachments',
    note: 'What Trip App asked for',
  },
  {
    value: 'Subject only, no body text',
    label: 'Subject only, no body text',
    note: 'Trip App may not be able to read booking details',
  },
]

export const agentPartsChoices: Choice[] = [
  { value: 'Subject and body text', label: 'Subject and body text' },
  { value: 'Subject only', label: 'Subject only' },
  { value: 'Subject, body text and attachments', label: 'Subject, body text and attachments' },
]

export const agentActionChoices: Choice[] = [
  { value: 'Read only', label: 'Read only' },
  {
    value: 'Read and draft replies',
    label: 'Read and draft replies',
    note: 'Drafts stay in your mailbox until you send them',
  },
  { value: 'Read, label and archive', label: 'Read, label and archive' },
]

export function endChoices(asked: string): Choice[] {
  return endOptions.map((end) => ({
    value: end,
    label: `Until ${end}`,
    note: end === endOptions[0] ? asked : undefined,
  }))
}

/* What a query matches. Forwarding mailboxes run the same search in Gmail. */

type QueryMatch = {
  details: Array<{ label: string; value: string; mono?: boolean }>
  senders: string[]
  words: string[]
  query?: string
}

const travelSenders = [
  'iberia.com',
  'booking.com',
  'airbnb.com',
  'renfe.com',
  'vueling.com',
  'ryanair.com',
  'nh-hotels.com',
  'hertz.com',
  'alsa.es',
  'easyjet.com',
  'expedia.com',
  'trainline.com',
]
const invoiceSenders = [
  'holded.com',
  'vodafone.es',
  'iberdrola.es',
  'endesa.es',
  'movistar.es',
  'adobe.com',
  'figma.com',
  'google.com',
]
const receiptSenders = [
  'amazon.es',
  'apple.com',
  'glovoapp.com',
  'uber.com',
  'cabify.com',
  'ikea.com',
  'paypal.com',
  'zara.com',
]
const statementSenders = [
  'santander.es',
  'ing.es',
  'revolut.com',
  'caixabank.es',
  'n26.com',
  'americanexpress.com',
]

function senderSummary(senders: string[]): string {
  return senders.length > 3
    ? `${senders.slice(0, 3).join(', ')} and ${senders.length - 3} more`
    : senders.join(', ')
}

const queryMatches: Record<QueryId, QueryMatch> = {
  'flights-and-hotels': {
    details: [{ label: 'Booking data', value: 'Flights, hotels, trains and car hire' }],
    senders: travelSenders,
    words: ['booking', 'reservation', 'itinerary', 'confirmation'],
  },
  invoices: { details: [], senders: invoiceSenders, words: ['invoice', 'factura', 'bill'] },
  receipts: { details: [], senders: receiptSenders, words: ['receipt', 'order', 'recibo'] },
  'bank-statements': { details: [], senders: statementSenders, words: ['statement', 'extracto'] },
  newsletters: {
    details: [{ label: 'List header', value: 'Any mail sent to a mailing list' }],
    senders: [],
    words: [],
    query: 'unsubscribe',
  },
}

export function matchDetails(id: QueryId): Array<{ label: string; value: string; mono?: boolean }> {
  const match = queryMatches[id]
  const rows = [...match.details]
  if (match.senders.length)
    rows.push({ label: 'Senders', value: senderSummary(match.senders), mono: true })
  if (match.words.length)
    rows.push({ label: 'Subject has', value: match.words.join(', '), mono: true })
  if (match.query && !match.words.length)
    rows.push({ label: 'Has the word', value: match.query, mono: true })
  return rows
}

function fullQuery(id: QueryId): string {
  const { senders, words, query } = queryMatches[id]
  if (query) return query
  return [`from:(${senders.join(' OR ')})`, `subject:(${words.join(' OR ')})`].join(' ')
}

function shortQuery(id: QueryId): string {
  const { senders, words, query } = queryMatches[id]
  if (query) return query
  return `from:(${senders[0]} OR ${senders[1]} …) subject:(${words[0]} …)`
}

export function gmailQuery(ids: QueryId[]): string {
  return ids.length === 1 ? fullQuery(ids[0]) : ids.map((id) => `(${fullQuery(id)})`).join(' OR ')
}

/** The query as a consent screen shows it, shortened to fit one line. */
export function gmailShortQuery(ids: QueryId[]): string {
  return ids.length === 1 ? shortQuery(ids[0]) : `(${shortQuery(ids[0])}) OR …`
}

export function gmailSearchUrl(query: string): string {
  return `https://mail.google.com/mail/u/0/#search/${encodeURIComponent(query)}`
}

/* Turning a timeline into rows. */

export function viewRows(timeline: Timeline, lens: Lens): PreviewRow[] {
  const rows = timeline.head.map((email) => toRow(email, lens(email)))
  let run = 0
  for (const email of timeline.tail) {
    const kind = lens(email)
    run = kind === 'withheld' ? run + 1 : 0
    if (run <= 2) rows.push(toRow(email, kind))
  }
  return rows
}

function toRow(email: InboxEmail, kind: RowKind): PreviewRow {
  const { id, date, sender, subject } = email
  if (kind === 'withheld') return { id, kind, widths: email.widths }
  if (kind === 'excluded')
    return {
      id,
      kind,
      date,
      sender,
      reason: 'excluded by you',
      barWidth: Math.min(360, subject.length * 7),
    }
  return { id, kind, date, sender, subject }
}

/** Mail the person's rules hold back looks like any other mail the connection doesn't get. */
export function sharedLens(
  asked: ReadonlySet<QueryId>,
  excludedSenders: ReadonlySet<string> = new Set(),
): Lens {
  return (email) => {
    if (!email.query || !asked.has(email.query) || email.held) return 'withheld'
    return excludedSenders.has(email.sender) ? 'excluded' : 'shared'
  }
}

export function wideningLens(existing: ReadonlySet<QueryId>, added: ReadonlySet<QueryId>): Lens {
  return (email) => {
    if (!email.query || email.held) return 'withheld'
    if (added.has(email.query)) return 'new'
    if (existing.has(email.query)) return 'soft'
    return 'withheld'
  }
}

/* Timelines. */

type Head = Omit<InboxEmail, 'id'>

const other = (date: string, widths: [number, number, number]): Head => ({
  date,
  sender: '',
  subject: '',
  widths,
})

const tripHead: Head[] = [
  other('13 Mar', [44, 92, 410]),
  {
    date: '12 Mar',
    sender: 'Iberia',
    subject: 'Your flight to Lisbon is confirmed',
    query: 'flights-and-hotels',
    widths: [44, 76, 520],
  },
  other('11 Mar', [38, 108, 300]),
  other('10 Mar', [44, 76, 520]),
  {
    date: '09 Mar',
    sender: 'Booking.com',
    subject: 'Hotel reservation, 14 to 17 March',
    query: 'flights-and-hotels',
    widths: [38, 104, 360],
  },
  other('07 Mar', [44, 100, 240]),
  other('04 Mar', [38, 84, 460]),
  {
    date: '02 Mar',
    sender: 'Booking.com',
    subject: 'Reset your password',
    query: 'flights-and-hotels',
    held: true,
    widths: [44, 96, 280],
  },
  other('01 Mar', [44, 112, 350]),
  {
    date: '27 Feb',
    sender: 'Renfe',
    subject: 'Madrid to Valencia, 6 April',
    query: 'flights-and-hotels',
    widths: [38, 88, 430],
  },
  other('24 Feb', [38, 70, 490]),
  other('21 Feb', [44, 96, 280]),
  {
    date: '19 Feb',
    sender: 'Airbnb',
    subject: 'Reservation confirmed in Porto',
    query: 'flights-and-hotels',
    widths: [44, 80, 330],
  },
]

const agentHead: Head[] = [
  other('15 Mar', [44, 92, 410]),
  {
    date: '14 Mar',
    sender: 'Holded',
    subject: 'Invoice F-2291, February',
    query: 'invoices',
    widths: [44, 100, 240],
  },
  {
    date: '12 Mar',
    sender: 'Iberia',
    subject: 'Your flight to Lisbon is confirmed',
    query: 'flights-and-hotels',
    widths: [44, 76, 520],
  },
  {
    date: '11 Mar',
    sender: 'Amazon',
    subject: 'Your receipt, order 402-881',
    query: 'receipts',
    widths: [38, 108, 300],
  },
  {
    date: '11 Mar',
    sender: 'Vodafone',
    subject: 'Your March bill is ready',
    query: 'invoices',
    widths: [38, 84, 460],
  },
  {
    date: '10 Mar',
    sender: 'Morning Brew',
    subject: 'Issue 412',
    query: 'newsletters',
    widths: [44, 76, 520],
  },
  {
    date: '09 Mar',
    sender: 'Booking.com',
    subject: 'Hotel reservation, 14 to 17 March',
    query: 'flights-and-hotels',
    widths: [44, 112, 350],
  },
  {
    date: '05 Mar',
    sender: 'ING',
    subject: 'Your February statement',
    query: 'bank-statements',
    widths: [38, 84, 460],
  },
  {
    date: '02 Mar',
    sender: 'Booking.com',
    subject: 'Reset your password',
    query: 'flights-and-hotels',
    held: true,
    widths: [44, 96, 280],
  },
  {
    date: '27 Feb',
    sender: 'Renfe',
    subject: 'Madrid to Valencia, 6 April',
    query: 'flights-and-hotels',
    widths: [38, 88, 430],
  },
  {
    date: '26 Feb',
    sender: 'Iberdrola',
    subject: 'Factura de luz, febrero',
    query: 'invoices',
    widths: [44, 104, 360],
  },
  other('26 Feb', [38, 70, 490]),
  {
    date: '25 Feb',
    sender: 'Glovo',
    subject: 'Your Glovo order receipt',
    query: 'receipts',
    widths: [44, 96, 280],
  },
  other('25 Feb', [38, 80, 330]),
]

const ledgerlyHead: Head[] = [
  other('15 Mar', [44, 92, 410]),
  {
    date: '14 Mar',
    sender: 'Holded',
    subject: 'Invoice F-2291, February',
    query: 'invoices',
    widths: [44, 100, 240],
  },
  {
    date: '12 Mar',
    sender: 'Santander',
    subject: 'Extracto de febrero',
    query: 'bank-statements',
    widths: [44, 76, 520],
  },
  other('11 Mar', [38, 108, 300]),
  {
    date: '09 Mar',
    sender: 'Amazon',
    subject: 'Your receipt, order 402-881',
    query: 'receipts',
    widths: [38, 84, 460],
  },
  other('07 Mar', [44, 76, 520]),
  {
    date: '05 Mar',
    sender: 'ING',
    subject: 'Your February statement',
    query: 'bank-statements',
    widths: [44, 112, 350],
  },
  other('04 Mar', [38, 84, 460]),
  {
    date: '02 Mar',
    sender: 'Santander',
    subject: 'New sign-in to your account',
    query: 'bank-statements',
    held: true,
    widths: [44, 96, 280],
  },
  other('01 Mar', [44, 112, 350]),
  {
    date: '27 Feb',
    sender: 'Vodafone',
    subject: 'Your February bill',
    query: 'invoices',
    widths: [38, 88, 430],
  },
  other('20 Feb', [38, 70, 490]),
  {
    date: '12 Feb',
    sender: 'Santander',
    subject: 'Extracto de enero',
    query: 'bank-statements',
    widths: [44, 80, 330],
  },
]

const monthDays = [31, 28, 31]
const monthShort = ['Jan', 'Feb', 'Mar']
const monthEn = ['December', 'January', 'February']
const monthEs = ['diciembre', 'enero', 'febrero']

/** Day 0 is 1 January 2026. */
function dayLabel(day: number): string {
  let rest = day
  let month = 0
  while (rest >= monthDays[month]) rest -= monthDays[month++]
  return `${String(rest + 1).padStart(2, '0')} ${monthShort[month]}`
}

/** The month a bill sent on this day covers. */
function billMonth(day: number, lang: 'en' | 'es'): string {
  const i = day < 31 ? 0 : day < 59 ? 1 : 2
  return lang === 'en' ? monthEn[i] : monthEs[i]
}

const cities = [
  'Lisbon',
  'Porto',
  'Paris',
  'Rome',
  'London',
  'Berlin',
  'Amsterdam',
  'Milan',
  'Seville',
  'Bilbao',
  'Valencia',
  'Málaga',
]
const accounts = ['4021', '7730', '1185', '2290']
const spanishCities = [
  'Valencia',
  'Seville',
  'Barcelona',
  'Bilbao',
  'Málaga',
  'Granada',
  'Zaragoza',
]

type Template = (n: number, day: number) => [string, string]

const templates: Record<QueryId, Template[]> = {
  'flights-and-hotels': [
    (n) => ['Iberia', `Your flight to ${cities[n % 12]} is confirmed`],
    (n) => ['Booking.com', `Hotel reservation in ${cities[(n + 3) % 12]}`],
    (n) => ['Renfe', `Madrid to ${spanishCities[n % 7]}, your ticket`],
    (n) => ['Airbnb', `Reservation confirmed in ${cities[(n + 5) % 12]}`],
    (n) => ['Vueling', `Boarding pass, Barcelona to ${cities[(n + 1) % 12]}`],
    (n) => ['NH Hotels', `Your stay at NH ${cities[(n + 7) % 12]}`],
    (n) => ['Hertz', `Car hire in ${cities[(n + 2) % 12]} is confirmed`],
    (n) => ['Ryanair', `Your itinerary to ${cities[(n + 9) % 12]}`],
    (n) => ['Alsa', `Bus ticket to ${spanishCities[(n + 2) % 7]}`],
    (n) => ['Trainline', `Your e-ticket to ${cities[(n + 4) % 12]}`],
    (n) => ['easyJet', `Booking confirmation, ${cities[(n + 6) % 12]}`],
    (n) => ['Expedia', `Itinerary for your trip to ${cities[(n + 8) % 12]}`],
  ],
  invoices: [
    (n, d) => ['Holded', `Invoice F-${2100 + n}, ${billMonth(d, 'en')}`],
    (_, d) => ['Vodafone', `Your ${billMonth(d, 'en')} bill is ready`],
    (_, d) => ['Iberdrola', `Factura de luz, ${billMonth(d, 'es')}`],
    (_, d) => ['Endesa', `Factura de gas, ${billMonth(d, 'es')}`],
    (_, d) => ['Movistar', `Tu factura de ${billMonth(d, 'es')}`],
    (_, d) => ['Adobe', `Your Adobe invoice for ${billMonth(d, 'en')}`],
    (n) => ['Figma', `Invoice #${40210 + n}`],
    (_, d) => ['Google Workspace', `Your ${billMonth(d, 'en')} invoice is available`],
  ],
  receipts: [
    (n) => ['Amazon', `Your receipt, order 402-${100 + ((n * 37) % 900)}`],
    () => ['Apple', 'Your receipt from Apple'],
    () => ['Glovo', 'Your Glovo order receipt'],
    () => ['Uber', 'Your trip receipt'],
    () => ['Cabify', 'Your Cabify receipt'],
    () => ['IKEA', 'Your IKEA receipt'],
    () => ['PayPal', 'Receipt for your payment'],
    () => ['Zara', 'Your Zara order'],
  ],
  'bank-statements': [
    (n, d) => ['Santander', `Extracto de ${billMonth(d, 'es')}, cuenta ${accounts[n % 4]}`],
    (n, d) => ['ING', `Your ${billMonth(d, 'en')} statement, account ${accounts[(n + 1) % 4]}`],
    (n, d) => ['Revolut', `Your ${billMonth(d, 'en')} statement, card ${accounts[(n + 2) % 4]}`],
    (n, d) => [
      'CaixaBank',
      `Extracto mensual, ${billMonth(d, 'es')}, tarjeta ${accounts[(n + 3) % 4]}`,
    ],
    (n, d) => ['N26', `Account statement, ${billMonth(d, 'en')}, ${accounts[n % 4]}`],
    (n) => ['American Express', `Your statement for card ${accounts[(n + 1) % 4]} is ready`],
  ],
  newsletters: [
    (n) => ['Morning Brew', `Issue ${411 - n}`],
    () => ['Dense Discovery', 'This week, in five links'],
    () => ['The Browser', 'Your weekend reading list'],
    () => ['Monocle', 'The Monocle Minute'],
    () => ['Sidebar', 'Five design links, daily'],
    () => ['TLDR', 'Your daily digest'],
    () => ['Product Hunt', 'Top launches today'],
  ],
}

function random(seed: number): () => number {
  let s = seed
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0
    return s / 4294967296
  }
}

/** Generates the rest of each query's matches between 1 January and `before`, with other mail in between. */
function buildTail(
  prefix: string,
  counts: Partial<Record<QueryId, number>>,
  before: number,
  seed: number,
): InboxEmail[] {
  const next = random(seed)
  const matches: Array<{
    key: number
    day: number
    query: QueryId
    sender: string
    subject: string
  }> = []
  for (const [query, count] of Object.entries(counts) as Array<[QueryId, number]>) {
    const list = templates[query]
    for (let i = 0; i < count; i++) {
      const key = before - ((i + next()) * before) / count
      const day = Math.floor(key)
      const [sender, subject] = list[Math.floor(next() * list.length)](i, day)
      matches.push({ key, day, query, sender, subject })
    }
  }
  matches.sort((a, b) => b.key - a.key)

  const widths = (): [number, number, number] => [
    next() < 0.5 ? 38 : 44,
    70 + Math.floor(next() * 24) * 2,
    240 + Math.floor(next() * 29) * 10,
  ]
  const tail: InboxEmail[] = []
  for (const match of matches) {
    const date = dayLabel(match.day)
    for (let k = Math.floor(next() * 3); k > 0; k--)
      tail.push({ id: '', date, sender: '', subject: '', widths: widths() })
    tail.push({
      id: '',
      date,
      sender: match.sender,
      subject: match.subject,
      query: match.query,
      widths: widths(),
    })
  }
  return tail.map((email, i) => ({ ...email, id: `${prefix}-t${i}` }))
}

function remaining(
  head: Head[],
  counts: Partial<Record<QueryId, number>>,
): Partial<Record<QueryId, number>> {
  const rest: Partial<Record<QueryId, number>> = {}
  for (const [query, count] of Object.entries(counts) as Array<[QueryId, number]>) {
    rest[query] = count - head.filter((e) => e.query === query && !e.held).length
  }
  return rest
}

function timeline(
  prefix: string,
  head: Head[],
  counts: Partial<Record<QueryId, number>>,
  before: number,
  seed: number,
): Timeline {
  return {
    head: head.map((email, i) => ({ ...email, id: `${prefix}-h${i}` })),
    tail: buildTail(prefix, remaining(head, counts), before, seed),
  }
}

const countsFor = (ids: QueryId[]) =>
  Object.fromEntries(ids.map((id) => [id, queryCount(id)])) as Partial<Record<QueryId, number>>

/** Trip App asks for flights and hotels. The tail runs from 18 Feb back to 1 January. */
export const tripTimeline = timeline('trip', tripHead, countsFor(['flights-and-hotels']), 49, 7)

/** Claude can be let into any kind of mail. The tail runs from 24 Feb back. */
export const agentTimeline = timeline('agent', agentHead, countsFor(agentQueryIds), 55, 11)

/** Ledgerly reads invoices and receipts, and asks to add bank statements. The tail runs from 11 Feb back. */
export const ledgerlyTimeline = timeline(
  'ledgerly',
  ledgerlyHead,
  countsFor(['invoices', 'receipts', 'bank-statements']),
  42,
  19,
)
