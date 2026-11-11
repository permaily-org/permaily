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
  not_found: "That isn't there any more.",
  invalid_passkey: "That passkey couldn't be used. Try again.",
  invalid_name: 'A name is one line of 1 to 64 characters.',
  too_many_passkeys: 'This account has as many passkeys as it can have. Remove one first.',
  last_passkey:
    'That is the only passkey of an account that signs in with passkeys only. Add another, or turn passkey only off first.',
  passkey_needed: 'Add a passkey before turning on passkey only.',
  passkey_only_off: 'There are recovery codes only while passkey only is on.',
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

/**
 * What to say when a recovery code did not sign anyone in. It is the one answer to every refusal: it must not
 * tell whether the address has an account, whether that account takes recovery codes, or which part was wrong.
 */
export const recoveryRefused =
  "That address and recovery code didn't sign you in. Check both: each code works once. After 5 wrong tries, wait an hour."
