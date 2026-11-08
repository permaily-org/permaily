/*
  Page-only content for Rules and the rule editor.
  Shared numbers come from src/data/sample.ts. Company senders are illustrative.
*/
import { everyConnection, rules, suggestedRule, type AppliesTo, type RuleEffect } from '../../data/sample.ts'

export { joinNames } from '../../data/sample.ts'

/* Rule editor */

/** about and text run on a model inside the verified service. The rest match on the email's own fields. */
export type GroupKind = 'about' | 'text' | 'type' | 'sender' | 'subject' | 'label'

export type ConditionGroup = { id: string; kind: GroupKind; label: string; chips: string[] }

/** Groups inside a block must all match. Blocks are joined by "or". */
export type ConditionBlock = ConditionGroup[]

/**
 * A sample email. keys are what it can match on besides its sender. Square brackets in the snippet
 * mark the detail a remove rule takes out.
 */
export type PreviewMail = { id: string; date: string; sender: string; address: string; subject: string; snippet?: string; keys?: string[] }

export type RuleSeed = {
  name: string
  description: string
  effect: RuleEffect
  removes: string
  appliesTo: AppliesTo
  /** Matches with the conditions as loaded. */
  count: number
  /** How many of those the saved rule didn't match yet. */
  newCount: number
  blocks: ConditionBlock[]
  butNot: string[]
  /** Positive chips the saved rule didn't have, so they count as part of this edit. */
  unsaved: string[]
  rows: PreviewMail[]
}

let rowId = 0
function mail(date: string, sender: string, address: string, subject: string, keys?: string[], snippet?: string): PreviewMail {
  rowId += 1
  return { id: `m${rowId}`, date, sender, address, subject, keys, snippet }
}

const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const dayOf = (row: PreviewMail) => months.indexOf(row.date.slice(3)) * 100 + Number(row.date.slice(0, 2))

/** Newest first, so senders added in an edit land among the rest. */
function newestFirst(rows: PreviewMail[]): PreviewMail[] {
  return [...rows].sort((a, b) => dayOf(b) - dayOf(a))
}

/** Mail a rule doesn't match yet. Hidden in its preview until an edit makes it match. */
const otherMail: PreviewMail[] = [
  mail('14 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2291, February', ['invoice']),
  mail('14 Mar', 'Iberia', 'confirmaciones@iberia.com', 'Your flight to Lisbon is confirmed', ['flight']),
  mail('12 Mar', 'Vodafone', 'facturas@vodafone.es', 'Your March bill is ready', ['bill']),
  mail('11 Mar', 'Spotify', 'news@spotify.com', 'Your plan renews on 1 April', ['renewal']),
  mail('06 Mar', 'Santander', 'avisos@santander.es', 'Extracto de febrero', ['statement']),
  mail('02 Mar', 'Ticketmaster', 'entradas@ticketmaster.es', 'Your tickets for 21 March', ['event ticket']),
  mail('27 Feb', 'Renfe', 'billetes@renfe.es', 'Madrid to Valencia, 6 April', ['train']),
  mail('19 Feb', 'Airbnb', 'automated@airbnb.com', 'Reservation confirmed in Porto', ['reservation']),
]

const signInRows = newestFirst([
  mail('14 Mar', 'Google', 'no-reply@accounts.google.com', 'Your verification code', ['one-time code']),
  mail('13 Mar', 'Revolut', 'no-reply@revolut.com', 'New sign-in from Madrid', ['security alert']),
  mail('12 Mar', 'Booking.com', 'noreply@booking.com', 'Reset your password', ['password reset']),
  mail('11 Mar', 'Iberia', 'no-reply@iberia.com', 'Your login link', ['login link']),
  mail('09 Mar', 'Santander', 'avisos@santander.es', 'New sign-in to your account', ['security alert']),
  mail('07 Mar', 'Amazon', 'account-update@amazon.es', 'Your one-time password', ['one-time code']),
  mail('05 Mar', 'Holded', 'no-reply@holded.com', 'Change your password', ['password reset']),
  mail('02 Mar', 'Apple', 'no_reply@email.apple.com', 'Your Apple ID was used to sign in', ['security alert']),
  ...otherMail,
])

const privateRows = newestFirst([
  mail('13 Mar', 'Dr. Elena Vidal', 'e.vidal@clinicanova.example', 'Your new patient portal'),
  mail('12 Mar', 'Dr. Elena Vidal', 'e.vidal@clinic.example', 'Your test results'),
  mail('08 Mar', 'Dr. Elena Vidal', 'e.vidal@clinic.example', 'Appointment on 20 March'),
  mail('04 Mar', 'BBVA', 'avisos@bbva.es', 'Tu extracto de febrero'),
  mail('02 Mar', 'Lucía Ruiz', 'lucia@example.com', 'Sunday lunch', ['Family']),
  mail('27 Feb', 'BBVA', 'avisos@bbva.es', 'Cambios en tu tarjeta'),
  mail('21 Feb', 'Pablo Ruiz', 'pablo@example.com', 'Photos from the weekend', ['Family']),
  ...otherMail,
])

const address = ['my home address']
const homeAddressRows = newestFirst([
  mail('13 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881', address, 'Delivering to [Calle Falsa 123, 4B], Madrid'),
  mail('12 Mar', 'Zalando', 'info@zalando.es', 'Your order is on its way', address, 'Arriving at [Calle Falsa 123, 4B] on Friday'),
  mail('10 Mar', 'Iberdrola', 'facturas@iberdrola.es', 'Factura de luz, febrero', address, 'Supply address: [Calle Falsa 123, 4B, 28000 Madrid]'),
  mail('06 Mar', 'Glovo', 'pedidos@glovo.com', 'Recibo de tu pedido', address, 'Delivered to [Calle Falsa 123]'),
  mail('02 Mar', 'Santander', 'avisos@santander.es', 'Your new card is on its way', address, 'Posted to [Calle Falsa 123, 4B, 28000 Madrid]'),
  mail('27 Feb', 'Correos', 'avisos@correos.es', 'Your parcel is waiting', address, 'We left a notice at [Calle Falsa 123]'),
  ...otherMail,
])

const kids = ['my children']
const childrenRows = newestFirst([
  mail('11 Mar', 'Colegio San José', 'secretaria@colegio-sanjose.example', 'Class trip on Friday', kids, 'Please sign the form for [Leo] by Wednesday'),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March', kids, 'Guests: Marta Ruiz, [Leo Ruiz], [Alba Ruiz]'),
  mail('04 Mar', 'Dr. Ana Soler', 'consulta@pediatria.example', 'Vaccination reminder', kids, '[Alba] is due for her booster'),
  mail('25 Feb', 'Colegio San José', 'secretaria@colegio-sanjose.example', 'Parents evening', kids, 'A slot to talk about [Leo] and [Alba]'),
  ...otherMail,
])

const car = ['my car']
const carRows = newestFirst([
  mail('12 Mar', 'Mapfre', 'polizas@mapfre.es', 'Your car insurance renews on 1 April', car),
  mail('05 Mar', 'ITV Madrid', 'citas@itv.example', 'Inspection booked for 1234 KLM', car),
  mail('20 Feb', 'DGT', 'notificaciones@dgt.es', 'Traffic fine notice', car),
  mail('14 Feb', 'Mapfre', 'polizas@mapfre.es', 'Policy documents for 1234 KLM'),
  ...otherMail,
])

const signInBlocks = (): ConditionBlock[] => [
  [{ id: 'type', kind: 'type', label: 'is a', chips: ['password reset', 'login link', 'one-time code', 'security alert'] }],
]

/** A slice of the inbox for new rules. Nothing matches until a condition is added. */
const inboxRows: PreviewMail[] = newestFirst([
  ...otherMail,
  mail('13 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881', ['order', 'receipt']),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March', ['hotel', 'reservation']),
  mail('04 Mar', 'Zalando', 'info@zalando.es', 'Your order is on its way', ['order', 'delivery']),
])

function ruleFields(id: string) {
  const rule = rules.find((r) => r.id === id)
  if (!rule) throw new Error(`Unknown rule ${id}`)
  const { name, matches, effect, removes, appliesTo } = rule
  return { name, count: matches, newCount: 0, effect, removes: removes ?? '', appliesTo, butNot: [], unsaved: [] }
}

const ruleSeeds: Record<string, () => RuleSeed> = {
  'sign-in-and-security': () => ({
    ...ruleFields('sign-in-and-security'),
    description: 'Mail that could let someone into my accounts',
    blocks: signInBlocks(),
    rows: signInRows,
  }),
  private: () => ({
    ...ruleFields('private'),
    description: 'My doctor, my bank and family',
    blocks: [
      [{ id: 'from', kind: 'sender', label: 'is from', chips: ['e.vidal@clinic.example', 'bbva.es'] }],
      [{ id: 'label', kind: 'label', label: 'has label', chips: ['Family'] }],
    ],
    rows: privateRows,
  }),
  'home-address': () => ({
    ...ruleFields('home-address'),
    description: 'My home address, wherever it appears',
    blocks: [
      [{ id: 'about', kind: 'about', label: 'mentions', chips: ['my home address'] }],
      [{ id: 'text', kind: 'text', label: 'has the words', chips: ['Calle Falsa 123', '28000'] }],
    ],
    rows: homeAddressRows,
  }),
  children: () => ({
    ...ruleFields('children'),
    description: "My children's names",
    blocks: [
      [{ id: 'about', kind: 'about', label: 'mentions', chips: ['my children'] }],
      [{ id: 'text', kind: 'text', label: 'has the words', chips: ['Leo', 'Alba'] }],
    ],
    rows: childrenRows,
  }),
  car: () => ({
    ...ruleFields('car'),
    description: 'Anything about my car',
    blocks: [
      [{ id: 'about', kind: 'about', label: 'is about', chips: ['my car'] }],
      [{ id: 'from', kind: 'sender', label: 'is from', chips: ['mapfre.es', 'dgt.es'] }],
    ],
    rows: carRows,
  }),
}

/** Where a new account reviews the suggested rule before adding it. */
export const suggestedRulePath = `/rules/new?suggested=${suggestedRule.id}`

/** The suggested rule as a new, unsaved rule that holds back the email from every connection. */
function suggestedSeed(): RuleSeed {
  const count = suggestedRule.covers.reduce((sum, item) => sum + item.count, 0)
  return {
    name: suggestedRule.name,
    description: 'Mail that could let someone into my accounts',
    effect: 'hold',
    removes: '',
    appliesTo: everyConnection,
    count,
    newCount: count,
    blocks: signInBlocks(),
    butNot: [],
    unsaved: [],
    rows: signInRows,
  }
}

function newRuleSeed(): RuleSeed {
  return {
    name: 'New rule',
    description: '',
    effect: 'hold',
    removes: '',
    appliesTo: everyConnection,
    count: 0,
    newCount: 0,
    blocks: [
      [{ id: 'about', kind: 'about', label: 'is about', chips: [] }],
      [{ id: 'from', kind: 'sender', label: 'is from', chips: [] }],
    ],
    butNot: [],
    unsaved: [],
    rows: inboxRows,
  }
}

function sameSender(address: string, entry: string): boolean {
  const a = address.toLowerCase()
  const e = entry.toLowerCase()
  return a === e || a.split('@')[1] === e
}

/** Adds a sender to the rule's first sender group as an unsaved change, such as from a receipt. */
function withSender(seed: RuleSeed, sender: string): RuleSeed {
  const value = sender.trim().toLowerCase()
  const bi = seed.blocks.findIndex((block) => block.some((g) => g.kind === 'sender'))
  if (!value || bi < 0) return seed
  const group = seed.blocks[bi]!.find((g) => g.kind === 'sender')!
  if (group.chips.some((c) => c.toLowerCase() === value)) return seed
  const hits = seed.rows.filter((r) => sameSender(r.address, value)).length
  const blocks = seed.blocks.map((block, b) =>
    b !== bi ? block : block.map((g) => (g.id === group.id ? { ...g, chips: [value, ...g.chips] } : g)),
  )
  return { ...seed, blocks, unsaved: [...seed.unsaved, value], count: seed.count + hits, newCount: seed.newCount + hits }
}

export function ruleSeed(id: string, params: { suggested: string | null; add: string | null }): RuleSeed | undefined {
  const seed = id === 'new' ? (params.suggested === suggestedRule.id ? suggestedSeed() : newRuleSeed()) : ruleSeeds[id]?.()
  return seed && params.add ? withSender(seed, params.add) : seed
}
