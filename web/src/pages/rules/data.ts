/*
  Page-only content for Rules and the rule editor.
  Shared numbers come from src/data/sample.ts. Company senders are illustrative.
*/
import {
  type AppliesTo,
  everyConnection,
  type RuleEffect,
  rules,
  suggestedRule,
} from '../../data/sample.ts'

export { joinNames } from '../../data/sample.ts'

/* Rule editor */

/** The part of an email a condition looks at. */
export type Field = 'message' | 'subject' | 'sender' | 'kind' | 'label'

/** How a condition compares that part with its values. A negative one matches mail that has none of them. */
export type Operator =
  | 'about'
  | 'notAbout'
  | 'mentions'
  | 'notMentions'
  | 'contains'
  | 'notContains'
  | 'is'
  | 'isNot'

/** One condition: a field, how it's compared, and the values. A positive condition matches mail that has any of them. */
export type Criterion = { id: string; field: Field; operator: Operator; values: string[] }

/** Criteria: a set of conditions people can name. Mail meets it when it meets every condition, and a rule when it meets any of its criteria. */
export type CriteriaGroup = { id: string; name: string; criteria: Criterion[] }

export const operators: Record<Operator, { label: string; negative: boolean }> = {
  about: { label: 'is about', negative: false },
  notAbout: { label: "isn't about", negative: true },
  mentions: { label: 'mentions', negative: false },
  notMentions: { label: "doesn't mention", negative: true },
  contains: { label: 'contains', negative: false },
  notContains: { label: "doesn't contain", negative: true },
  is: { label: 'is', negative: false },
  isNot: { label: "isn't", negative: true },
}

/** Every field, in the order the editor lists them, with the operators it takes. */
export const fields: { field: Field; label: string; operators: Operator[] }[] = [
  {
    field: 'message',
    label: 'Message',
    operators: ['about', 'notAbout', 'mentions', 'notMentions', 'contains', 'notContains'],
  },
  { field: 'subject', label: 'Subject', operators: ['contains', 'notContains'] },
  { field: 'sender', label: 'Sender', operators: ['is', 'isNot'] },
  { field: 'kind', label: 'Kind', operators: ['is', 'isNot'] },
  { field: 'label', label: 'Label', operators: ['is', 'isNot'] },
]

export function fieldLabel(field: Field): string {
  return fields.find((f) => f.field === field)?.label ?? field
}

/** The conditions people usually start from, with a value each could take. Any of them can be turned negative after. */
export const starters: { field: Field; operator: Operator; example: string }[] = [
  { field: 'message', operator: 'about', example: 'my car' },
  { field: 'message', operator: 'mentions', example: 'my home address' },
  { field: 'message', operator: 'contains', example: 'Calle Falsa 123' },
  { field: 'subject', operator: 'contains', example: 'invoice' },
  { field: 'sender', operator: 'is', example: 'mapfre.es' },
  { field: 'kind', operator: 'is', example: 'password reset' },
  { field: 'label', operator: 'is', example: 'Family' },
]

/** Kinds of mail "Kind is" suggests as people type. */
export const mailTypes = [
  'password reset',
  'login link',
  'one-time code',
  'security alert',
  'invoice',
  'receipt',
  'bank statement',
  'bill',
  'order',
  'delivery',
  'reservation',
  'newsletter',
]

/**
 * What a condition's values are like. Topics and things described in words are checked by a model inside the verified
 * service. Exact strings, such as addresses and words, are set in monospace. Topics aren't.
 */
export function valueStyle({ field, operator }: Pick<Criterion, 'field' | 'operator'>): {
  placeholder: string
  literal: boolean
  model: boolean
  suggestions?: string[]
} {
  switch (field) {
    case 'message':
      if (operator === 'about' || operator === 'notAbout')
        return { placeholder: 'a topic, in your words', literal: false, model: true }
      if (operator === 'mentions' || operator === 'notMentions')
        return { placeholder: 'something, in your words', literal: false, model: true }
      return { placeholder: 'word or phrase', literal: true, model: true }
    case 'subject':
      return { placeholder: 'word or phrase', literal: true, model: false }
    case 'sender':
      return { placeholder: 'domain or address', literal: true, model: false }
    case 'kind':
      return { placeholder: 'kind of mail', literal: true, model: false, suggestions: mailTypes }
    case 'label':
      return { placeholder: 'Gmail label', literal: true, model: false }
  }
}

/** A field changed under a condition keeps its operator when it can, or the first one of the same sign. */
export function withField(criterion: Criterion, field: Field): Criterion {
  const allowed = fields.find((f) => f.field === field)?.operators ?? []
  if (allowed.includes(criterion.operator)) return { ...criterion, field }
  const negative = operators[criterion.operator].negative
  return {
    ...criterion,
    field,
    operator:
      allowed.find((o) => operators[o].negative === negative) ?? allowed[0] ?? criterion.operator,
  }
}

/* What a partial rule takes out */

/** A part of an email a partial rule can take out. Attachments and data take a list of what to look for. */
export type Part = 'subject' | 'body' | 'attachments' | 'data' | 'thread' | 'sender' | 'cc'

export const parts: { part: Part; label: string; tip?: string }[] = [
  { part: 'subject', label: 'Subject' },
  { part: 'body', label: 'Body' },
  { part: 'attachments', label: 'Attachments' },
  { part: 'data', label: 'Data' },
  { part: 'thread', label: 'Thread', tip: 'The earlier messages quoted under a reply.' },
  { part: 'sender', label: 'Sender' },
  { part: 'cc', label: 'CC' },
]

/** What a partial rule takes out: whole parts, the attachments described (all of them when none are), and data described in words. */
export type Removal = { parts: Part[]; attachments: string[]; data: string[] }

export const noRemoval: Removal = { parts: [], attachments: [], data: [] }

/** Data people often take out, suggested as they describe it. */
export const dataSuggestions = [
  'Home address',
  'Personal phone number',
  'Credit card numbers',
  'Bank account numbers',
  'ID numbers',
  'Date of birth',
  "Children's names",
  'Health details',
]

/** Kinds of attachment suggested as people describe which ones to take out. */
export const attachmentSuggestions = ['PDFs', 'Images', 'Invoices', 'ID documents', 'Spreadsheets']

/** A partial rule needs something to take out, and data needs at least one thing described. */
export function removalComplete(removal: Removal): boolean {
  return removal.parts.length > 0 && (!removal.parts.includes('data') || removal.data.length > 0)
}

/**
 * A sample email. keys are what it can match on besides its sender. Square brackets in the snippet
 * mark the detail a remove rule takes out.
 */
export type PreviewMail = {
  id: string
  date: string
  sender: string
  address: string
  subject: string
  snippet?: string
  keys?: string[]
}

export type RuleSeed = {
  name: string
  description: string
  effect: RuleEffect
  /** What a partial rule takes out. */
  removal: Removal
  appliesTo: AppliesTo
  /** Matches with the conditions as loaded. */
  count: number
  /** How many of those the saved rule didn't match yet. */
  newCount: number
  /** Mail that meets every condition of any one group matches. */
  groups: CriteriaGroup[]
  /** Values the saved rule didn't have, so they count as part of this edit. */
  unsaved: string[]
  rows: PreviewMail[]
}

let rowId = 0
function mail(
  date: string,
  sender: string,
  address: string,
  subject: string,
  keys?: string[],
  snippet?: string,
): PreviewMail {
  rowId += 1
  return { id: `m${rowId}`, date, sender, address, subject, keys, snippet }
}

const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
const dayOf = (row: PreviewMail) =>
  months.indexOf(row.date.slice(3)) * 100 + Number(row.date.slice(0, 2))

/** Newest first, so senders added in an edit land among the rest. */
function newestFirst(rows: PreviewMail[]): PreviewMail[] {
  return [...rows].sort((a, b) => dayOf(b) - dayOf(a))
}

/** Mail a rule doesn't match yet. Hidden in its preview until an edit makes it match. */
const otherMail: PreviewMail[] = [
  mail('14 Mar', 'Holded', 'facturas@holded.com', 'Invoice F-2291, February', ['invoice']),
  mail('14 Mar', 'Iberia', 'confirmaciones@iberia.com', 'Your flight to Lisbon is confirmed', [
    'flight',
  ]),
  mail('12 Mar', 'Vodafone', 'facturas@vodafone.es', 'Your March bill is ready', ['bill']),
  mail('11 Mar', 'Spotify', 'news@spotify.com', 'Your plan renews on 1 April', ['renewal']),
  mail('06 Mar', 'Santander', 'avisos@santander.es', 'Extracto de febrero', ['statement']),
  mail('02 Mar', 'Ticketmaster', 'entradas@ticketmaster.es', 'Your tickets for 21 March', [
    'event ticket',
  ]),
  mail('27 Feb', 'Renfe', 'billetes@renfe.es', 'Madrid to Valencia, 6 April', ['train']),
  mail('19 Feb', 'Airbnb', 'automated@airbnb.com', 'Reservation confirmed in Porto', [
    'reservation',
  ]),
]

const signInRows = newestFirst([
  mail('14 Mar', 'Google', 'no-reply@accounts.google.com', 'Your verification code', [
    'one-time code',
  ]),
  mail('13 Mar', 'Revolut', 'no-reply@revolut.com', 'New sign-in from Madrid', ['security alert']),
  mail('12 Mar', 'Booking.com', 'noreply@booking.com', 'Reset your password', ['password reset']),
  mail('11 Mar', 'Iberia', 'no-reply@iberia.com', 'Your login link', ['login link']),
  mail('09 Mar', 'Santander', 'avisos@santander.es', 'New sign-in to your account', [
    'security alert',
  ]),
  mail('07 Mar', 'Amazon', 'account-update@amazon.es', 'Your one-time password', ['one-time code']),
  mail('05 Mar', 'Holded', 'no-reply@holded.com', 'Change your password', ['password reset']),
  mail('02 Mar', 'Apple', 'no_reply@email.apple.com', 'Your Apple ID was used to sign in', [
    'security alert',
  ]),
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
  mail(
    '13 Mar',
    'Amazon',
    'auto-confirm@amazon.es',
    'Your receipt, order 402-881',
    address,
    'Delivering to [Calle Falsa 123, 4B], Madrid',
  ),
  mail(
    '12 Mar',
    'Zalando',
    'info@zalando.es',
    'Your order is on its way',
    address,
    'Arriving at [Calle Falsa 123, 4B] on Friday',
  ),
  mail(
    '10 Mar',
    'Iberdrola',
    'facturas@iberdrola.es',
    'Factura de luz, febrero',
    address,
    'Supply address: [Calle Falsa 123, 4B, 28000 Madrid]',
  ),
  mail(
    '06 Mar',
    'Glovo',
    'pedidos@glovo.com',
    'Recibo de tu pedido',
    address,
    'Delivered to [Calle Falsa 123]',
  ),
  mail(
    '02 Mar',
    'Santander',
    'avisos@santander.es',
    'Your new card is on its way',
    address,
    'Posted to [Calle Falsa 123, 4B, 28000 Madrid]',
  ),
  mail(
    '27 Feb',
    'Correos',
    'avisos@correos.es',
    'Your parcel is waiting',
    address,
    'We left a notice at [Calle Falsa 123]',
  ),
  ...otherMail,
])

const kids = ['my children']
const childrenRows = newestFirst([
  mail(
    '11 Mar',
    'Colegio San José',
    'secretaria@colegio-sanjose.example',
    'Class trip on Friday',
    kids,
    'Please sign the form for [Leo] by Wednesday',
  ),
  mail(
    '09 Mar',
    'Booking.com',
    'noreply@booking.com',
    'Hotel reservation, 14 to 17 March',
    kids,
    'Guests: Marta Ruiz, [Leo Ruiz], [Alba Ruiz]',
  ),
  mail(
    '04 Mar',
    'Dr. Ana Soler',
    'consulta@pediatria.example',
    'Vaccination reminder',
    kids,
    '[Alba] is due for her booster',
  ),
  mail(
    '25 Feb',
    'Colegio San José',
    'secretaria@colegio-sanjose.example',
    'Parents evening',
    kids,
    'A slot to talk about [Leo] and [Alba]',
  ),
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

/** Criteria of one condition, as most saved rules have. */
const group = (
  id: string,
  name: string,
  field: Field,
  operator: Operator,
  values: string[],
): CriteriaGroup => ({
  id,
  name,
  criteria: [{ id: `${id}-1`, field, operator, values }],
})

const signInGroups = (): CriteriaGroup[] => [
  group('kinds', 'Sign-in mail', 'kind', 'is', [
    'password reset',
    'login link',
    'one-time code',
    'security alert',
  ]),
]

/** A slice of the inbox for new rules. Nothing matches until a condition is added. */
const inboxRows: PreviewMail[] = newestFirst([
  ...otherMail,
  mail('13 Mar', 'Amazon', 'auto-confirm@amazon.es', 'Your receipt, order 402-881', [
    'order',
    'receipt',
  ]),
  mail('09 Mar', 'Booking.com', 'noreply@booking.com', 'Hotel reservation, 14 to 17 March', [
    'hotel',
    'reservation',
  ]),
  mail('04 Mar', 'Zalando', 'info@zalando.es', 'Your order is on its way', ['order', 'delivery']),
])

function ruleFields(id: string) {
  const rule = rules.find((r) => r.id === id)
  if (!rule) throw new Error(`Unknown rule ${id}`)
  const { name, matches, effect, appliesTo } = rule
  return { name, count: matches, newCount: 0, effect, removal: noRemoval, appliesTo, unsaved: [] }
}

const ruleSeeds: Record<string, () => RuleSeed> = {
  'sign-in-and-security': () => ({
    ...ruleFields('sign-in-and-security'),
    description: 'Mail that could let someone into my accounts',
    groups: signInGroups(),
    rows: signInRows,
  }),
  private: () => ({
    ...ruleFields('private'),
    description: 'My doctor, my bank and family',
    groups: [
      group('from', 'My doctor and my bank', 'sender', 'is', ['e.vidal@clinic.example', 'bbva.es']),
      group('label', 'Family', 'label', 'is', ['Family']),
    ],
    rows: privateRows,
  }),
  'home-address': () => ({
    ...ruleFields('home-address'),
    description: 'My home address, wherever it appears',
    removal: { parts: ['data'], attachments: [], data: ['Home address'] },
    groups: [
      group('mentions', 'Mentions of my address', 'message', 'mentions', ['my home address']),
      group('words', 'The address itself', 'message', 'contains', ['Calle Falsa 123', '28000']),
    ],
    rows: homeAddressRows,
  }),
  children: () => ({
    ...ruleFields('children'),
    description: "My children's names",
    removal: { parts: ['data'], attachments: [], data: ["Children's names"] },
    groups: [
      group('mentions', 'Mentions of my children', 'message', 'mentions', ['my children']),
      group('words', 'Their names', 'message', 'contains', ['Leo', 'Alba']),
    ],
    rows: childrenRows,
  }),
  car: () => ({
    ...ruleFields('car'),
    description: 'Anything about my car',
    groups: [
      group('about', 'About my car', 'message', 'about', ['my car']),
      group('from', 'My insurer and the DGT', 'sender', 'is', ['mapfre.es', 'dgt.es']),
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
    removal: noRemoval,
    appliesTo: everyConnection,
    count,
    newCount: count,
    groups: signInGroups(),
    unsaved: [],
    rows: signInRows,
  }
}

function newRuleSeed(): RuleSeed {
  return {
    name: 'New rule',
    description: '',
    effect: 'hold',
    removal: noRemoval,
    appliesTo: everyConnection,
    count: 0,
    newCount: 0,
    // A new rule starts with no conditions: people add the ones they need.
    groups: [],
    unsaved: [],
    rows: inboxRows,
  }
}

function sameSender(address: string, entry: string): boolean {
  const a = address.toLowerCase()
  const e = entry.toLowerCase()
  return a === e || a.split('@')[1] === e
}

/**
 * Adds a sender to the rule as an unsaved change, such as from a receipt, so mail from it matches on its own: into a
 * group that's only "Sender is", or as a new group of its own.
 */
function withSender(seed: RuleSeed, sender: string): RuleSeed {
  const value = sender.trim().toLowerCase()
  if (!value) return seed
  const senders = seed.groups.find(
    (g) =>
      g.criteria.length === 1 &&
      g.criteria[0]!.field === 'sender' &&
      g.criteria[0]!.operator === 'is',
  )
  if (senders?.criteria[0]?.values.some((v) => v.toLowerCase() === value)) return seed
  const hits = seed.rows.filter((r) => sameSender(r.address, value)).length
  const groups = senders
    ? seed.groups.map((g) =>
        g === senders
          ? { ...g, criteria: [{ ...g.criteria[0]!, values: [value, ...g.criteria[0]!.values] }] }
          : g,
      )
    : [group('from', 'Senders', 'sender', 'is', [value]), ...seed.groups]
  return {
    ...seed,
    groups,
    unsaved: [...seed.unsaved, value],
    count: seed.count + hits,
    newCount: seed.newCount + hits,
  }
}

export function ruleSeed(
  id: string,
  params: { suggested: string | null; add: string | null },
): RuleSeed | undefined {
  const seed =
    id === 'new'
      ? params.suggested === suggestedRule.id
        ? suggestedSeed()
        : newRuleSeed()
      : ruleSeeds[id]?.()
  return seed && params.add ? withSender(seed, params.add) : seed
}
