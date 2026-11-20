import type * as api from '../../lib/api.ts'

/*
  What the Activity screen says of the account's record (ADR 0041, "The activity screen"). Every sentence here is a
  first draft for the designer's review. A verdict is words, never colour alone, and a break is a fact, not an error to
  try again. Senders, subjects, topics and sentences are what mail and vendors wrote: the screen puts them as text, in
  `bdi`, never here.
*/

const number = new Intl.NumberFormat('en-GB')

/** A count or a link's number as people read it: "1,234". */
export function count(n: number): string {
  return number.format(n)
}

/** Where a receipt is on the chain, as the screen and permaily-verify write it: "1,234.3". */
export function positionWords(n: number, index: number): string {
  return `${count(n)}.${index}`
}

/** What was found of a receipt that is not kept as recorded. */
export function receiptVerdictWords(verdict: api.ReceiptVerdict, n: number, index: number): string {
  const at = positionWords(n, index)
  switch (verdict) {
    case 'missing':
      return `Receipt ${at} was deleted from the storage.`
    case 'changed':
      return `Receipt ${at} was changed in the storage: it isn't what was recorded.`
    case 'unreadable':
      return `Receipt ${at} can't be read: something else is stored in its place.`
    default:
      return `Receipt ${at} is kept as it was recorded.`
  }
}

/** The chip of a receipt's verdict. */
export function receiptVerdictChip(verdict: api.ReceiptVerdict): string {
  return { ok: 'Kept', missing: 'Deleted', changed: 'Changed', unreadable: "Can't be read" }[
    verdict
  ]
}

/** Whether a link reads as kept: `ok`, and `expired`, whose content the link after it still checks. */
export function linkKept(verdict: api.LinkVerdict | undefined): boolean {
  return verdict === 'ok' || verdict === 'expired'
}

/** What was found of a link that is not kept as written. */
export function linkVerdictWords(verdict: api.LinkVerdict | undefined, n: number): string {
  const at = count(n)
  switch (verdict) {
    case 'lost':
      return `Link ${at} is missing from the storage: the receipts it named can't be listed.`
    case 'forked':
      return `Link ${at} was written twice: the storage holds two versions of it.`
    case 'unreadable':
      return `Link ${at} can't be read: something else is stored in its place.`
    case 'prev':
      return `Link ${at} doesn't match the link after it.`
    case 'ok':
    case 'expired':
      return `Link ${at} is kept as it was written.`
    default:
      return `Link ${at} couldn't be checked.`
  }
}

/** The chip of a link's verdict. */
export function linkVerdictChip(verdict: api.LinkVerdict | undefined): string {
  switch (verdict) {
    case 'lost':
      return 'Missing'
    case 'forked':
      return 'Written twice'
    case 'unreadable':
      return "Can't be read"
    case 'prev':
      return "Doesn't match"
    case 'ok':
    case 'expired':
      return 'Kept'
    default:
      return 'Not checked'
  }
}

/** Under a link whose `break` says the link before it could not be read when it was written. */
export function breakWords(n: number): string {
  return `Link ${count(n)}: the link before this one couldn't be read when this one was written.`
}

/** A link's kind, when it is not one written as its receipts were. */
export function linkKindChip(kind: api.ReceiptsLink['kind']): string | null {
  return kind === 'recovered' ? 'Linked late' : kind === 'adopted' ? 'From before' : null
}

export const linkKindTips = {
  recovered: "This receipt was found without a link by Permaily's daily check, and linked then.",
  adopted:
    "This receipt was there when your record was first chained. Nothing can show it wasn't changed before that.",
  expired: "This link's lock ended. What it says is still checked by the link after it.",
} as const

/** What a consent receipt records: the decision, which Permaily records before it makes the change (choice 17). */
export function consentWords(consent: api.ReceiptConsent): string {
  if (!consent.allowed) return 'Refused a request'
  if (consent.renewal) return 'Approved a renewal request'
  if (consent.change === 'add_device') return 'Approved adding a device'
  return 'Approved a connection request'
}

export const consentTip =
  'This records what you decided. Permaily records it before it makes the change, so a change that then failed still shows here. Connections shows what is in force.'

/** Whether an ended receipt is about one device rather than the whole connection. */
export function endedDevice(receipt: api.Receipt): boolean {
  return (
    Boolean(receipt.device) && !receipt.ended?.replaced_by && receipt.ended?.reason !== 'replaced'
  )
}

const refusedReasons: Record<string, string> = {
  held: 'A rule held the message',
  outside_scope: "It isn't mail this connection reads",
  part_not_given: "An attachment, which this connection doesn't get",
  invalid_formula: "A search Permaily couldn't read",
  formula_part_not_approved: "A search on a part this connection doesn't get",
  model_unavailable: "Permaily couldn't ask its model",
  attachment_too_large: 'The attachment is too large',
  invalid_body: "A request Permaily couldn't read",
  invalid_limit: "A page size Permaily doesn't take",
  invalid_cursor: "A page that doesn't exist",
  rate_limited: 'Too many requests',
  unavailable: "Permaily couldn't read what it needed in time",
  connection_ended: 'The connection had ended',
  too_late: 'Prepared, then not given in time',
}

/** Why a request got no mail. A reason this app does not know is said as the server wrote it, by the caller. */
export function refusedWords(reason: string): string | null {
  return refusedReasons[reason] ?? null
}

const operations: Record<string, string> = {
  search: 'Searches',
  read: 'Reads',
  attachment: 'Attachments',
}

/** One count of a summary of refusals: "Reads: Too many requests". */
export function refusalCountWords(operation: string, reason: string): string {
  return `${operations[operation] ?? operation}: ${refusedWords(reason) ?? reason}`
}

/** How a rule decided to hold a message. */
export function heldWhyWords(why: string): string {
  switch (why) {
    case 'exact':
      return 'By exact words'
    case 'decided':
      return 'By the model'
    case 'recovered':
      return 'Held because its decision was lost'
    case 'undecided':
      return "Held because it couldn't be decided"
    default:
      return 'Held'
  }
}

const partWords: Record<api.ConnectionPart, string> = {
  sender: 'Sender and date',
  subject: 'Subject',
  body: 'Body text',
  attachments: 'Attachments',
}

/** The parts given of one message: "Sender and date, Subject, Attachment 1". */
export function givenWords(parts: readonly api.ConnectionPart[], attachment?: number): string {
  return parts
    .map((p) =>
      p === 'attachments' && attachment !== undefined ? `Attachment ${attachment}` : partWords[p],
    )
    .filter(Boolean)
    .join(', ')
}

/** A sender as a row shows it: the name, or the address. */
export function senderWords(from: readonly api.MailAddress[]): string {
  const first = from[0]
  return first ? first.name || first.address : ''
}

/** Every sender as the panel shows it: "Billing <billing@example.com>". */
export function sendersWords(from: readonly api.MailAddress[]): string {
  return from.map((a) => (a.name ? `${a.name} <${a.address}>` : a.address)).join('; ')
}

/** A model's answer's condition: "subject is about invoices". The topic is the vendor's or the person's words. */
export function conditionWords(model: api.ReceiptModel): { lead: string; topic: string } {
  const subject = model.var === 'message' ? 'the message' : model.var
  return {
    lead: model.op === 'about' ? `${subject} is about` : `${subject} ${model.op}`,
    topic: model.topic,
  }
}

/** A probability as the panel writes it: two decimals. */
export function probabilityWords(p: number): string {
  return p.toFixed(2)
}

/** The first and last characters of a digest, for the eye; the whole of it is copied. */
export function shortDigest(sha256: string): string {
  return sha256.length > 12 ? `${sha256.slice(0, 4)}…${sha256.slice(-4)}` : sha256
}

const day = new Intl.DateTimeFormat('en-GB', {
  weekday: 'long',
  day: 'numeric',
  month: 'long',
  year: 'numeric',
})
const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' })
const moment = new Intl.DateTimeFormat('en-GB', { dateStyle: 'long', timeStyle: 'short' })

function valid(iso: string | undefined): Date | null {
  if (!iso) return null
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? null : date
}

/** The day a row is grouped under, in the person's time zone, or null when it has no time. */
export function dayWords(iso: string | undefined): string | null {
  const date = valid(iso)
  return date ? day.format(date) : null
}

/** A row's time of day. */
export function clockWords(iso: string | undefined): string {
  const date = valid(iso)
  return date ? clock.format(date) : ''
}

/** A moment in full: "16 November 2026 at 10:00". */
export function momentWords(iso: string | undefined): string {
  const date = valid(iso)
  return date ? moment.format(date) : ''
}
