/*
  The rules screens' own words (ADR 0040, "The screens"), a first draft for the designer's review (docs/web.md, TODO).
  What a rule matches, does and who it is for is always the server's sentence (`RuleCheck`); these are the fixed words
  around it: each kind of data with what it takes out besides, the parts, and what every confirmation says.
*/
import type { ConnectionPart, RuleCheck, RuleDraft, ShapeKind } from '../../lib/api.ts'

/**
 * Each kind of data a rule takes out with no model, and what it takes out besides (ADR 0040, "False positives, on
 * purpose"): a pattern over-removes rather than let a piece out.
 */
export const shapeWords: Record<ShapeKind, { label: string; also: string }> = {
  email_address: {
    label: 'Email addresses',
    also: 'Anything with an @ and a domain after it, whoever it belongs to.',
  },
  phone_number: {
    label: 'Phone numbers',
    also: 'Every number of 7 digits or more, with whatever numbers are written next to it: dates and order numbers too.',
  },
  card_number: {
    label: 'Card numbers',
    also: 'Every number of 12 digits or more, with the numbers written next to it, whether or not it is a real card.',
  },
  account_number: {
    label: 'Account numbers',
    also: 'Every number of 8 digits or more, with the numbers written next to it, and IBANs.',
  },
  numeric_code: {
    label: 'Codes',
    also: 'Every number of 4 digits or more: years, dates, amounts and numbers written next to it included.',
  },
  link: {
    label: 'Links',
    also: 'Web addresses, and any words joined by a dot that end like a domain, such as “hello.world”.',
  },
}

/** The parts of a message a rule can take out whole. */
export const partWords: Record<ConnectionPart, string> = {
  sender: 'The sender',
  subject: 'The subject',
  body: 'The text',
  attachments: 'Attachments',
}

/** The parts in the order the editor lists them. */
export const removableParts: readonly ConnectionPart[] = [
  'sender',
  'subject',
  'body',
  'attachments',
]

/** What every confirmation of a change says (ADR 0040, choice 12): a change is not a revocation. */
export const inFlight =
  'Applies to every request that begins after you save; an answer already being prepared can still finish.'

/** What a rule that takes out kinds of data or topics takes out besides, since it doesn't read them (choices 6, 9). */
export const senderAndAttachments =
  'The sender and attachments are taken out too, since the rule doesn’t read them.'

/** What taking out by pattern says (ADR 0040, "Fixed shapes"). */
export const patternsOverRemove =
  'Patterns take out more than they name: every number long enough for a kind, with the numbers next to it, and anything that looks like an address or a link.'

/** What taking out by topic says (ADR 0040, "Removing by meaning", and its implementation notes, N2). */
export const topicsOverRemove =
  'A model reads each sentence of the subject and the text with every address, number and link hidden from it; a sentence it says mentions a topic, or that it can’t decide, is taken out. The pieces it could not be shown (addresses, numbers and links) are taken out too, whatever it answers.'

/** What a rule a model decides does while the model can't be asked. */
export const modelDown =
  'While Permaily can’t ask its model, a rule that holds mail back holds every message it might apply to, and one that takes out topics takes out the whole subject and text.'

/** The fixed lines a draft's confirmation says besides the server's sentence: what it takes out besides, and when it applies. */
export function confirmationLines(draft: RuleDraft): string[] {
  const shapes = (draft.removal?.shapes?.length ?? 0) > 0
  const topics = (draft.removal?.topics?.length ?? 0) > 0
  const lines: string[] = []
  if (draft.effect === 'remove' && shapes) lines.push(patternsOverRemove)
  if (draft.effect === 'remove' && topics) lines.push(topicsOverRemove)
  if (draft.effect === 'remove' && (shapes || topics)) lines.push(senderAndAttachments)
  lines.push(inFlight)
  return lines
}

/** Why a change of rules asks the person to show it's them again. */
export const rulesProof = {
  says: `Rules decide what leaves your mailboxes, so Permaily asks you to show it's you again. It counts for the next 10 minutes. ${inFlight}`,
}

/** A rule's name, or what it does when it has none. */
export function ruleName(check: RuleCheck): string {
  return check.draft.name.trim() || check.sentence.text
}

/**
 * What the preview says when the server can't show a message as the removal leaves it: it answers `not_found` until
 * disclosure (ADR 0040, pull request 5) is composed, which is not the email being gone.
 */
export const asLeavingUnavailable =
  "Permaily can't show this email as it would leave yet. The list above still shows the mail the rule applies to."
