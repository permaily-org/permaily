import type { ApiErrorCode } from '../../lib/api.ts'
import type { LinkProblem } from '../../lib/session.tsx'

/** What to tell a person for each way the API can fail, in words they can act on. */
const messages: Record<ApiErrorCode, string> = {
  invalid_address: "Permaily can't use that address. Check it for typos.",
  invalid_invitation:
    "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
  invalid_code: "That code isn't right, or it has expired. Check it, or send another message.",
  too_many_requests: 'Too many attempts. Wait a few minutes, then try again.',
  network: "Permaily can't be reached. Check your connection, then try again.",
  unauthorized: 'Something went wrong on our side. Try again in a moment.',
  other: 'Something went wrong on our side. Try again in a moment.',
}

export function messageFor(code: ApiErrorCode): string {
  return messages[code]
}

/** The headline when the link of a message did not sign anyone in. */
export const linkHeadlines: Record<LinkProblem, string> = {
  expired: 'That link no longer works',
  failed: "That link couldn't sign you in",
}
