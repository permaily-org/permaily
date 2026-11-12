import type { ApiErrorCode, Provider } from '../../lib/api.ts'
import type { ProviderReason } from '../../lib/arrival.ts'
import { providerNames } from '../../lib/providers.ts'
import type { LinkProblem } from '../../lib/session.tsx'

/** Where a person writes to ask about their account. The app sends nothing there: it is a `mailto:` link. */
export const supportAddress = 'support@permaily.com'

/** The headline of a sign-in that proved who it is and ended at an account the operator blocked. */
export const blockedTitle = 'That account is blocked'

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
  account_blocked: `That account is blocked. To ask about it, write to ${supportAddress}.`,
  address_in_use: 'That address belongs to another Permaily account. Use another address.',
  confirmation_needed: "Nothing changed: Permaily needs you to confirm it's you first. Try again.",
  passkey_required: 'This account confirms with a passkey, not with a code.',
  passkey_only_needed: 'The admin panel works only while your account signs in with passkeys only.',
  super_admin_only: 'Only the super admin can do that.',
  not_cancellable: 'The person asked for this deletion, so only they can cancel it.',
  terms_needed:
    "Permaily's terms changed since this page opened. Reload it to read them, then agree to go on.",
  invalid_provider_app:
    "Permaily couldn't use that client ID and secret. Check them, then enter the secret again.",
  too_many_mailboxes: 'This account has as many mailboxes as it can have. Disconnect one first.',
  invalid_label: 'A label is one line of at most 64 characters.',
  invalid_retention: 'Choose 7, 30, 90, 180 or 365 days.',
  invalid_query: 'A search is 1 to 200 characters.',
  too_many_messages: 'Delete at most 100 messages at a time.',
  mailbox_unreadable:
    "This mailbox can't be read. Disconnect it, and write to support@permaily.com.",
  other: 'Something went wrong on our side. Try again in a moment.',
}

export function messageFor(code: ApiErrorCode): string {
  return messages[code]
}

/** The headline when the link of a message did not sign anyone in. */
export const linkHeadlines: Record<LinkProblem, string> = {
  expired: 'That link no longer works',
  failed: "That link couldn't sign you in",
  blocked: blockedTitle,
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
 *
 * `account_blocked` is also what a sign-in by mail, with a passkey or with a recovery code ends with: it is
 * said only after the proof, names nobody, and offers nothing to try again, since that would fail the same.
 *
 * The provider is named when the server said which it was, which it does while it still knows the trip.
 * Both the reason and the provider are words of the address, which anyone can write: each is one of a fixed
 * set, and only these fixed words are ever shown.
 */
export function providerProblem(
  reason: ProviderReason,
  provider?: Provider,
): { title: string; text: string } {
  const name = provider ? providerNames[provider] : null
  switch (reason) {
    case 'provider_unavailable':
      return {
        title: name
          ? `Signing in with ${name} isn't working right now`
          : "That sign-in isn't working right now",
        text: 'Nobody was signed in. Try again later, or sign in another way.',
      }
    case 'invitation_needed':
      return {
        title: 'An invitation is needed',
        text: 'There is no Permaily account for your address yet, and making one takes an invitation. Enter yours, then continue the same way.',
      }
    case 'invalid_invitation':
      return {
        title: 'Create your account',
        text: "That invitation isn't valid, or was used already. Check it, or ask for a new one.",
      }
    case 'provider_in_use':
      return {
        title: name ? `Signing in with ${name} didn't work` : "That sign-in didn't work",
        text: name
          ? `That ${name} account belongs with another Permaily account. Nobody was signed in.`
          : 'That account belongs with another Permaily account. Nobody was signed in.',
      }
    case 'provider_linked':
      return {
        title: name ? `Signing in with ${name} didn't work` : "That sign-in didn't work",
        text: name
          ? `Your Permaily account has another ${name} account linked already. Sign in with that one, or sign in another way and unlink it in Settings.`
          : 'Your Permaily account has another account of that provider linked already. Sign in with that one, or sign in another way and unlink it in Settings.',
      }
    case 'account_closing':
      return {
        title: 'That account is being deleted',
        text: 'Nothing was linked to it, and nobody was signed in. Sign in another way to cancel the deletion.',
      }
    case 'account_blocked':
      // The sign-in screen writes these words with the address as a link (Blocked.tsx).
      return {
        title: blockedTitle,
        text: `Nobody was signed in. To ask about it, write to ${supportAddress}.`,
      }
    default:
      return {
        title: name ? `Signing in with ${name} didn't work` : "That sign-in didn't work",
        text: 'Nobody was signed in. Try again, or sign in another way.',
      }
  }
}

/** The reasons that are about an invitation: the sign-up screen says them, next to where one is typed. */
export function needsInvitation(reason: ProviderReason): boolean {
  return reason === 'invitation_needed' || reason === 'invalid_invitation'
}

/**
 * What Settings says when linking a provider came back with nothing linked. A reason that linking cannot
 * end with reads as the one that says least. An account that is being deleted is said by its own screen.
 */
export function linkProblem(reason: ProviderReason, provider?: Provider): string {
  const name = provider ? providerNames[provider] : null
  switch (reason) {
    case 'provider_unavailable':
      return name
        ? `Nothing was linked: ${name} isn't working right now. Try again later.`
        : "Nothing was linked: that isn't working right now. Try again later."
    case 'provider_in_use':
      return name
        ? `Nothing was linked: that ${name} account belongs with another Permaily account.`
        : 'Nothing was linked: that account belongs with another Permaily account.'
    case 'provider_linked':
      return name
        ? `Nothing was linked: this account has another ${name} account linked already. Unlink it first.`
        : 'Nothing was linked: this account has an account of that provider linked already. Unlink it first.'
    case 'account_blocked':
      return `Nothing was linked: that account is blocked. To ask about it, write to ${supportAddress}.`
    default:
      return name
        ? `Nothing was linked: signing in with ${name} didn't work. Try again.`
        : "Nothing was linked: that didn't work. Try again."
  }
}
