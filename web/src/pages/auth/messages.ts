import type { ApiErrorCode } from '../../lib/api.ts'
import type { ProviderReason } from '../../lib/arrival.ts'
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
  account_closing: 'This account is being deleted. Cancel the deletion to change anything.',
  provider_off: "That way of signing in isn't available right now. Sign in another way.",
  provider_failed: "That sign-in didn't work. Try again, or sign in another way.",
  provider_unavailable:
    "That way of signing in isn't working right now. Try again later, or sign in another way.",
  provider_in_use: 'That account belongs with another Permaily account.',
  provider_linked: 'This account has an account of that provider linked already. Unlink it first.',
  invitation_needed:
    'There is no Permaily account for your address yet. An invitation is needed to make one.',
  last_way_in: 'That is the only way left to sign in to this account. Add another first.',
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

/**
 * What the sign-in screens say when a sign-in with a provider came back without signing anyone in. Each says
 * what its reason says and no more: `provider_failed` covers a provider that said no, an answer that came too
 * late or to another browser, and an account that takes no providers, and the server does not say which.
 * The two about invitations are said on the sign-up screen, where the invitation is typed.
 */
export const providerProblems: Record<ProviderReason, { title: string; text: string }> = {
  provider_failed: {
    title: "That sign-in didn't work",
    text: 'Nobody was signed in. Try again, or sign in another way.',
  },
  provider_unavailable: {
    title: "That sign-in isn't working right now",
    text: 'Nobody was signed in. Try again later, or sign in another way.',
  },
  invitation_needed: {
    title: 'An invitation is needed',
    text: 'There is no Permaily account for your address yet, and making one takes an invitation. Enter yours, then continue the same way.',
  },
  invalid_invitation: {
    title: 'Create your account',
    text: "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
  },
  provider_in_use: {
    title: "That sign-in didn't work",
    text: 'That account belongs with another Permaily account. Nobody was signed in.',
  },
  provider_linked: {
    title: "That sign-in didn't work",
    text: 'Your Permaily account has another account of that provider linked already. Sign in with that one, or sign in another way and unlink it in Settings.',
  },
  account_closing: {
    title: 'That account is being deleted',
    text: 'Nothing was linked to it, and nobody was signed in. Sign in another way to cancel the deletion.',
  },
}

/** The reasons that are about an invitation: the sign-up screen says them, next to where one is typed. */
export function needsInvitation(reason: ProviderReason): boolean {
  return reason === 'invitation_needed' || reason === 'invalid_invitation'
}

/**
 * What Settings says when linking a provider came back with nothing linked. A reason that linking cannot
 * end with reads as the one that says least. An account that is being deleted is said by its own screen.
 */
export function linkProblem(reason: ProviderReason): string {
  switch (reason) {
    case 'provider_unavailable':
      return "Nothing was linked: that isn't working right now. Try again later."
    case 'provider_in_use':
      return 'Nothing was linked: that account belongs with another Permaily account.'
    case 'provider_linked':
      return 'Nothing was linked: this account has an account of that provider linked already. Unlink it first.'
    default:
      return "Nothing was linked: that didn't work. Try again."
  }
}
