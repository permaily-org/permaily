import type * as api from '../../lib/api.ts'
import { isClosing, termsToAgree, useSession } from '../../lib/session.tsx'

/**
 * What the account's standing lets the person do with mailboxes (ADR 0026, "Access while an account is closing
 * or has not agreed"). The server enforces it route by route; the app only offers what it allows.
 */
export type Standing = {
  /** The person asked to delete the account, or Permaily's operator did. */
  closing: boolean
  /** The terms in force are not the ones the person agreed to. */
  terms: boolean
  /** When a closing account is removed, as the server said. */
  closesAt?: string
}

export function standingOf(me: api.Me): Standing {
  return {
    closing: isClosing(me.account),
    terms: termsToAgree(me) !== null,
    ...(me.account.closes_at ? { closesAt: me.account.closes_at } : {}),
  }
}

/** The standing of the account signed in. Every page that uses it sits behind `RequireSession`. */
export function useStanding(): Standing {
  const { state } = useSession()
  return state.status === 'signedIn' ? standingOf(state.me) : { closing: false, terms: false }
}

/** True while the account is closing or has not agreed: mailboxes then show in a limited form. */
export function limited(standing: Standing): boolean {
  return standing.closing || standing.terms
}

/** Making a mailbox, renaming it and its retention: only an account that is open and agreed. */
export function canChange(standing: Standing): boolean {
  return !limited(standing)
}

/** Opening and searching messages: a closing account may, one that has not agreed may not. */
export function canRead(standing: Standing): boolean {
  return !standing.terms
}

/** Deleting messages: only an account that is open and agreed. */
export function canDeleteMail(standing: Standing): boolean {
  return !limited(standing)
}

/**
 * The routes a closing account, or one that has not agreed, still reaches: the list of mailboxes, a mailbox's
 * forwarding set-up and the inbox. Every other route shows the screen that blocks it.
 */
export function isMailboxPath(pathname: string): boolean {
  return (
    /^\/accounts\/?$/.test(pathname) ||
    /^\/accounts\/(?!new\/)[^/]+\/forwarding\/?$/.test(pathname) ||
    /^\/inbox(\/.*)?$/.test(pathname)
  )
}
