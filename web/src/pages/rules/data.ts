/*
  Page-only content for Rules, Community packs and the rule editor.
  Shared numbers come from src/data/sample.ts. Company senders are illustrative.
*/
import { connections, packs, rules, type Pack, type Rule } from '../../data/sample.ts'

export function connectionNames(initials: string[]): string[] {
  return initials.map((i) => connections.find((c) => c.initials === i)?.name ?? i)
}

/** "Trip App", "Trip App and Claude", "Trip App, Claude and Ledgerly" */
export function joinNames(names: string[]): string {
  if (names.length < 2) return names.join('')
  return `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`
}

export type PackCategory = 'All' | 'Travel' | 'Finance' | 'Shopping' | 'Bills' | 'Events'

/** Category chips filter by the first part of the pack id. */
export const categoryPrefixes: Record<Exclude<PackCategory, 'All'>, string[]> = {
  Travel: ['travel'],
  Finance: ['finance'],
  Shopping: ['shopping'],
  Bills: ['utilities', 'subscriptions'],
  Events: ['events'],
}

export function packPrefix(id: string): string {
  return id.split('.')[0] ?? id
}

/** Structured data an email can carry, such as a flight reservation. Machine names stay in the pack source. */
export type PackData = { label: string; editorLabel: string; machine: string[]; plain: string[] }

export type PackSource = {
  version: number
  data?: PackData
  senders?: string[]
  subject?: string[]
  header?: string[]
  reviewedBy: number
  contributors: number
  checks: string
  update?: { version: number; adds: string[] }
}

const freeMailCheck = 'No free-mail domains or wildcard senders'

export const packSources: Record<string, PackSource> = {
  'travel.bookings': {
    version: 3,
    data: {
      label: 'Booking data',
      editorLabel: 'has booking data',
      machine: ['FlightReservation', 'LodgingReservation', 'TrainReservation', 'RentalCarReservation'],
      plain: ['flight', 'hotel', 'train', 'car hire'],
    },
    senders: [
      'iberia.com',
      'booking.com',
      'airbnb.com',
      'renfe.es',
      'ryanair.com',
      'easyjet.com',
      'expedia.com',
      'hotels.com',
      'marriott.com',
      'hilton.com',
      'europcar.com',
      'sixt.com',
    ],
    subject: ['booking', 'reservation', 'itinerary'],
    reviewedBy: 3,
    contributors: 41,
    checks: freeMailCheck,
    update: { version: 4, adds: ['vueling.com', 'flixbus.com'] },
  },
  'finance.invoices': {
    version: 2,
    senders: ['holded.com', 'vodafone.es', 'iberdrola.es', 'endesa.es', 'movistar.es', 'orange.es', 'stripe.com', 'paddle.com', 'quaderno.io'],
    subject: ['invoice', 'factura'],
    reviewedBy: 4,
    contributors: 28,
    checks: freeMailCheck,
  },
  'finance.receipts': {
    version: 1,
    data: { label: 'Order data', editorLabel: 'has order data', machine: ['Order', 'Invoice'], plain: ['order', 'payment'] },
    subject: ['receipt', 'recibo'],
    reviewedBy: 2,
    contributors: 19,
    checks: freeMailCheck,
  },
  'finance.statements': {
    version: 1,
    senders: ['santander.es', 'ing.es', 'revolut.com', 'caixabank.es', 'n26.com', 'openbank.es'],
    subject: ['statement', 'extracto'],
    reviewedBy: 3,
    contributors: 22,
    checks: 'No free-mail domains. Never share still applies on top',
  },
  'shopping.orders': {
    version: 2,
    data: { label: 'Order data', editorLabel: 'has order data', machine: ['Order', 'ParcelDelivery'], plain: ['order', 'delivery'] },
    senders: ['amazon.es', 'zalando.es', 'elcorteingles.es', 'aliexpress.com', 'ikea.com'],
    reviewedBy: 2,
    contributors: 33,
    checks: freeMailCheck,
  },
  'utilities.bills': {
    version: 1,
    senders: ['iberdrola.es', 'endesa.es', 'naturgy.es', 'vodafone.es', 'movistar.es', 'digi.es'],
    subject: ['factura', 'bill'],
    reviewedBy: 2,
    contributors: 15,
    checks: freeMailCheck,
  },
  'subscriptions.renewals': {
    version: 1,
    senders: ['spotify.com', 'netflix.com', 'apple.com', 'adobe.com'],
    subject: ['renewal', 'price change', 'renovación'],
    reviewedBy: 2,
    contributors: 12,
    checks: freeMailCheck,
  },
  'events.tickets': {
    version: 1,
    data: { label: 'Event data', editorLabel: 'has event data', machine: ['EventReservation'], plain: ['event ticket'] },
    senders: ['ticketmaster.es', 'entradas.com', 'dice.fm', 'eventbrite.com'],
    reviewedBy: 2,
    contributors: 9,
    checks: freeMailCheck,
  },
}

export function packById(id: string | null | undefined): Pack | undefined {
  return packs.find((p) => p.id === id)
}

/** The rule that uses a pack, if any. Rule packs carry a version, such as travel.bookings@3. */
export function ruleForPack(packId: string): Rule | undefined {
  return rules.find((r) => r.pack?.split('@')[0] === packId)
}

/** "a, b, c, and 9 more" */
export function listWithMore(items: string[], shown = 3): string {
  if (items.length <= shown) return items.join(', ')
  return `${items.slice(0, shown).join(', ')}, and ${items.length - shown} more`
}

/* Rule editor */

export type GroupKind = 'data' | 'sender' | 'subject' | 'header' | 'mailbox'

export type ConditionGroup = { id: string; kind: GroupKind; label: string; chips: string[] }

/** Groups inside a block must all match. Blocks are joined by "or". */
export type ConditionBlock = ConditionGroup[]

type MailSeed = { kind: 'mail'; date: string; sender: string; address: string; subject: string; keys?: string[] }

export type PreviewRow =
  | { kind: 'other'; widths: [number, number, number] }
  | (MailSeed & { id: string })
  | { kind: 'blocked'; id: string; date: string; sender: string; reason: string }

export type RuleSeed = {
  name: string
  description: string
  pack?: string
  basedOnText?: string
  /** Matches with the conditions as loaded. */
  count: number
  /** How many of those the saved rule didn't match yet. */
  newCount: number
  usedBy: string[]
  usedByNote?: string
  blocks: ConditionBlock[]
  butNot: string[]
  /** Positive chips the saved rule didn't have, so they count as part of this edit. */
  unsaved: string[]
  readOnly?: boolean
  rows: PreviewRow[]
}

let rowId = 0
function mail(date: string, sender: string, address: string, subject: string, keys?: string[]): PreviewRow {
  rowId += 1
  return { kind: 'mail', id: `m${rowId}`, date, sender, address, subject, keys }
}
function other(a: number, b: number, c: number): PreviewRow {
  return { kind: 'other', widths: [a, b, c] }
}
function blocked(date: string, sender: string, reason: string): PreviewRow {
  rowId += 1
  return { kind: 'blocked', id: `b${rowId}`, date, sender, reason }
}

const flightsRows: PreviewRow[] = [
  other(44, 92, 300),
  mail('12 Mar', 'Iberia', 'confirmaciones@iberia.com', 'Your flight to Lisbon is confirmed', ['flight']),
  other(38, 108, 240),
  mail('11 Mar', 'Iberia', 'news@iberia.com', 'Spring fares to Lisbon and Porto'),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March', ['hotel', 'reservation']),
  other(44, 100, 200),
  other(38, 84, 330),
  mail('04 Mar', 'Norwegian', 'noreply@norwegian.com', 'Your booking KX72QM'),
  other(44, 112, 260),
  mail('27 Feb', 'Renfe', 'billetes@renfe.es', 'Madrid to Valencia, 6 April', ['train']),
  mail('22 Feb', 'Norwegian', 'noreply@norwegian.com', 'Boarding pass, Oslo to Madrid'),
  other(38, 70, 320),
  mail('19 Feb', 'Airbnb', 'automated@airbnb.com', 'Reservation confirmed in Porto', ['reservation']),
  other(44, 96, 290),
  mail('11 Jan', 'Norwegian', 'noreply@norwegian.com', 'Receipt for your flight'),
]

const invoiceRows: PreviewRow[] = [
  other(44, 96, 280),
  mail('14 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2291, February', ['invoice']),
  other(38, 112, 220),
  mail('12 Mar', 'Vodafone', 'facturas@vodafone.es', 'Your March bill is ready'),
  other(44, 84, 320),
  mail('10 Mar', 'Iberdrola', 'facturas@iberdrola.es', 'Factura de luz, febrero', ['factura']),
  other(38, 100, 260),
  other(44, 76, 300),
  mail('02 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2277, January', ['invoice']),
  other(44, 108, 240),
  mail('28 Feb', 'Endesa', 'facturas@endesa.es', 'Factura de gas, enero', ['factura']),
  other(38, 92, 310),
]

const receiptRows: PreviewRow[] = [
  mail('14 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881', ['order', 'receipt']),
  other(44, 100, 260),
  other(38, 84, 300),
  mail('11 Mar', 'Apple', 'no_reply@email.apple.com', 'Your receipt from Apple', ['payment', 'receipt']),
  other(44, 112, 220),
  mail('08 Mar', 'Glovo', 'pedidos@glovo.com', 'Recibo de tu pedido', ['order', 'recibo']),
  other(38, 92, 330),
  other(44, 76, 250),
  mail('03 Mar', 'PayPal', 'service@paypal.es', 'Receipt for your payment to Zara', ['payment', 'receipt']),
  other(44, 104, 280),
]

const newsletterRows: PreviewRow[] = [
  mail('14 Mar', 'Spotify', 'news@spotify.com', 'New releases for you', ['List-Unsubscribe']),
  other(44, 96, 240),
  mail('13 Mar', 'Medium', 'noreply@medium.com', 'Your daily digest', ['List-Unsubscribe']),
  mail('12 Mar', 'Renfe', 'ofertas@renfe.es', 'Ofertas de primavera', ['List-Unsubscribe']),
  other(38, 112, 300),
  mail('10 Mar', 'Zara', 'newsletter@zara.com', 'New arrivals this week', ['List-Unsubscribe']),
  other(44, 84, 260),
  mail('09 Mar', 'IKEA', 'news@ikea.com', 'Ideas for small spaces', ['List-Unsubscribe']),
  other(44, 100, 320),
]

const statementRows: PreviewRow[] = [
  other(44, 96, 280),
  mail('06 Mar', 'Santander', 'avisos@santander.es', 'Extracto de febrero', ['extracto']),
  other(38, 108, 240),
  blocked('04 Mar', 'BBVA', 'never share: bbva.es'),
  mail('02 Mar', 'Revolut', 'no-reply@revolut.com', 'Your February statement', ['statement']),
  other(44, 84, 300),
  other(38, 100, 260),
  mail('03 Feb', 'ING', 'extractos@ing.es', 'Extracto mensual, enero', ['extracto']),
  other(44, 92, 320),
]

const everythingRows: PreviewRow[] = [
  mail('14 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2291, February'),
  mail('14 Mar', 'Iberia', 'confirmaciones@iberia.com', 'Your flight to Lisbon is confirmed'),
  blocked('14 Mar', 'Google', 'never share: one-time code'),
  mail('13 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881'),
  mail('12 Mar', 'Vodafone', 'facturas@vodafone.es', 'Your March bill is ready'),
  blocked('12 Mar', 'Booking.com', 'never share: password reset'),
  mail('11 Mar', 'Spotify', 'news@spotify.com', 'New releases for you'),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March'),
  blocked('08 Mar', 'Dr. Elena Vidal', 'never share: added by you'),
  mail('06 Mar', 'Santander', 'avisos@santander.es', 'Extracto de febrero'),
]

/** A slice of the inbox for new rules. Nothing matches until a condition is added. */
const inboxRows: PreviewRow[] = [
  mail('14 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2291, February', ['invoice']),
  mail('14 Mar', 'Iberia', 'confirmaciones@iberia.com', 'Your flight to Lisbon is confirmed', ['flight']),
  other(44, 96, 280),
  mail('13 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881', ['order', 'receipt']),
  mail('12 Mar', 'Vodafone', 'facturas@vodafone.es', 'Your March bill is ready', ['bill']),
  other(38, 108, 240),
  mail('11 Mar', 'Spotify', 'news@spotify.com', 'Your plan renews on 1 April', ['renewal']),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March', ['hotel', 'reservation']),
  other(44, 84, 320),
  mail('06 Mar', 'Santander', 'avisos@santander.es', 'Extracto de febrero', ['extracto', 'statement']),
  mail('04 Mar', 'Zalando', 'info@zalando.es', 'Your order is on its way', ['order', 'delivery']),
  other(38, 100, 260),
  mail('02 Mar', 'Ticketmaster', 'entradas@ticketmaster.es', 'Your tickets for 21 March', ['event ticket']),
  mail('27 Feb', 'Renfe', 'billetes@renfe.es', 'Madrid to Valencia, 6 April', ['train']),
  other(44, 92, 300),
]

function ruleFields(id: string) {
  const rule = rules.find((r) => r.id === id)
  if (!rule) throw new Error(`Unknown rule ${id}`)
  const { name, pack, basedOnText, matches, usedBy, usedByNote } = rule
  return { name, pack, basedOnText, count: matches, newCount: 0, usedBy, usedByNote, unsaved: [] }
}

function packGroups(source: PackSource): ConditionBlock[] {
  const blocks: ConditionBlock[] = []
  if (source.data) blocks.push([{ id: 'data', kind: 'data', label: source.data.editorLabel, chips: [...source.data.plain] }])
  const second: ConditionBlock = []
  if (source.senders) second.push({ id: 'from', kind: 'sender', label: 'is from', chips: [...source.senders] })
  if (source.subject) second.push({ id: 'subject', kind: 'subject', label: 'subject has', chips: [...source.subject] })
  if (second.length) blocks.push(second)
  return blocks
}

const travel = packSources['travel.bookings']!

const ruleSeeds: Record<string, () => RuleSeed> = {
  'flights-and-hotels': () => ({
    ...ruleFields('flights-and-hotels'),
    description: 'Flight and hotel bookings',
    count: 50,
    newCount: 3,
    blocks: [
      [{ id: 'data', kind: 'data', label: 'has booking data', chips: [...travel.data!.plain] }],
      [
        { id: 'from', kind: 'sender', label: 'is from', chips: ['norwegian.com', ...travel.senders!] },
        { id: 'subject', kind: 'subject', label: 'subject has', chips: [...travel.subject!] },
      ],
    ],
    butNot: ['news@iberia.com', 'deals@booking.com'],
    unsaved: ['norwegian.com'],
    rows: flightsRows,
  }),
  invoices: () => ({
    ...ruleFields('invoices'),
    description: 'Invoices and bills',
    blocks: [
      [{ id: 'from', kind: 'sender', label: 'is from', chips: [...packSources['finance.invoices']!.senders!] }],
      [{ id: 'subject', kind: 'subject', label: 'subject has', chips: ['invoice', 'factura'] }],
    ],
    butNot: [],
    rows: invoiceRows,
  }),
  receipts: () => ({
    ...ruleFields('receipts'),
    description: 'Receipts for things I buy',
    blocks: packGroups(packSources['finance.receipts']!),
    butNot: [],
    rows: receiptRows,
  }),
  newsletters: () => ({
    ...ruleFields('newsletters'),
    description: 'Newsletters and mailing lists',
    blocks: [[{ id: 'header', kind: 'header', label: 'has header', chips: ['List-Unsubscribe'] }]],
    butNot: [],
    rows: newsletterRows,
  }),
  'bank-statements': () => ({
    ...ruleFields('bank-statements'),
    description: 'Monthly statements from my banks',
    blocks: packGroups(packSources['finance.statements']!),
    butNot: [],
    rows: statementRows,
  }),
  everything: () => ({
    ...ruleFields('everything'),
    description: "All my mail, minus what's on Never share",
    blocks: [[{ id: 'mailbox', kind: 'mailbox', label: 'is in', chips: ['marta.ruiz@example.com', 'marta@estudioruiz.example'] }]],
    butNot: [],
    readOnly: true,
    rows: everythingRows,
  }),
}

/** A new rule, empty or with a community pack loaded for preview. */
function newRuleSeed(packId: string | null): RuleSeed {
  const pack = packById(packId)
  const source = pack && packSources[pack.id]
  if (pack && source) {
    return {
      name: pack.description,
      description: pack.description,
      pack: `${pack.id}@${source.version}`,
      count: pack.yours,
      newCount: pack.yours,
      usedBy: [],
      usedByNote: 'Not used yet',
      blocks: packGroups(source),
      butNot: [],
      unsaved: [],
      rows: inboxRows,
    }
  }
  return {
    name: 'New rule',
    description: '',
    basedOnText: 'Your own rule',
    count: 0,
    newCount: 0,
    usedBy: [],
    usedByNote: 'Not used yet',
    blocks: [
      [
        { id: 'from', kind: 'sender', label: 'is from', chips: [] },
        { id: 'subject', kind: 'subject', label: 'subject has', chips: [] },
      ],
    ],
    butNot: [],
    unsaved: [],
    rows: inboxRows,
  }
}

export function ruleSeed(id: string, packId: string | null): RuleSeed | undefined {
  if (id === 'new') return newRuleSeed(packId)
  return ruleSeeds[id]?.()
}
