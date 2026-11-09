/*
  The full query each connection was approved for: what it matches, the fields that come back for
  each email, and the rest of its access. The connection's page shows it in plain words and as the
  exact file, in the same YAML the docs use.
*/
import {
  type Connection,
  connectionLabel,
  mailboxes,
  type QueryId,
  type Receipt,
  vendorOf,
} from './sample.ts'

/** A list of words means one of them. A `?` in the file marks a field that can be empty. */
export type FieldType = 'string' | 'number' | 'boolean' | 'date' | 'datetime' | readonly string[]

/** A field with `fields` is a group of others, such as the trip on a booking. */
export type Field = {
  name: string
  label: string
  type?: FieldType
  optional?: boolean
  fields?: Field[]
}

/**
 * What a field held in one email. A value a remove rule matches, such as the home address, is taken
 * out whenever that rule applies to the connection.
 */
export type ExampleValue = string | boolean | null | { value: string; removableBy: string }

export type QueryDefinition = {
  /** The match in plain words, for people who don't read searches. */
  matchText: string
  /** The exact search, one line per part. Empty for an agent's own searches. */
  search: string[]
  /** For an agent's own searches: the kinds of mail they stay inside. */
  within?: QueryId[]
  fields: Field[]
  /** The values its fields held in its latest email, keyed by path such as "trip.to". */
  example?: { receiptId: string; values: Record<string, ExampleValue> }
  parts: string[]
  actions: string[]
  /** A date mail is read from, or "new_only" or "all". */
  history: string
  expires: string
}

const yesNo = (name: string, label: string): Field => ({ name, label, type: 'boolean' })

const definitions: Record<string, QueryDefinition> = {
  'claude/chat': {
    matchText: 'Its own searches, only inside Flights and hotels and Invoices',
    search: [],
    within: ['flights-and-hotels', 'invoices'],
    fields: [],
    parts: ['subject', 'body_text'],
    actions: ['read'],
    history: '2026-01-01',
    expires: '2026-06-04',
  },
  'claude/morning-summary': {
    matchText: 'New mail in your Primary inbox from the last day',
    search: ['in:inbox category:primary newer_than:1d'],
    fields: [
      yesNo('needs_reply', 'Does it need a reply?'),
      yesNo('is_order', 'Is it about an order?'),
      {
        name: 'delivery_address',
        label: 'Delivery address on an order',
        type: 'string',
        optional: true,
      },
      { name: 'act_by', label: 'A date it asks you to act by', type: 'date', optional: true },
    ],
    example: {
      receiptId: 'r_7a0e51',
      values: {
        needs_reply: false,
        is_order: true,
        delivery_address: {
          value: 'Calle Falsa 123, 4B, 28000 Madrid',
          removableBy: 'home-address',
        },
        act_by: null,
      },
    },
    parts: ['subject', 'body_text'],
    actions: ['read'],
    history: 'new_only',
    expires: '2026-06-02',
  },
  'claude/finances': {
    matchText:
      'Mail from Holded, Vodafone, Iberdrola, Amazon, Santander and 5 more, or with invoice, factura, receipt, recibo or extracto in the subject',
    search: [
      'from:(holded.com OR vodafone.es OR iberdrola.es OR endesa.es OR amazon.es OR apple.com',
      'OR paypal.com OR santander.es OR ing.es OR revolut.com)',
      'OR subject:(invoice OR factura OR receipt OR recibo OR extracto)',
    ],
    fields: [
      { name: 'kind', label: 'What it is', type: ['invoice', 'receipt', 'statement'] },
      { name: 'issuer', label: "Who it's from", type: 'string' },
      { name: 'number', label: 'Invoice or order number', type: 'string', optional: true },
      { name: 'issued_on', label: 'Date on it', type: 'date' },
      {
        name: 'amount',
        label: 'Amount',
        fields: [
          { name: 'total', label: 'Total', type: 'number' },
          { name: 'tax', label: 'Tax included', type: 'number', optional: true },
          { name: 'currency', label: 'Currency', type: 'string' },
        ],
      },
      { name: 'billing_address', label: 'Address on it', type: 'string', optional: true },
      yesNo('is_paid', 'Has it been paid?'),
      yesNo('is_refund', 'Is it a refund?'),
    ],
    example: {
      receiptId: 'r_7d02b7',
      values: {
        kind: 'invoice',
        issuer: 'Iberdrola',
        number: 'FE26-0311-4471',
        issued_on: '11 Mar 2026',
        'amount.total': '61.20',
        'amount.tax': '10.62',
        'amount.currency': 'EUR',
        billing_address: {
          value: 'Calle Falsa 123, 4B, 28000 Madrid',
          removableBy: 'home-address',
        },
        is_paid: false,
        is_refund: false,
      },
    },
    parts: ['subject', 'body_text'],
    actions: ['read'],
    history: 'new_only',
    expires: '2026-06-04',
  },
  'trip-app/bookings': {
    matchText:
      'Mail from Iberia, Booking.com, Renfe, Airbnb and 8 more travel companies, with booking, reservation or itinerary in the subject',
    search: [
      'from:(iberia.com OR booking.com OR renfe.es OR airbnb.com OR vueling.com OR ryanair.com',
      'OR nh-hotels.com OR hertz.com OR alsa.es OR easyjet.com OR expedia.com OR trainline.com)',
      'subject:(booking OR reservation OR itinerary)',
    ],
    fields: [
      { name: 'kind', label: 'What was booked', type: ['flight', 'hotel', 'train', 'car'] },
      { name: 'reference', label: 'Booking reference', type: 'string' },
      {
        name: 'trip',
        label: 'Where and when',
        fields: [
          { name: 'from', label: 'From', type: 'string', optional: true },
          { name: 'to', label: 'To', type: 'string' },
          { name: 'starts_at', label: 'Starts', type: 'datetime' },
          { name: 'ends_at', label: 'Ends', type: 'datetime', optional: true },
        ],
      },
      { name: 'travelers', label: 'How many people', type: 'number' },
      yesNo('is_cancellation', 'Is it a cancellation?'),
      {
        name: 'cancellation_notes',
        label: 'What the cancellation says',
        type: 'string',
        optional: true,
      },
      yesNo('refunded', 'Was it refunded?'),
    ],
    example: {
      receiptId: 'r_7d10a9',
      values: {
        kind: 'flight',
        reference: 'KX7Q2M',
        'trip.from': 'Madrid',
        'trip.to': 'Lisbon',
        'trip.starts_at': '14 Mar 2026, 09:40',
        'trip.ends_at': null,
        travelers: '3',
        is_cancellation: false,
        cancellation_notes: null,
        refunded: false,
      },
    },
    parts: ['subject', 'body_text'],
    actions: ['read'],
    history: '2026-01-01',
    expires: '2026-05-18',
  },
  'ledgerly/bookkeeping': {
    matchText:
      'Mail from Holded, Vodafone, Iberdrola and 5 more, or with invoice, factura, receipt or recibo in the subject',
    search: [
      'from:(holded.com OR vodafone.es OR iberdrola.es OR endesa.es OR movistar.es OR adobe.com',
      'OR figma.com OR amazon.es)',
      'OR subject:(invoice OR factura OR receipt OR recibo)',
    ],
    fields: [
      { name: 'kind', label: 'What it is', type: ['invoice', 'receipt'] },
      { name: 'supplier', label: "Who it's from", type: 'string' },
      { name: 'number', label: 'Invoice or order number', type: 'string', optional: true },
      { name: 'date', label: 'Date on it', type: 'date' },
      { name: 'total', label: 'Total', type: 'number' },
      { name: 'tax', label: 'Tax', type: 'number', optional: true },
      { name: 'currency', label: 'Currency', type: 'string' },
      { name: 'category', label: 'Spending category', type: 'string', optional: true },
      { name: 'billing_address', label: 'Address on it', type: 'string', optional: true },
      yesNo('is_paid', 'Has it been paid?'),
    ],
    example: {
      receiptId: 'r_7b9e04',
      values: {
        kind: 'receipt',
        supplier: 'Amazon',
        number: '402-8810417',
        date: '13 Mar 2026',
        total: '34.99',
        tax: '6.07',
        currency: 'EUR',
        category: 'Home',
        billing_address: {
          value: 'Calle Falsa 123, 4B, 28000 Madrid',
          removableBy: 'home-address',
        },
        is_paid: true,
      },
    },
    parts: ['subject', 'body_text'],
    actions: ['read'],
    history: '2026-01-01',
    expires: '2026-09-02',
  },
  'inbox-tidy/cleanup': {
    matchText: 'All your mail, except spam and trash',
    search: ['in:anywhere -in:spam -in:trash'],
    fields: [
      {
        name: 'category',
        label: 'What kind of mail',
        type: ['newsletter', 'promotion', 'update', 'personal'],
      },
      yesNo('is_list', 'Was it sent to a mailing list?'),
      yesNo('has_unsubscribe', 'Does it have an unsubscribe link?'),
    ],
    parts: ['headers'],
    actions: ['label', 'archive'],
    history: 'all',
    expires: '2026-04-30',
  },
}

export function queryDefinition(connectionId: string): QueryDefinition | undefined {
  return definitions[connectionId]
}

/** One row of the readable view: a field, how deep it sits, and its path for the example. */
export type FieldRow = { field: Field; depth: number; path: string }

export function fieldRows(fields: Field[], depth = 0, prefix = ''): FieldRow[] {
  return fields.flatMap((field) => {
    const path = prefix ? `${prefix}.${field.name}` : field.name
    return [
      { field, depth, path },
      ...(field.fields ? fieldRows(field.fields, depth + 1, path) : []),
    ]
  })
}

const typeWords: Record<Exclude<FieldType, readonly string[]>, string> = {
  string: 'Text',
  number: 'Number',
  boolean: 'Yes or no',
  date: 'Date',
  datetime: 'Date and time',
}

/** "Yes or no", "Text, can be empty" or "Flight, hotel, train or car". */
export function typeText(field: Field): string {
  if (field.fields) return `Group of ${field.fields.length}`
  const type = field.type ?? 'string'
  const words =
    typeof type === 'string'
      ? typeWords[type]
      : `${type[0]!.charAt(0).toUpperCase()}${type.slice(0, -1).join(', ').slice(1)} or ${type.at(-1)}`
  return field.optional ? `${words}, can be empty` : words
}

function fileType(field: Field): string {
  const type = field.type ?? 'string'
  return (typeof type === 'string' ? type : type.join(' | ')) + (field.optional ? '?' : '')
}

function fieldLines(fields: Field[], indent: string): string[] {
  return fields.flatMap((f) =>
    f.fields
      ? [`${indent}${f.name}:`, ...fieldLines(f.fields, `${indent}  `)]
      : [`${indent}${f.name}: ${fileType(f)}`],
  )
}

const snake = (id: string) => id.replace(/-/g, '_')

/** The connection's query and access, exactly as approved, in the YAML the docs use. */
export function queryFile(connection: Connection): string {
  const def = definitions[connection.id]
  if (!def) return ''
  const vendor = vendorOf(connection)
  const accounts = mailboxes.filter((m) => m.usedBy.includes(connection.id)).map((m) => m.address)
  const lines = [
    `# ${connectionLabel(connection)}`,
    `vendor: ${vendor.id}`,
    `connection: ${connection.id}`,
    accounts.length === 1 ? `account: ${accounts[0]}` : `accounts: [${accounts.join(', ')}]`,
  ]
  if (def.within)
    lines.push('query:', '  own_searches: true', `  within: [${def.within.map(snake).join(', ')}]`)
  else lines.push('query: >', ...def.search.map((line) => `  ${line}`))
  if (def.fields.length > 0) lines.push('fields:', ...fieldLines(def.fields, '  '))
  lines.push(`parts: [${def.parts.join(', ')}]`, `actions: [${def.actions.join(', ')}]`)
  const { trigger } = connection
  if (trigger.kind === 'schedule') lines.push('runs:', '  every: day', `  at: "${trigger.at}"`)
  else lines.push(`runs: ${trigger.kind === 'new-mail' ? 'on_new_mail' : 'when_asked'}`)
  if (connection.webhook) lines.push(`webhook: ${connection.webhook.url}`)
  lines.push(
    /^\d/.test(def.history) ? `history_from: ${def.history}` : `history: ${def.history}`,
    `expires: ${def.expires}`,
  )
  return lines.join('\n')
}

/** The receipt an example came from, so the table can name the email. */
export function exampleReceipt(def: QueryDefinition, receipts: Receipt[]): Receipt | undefined {
  return def.example && receipts.find((r) => r.id === def.example?.receiptId)
}
