/**
 * What the address carried when the app was opened: the link of a sign-in message
 * (/sign-in#attempt=<id>&token=<token>) or an invitation (/sign-up?invitation=<code>).
 * Both are taken out of the address bar before anything renders, and live only in this module's memory:
 * never in storage, in history or in a log.
 */

export type SignInLink = { attempt: string; token: string }

type Arrival = {
  link: SignInLink | null
  /** The address had a sign-in link with a part missing, as when a mail app cuts it. */
  brokenLink: boolean
  invitation: string | null
}

let arrival: Arrival = { link: null, brokenLink: false, invitation: null }

/** Reads the address and cleans it. Call it before the router starts, and when the fragment changes. */
export function takeArrival(): void {
  const { pathname, search, hash } = window.location
  let cleanSearch = search
  let cleanHash = hash

  if (pathname === '/sign-in' && hash.length > 1) {
    const fragment = new URLSearchParams(hash.slice(1))
    const attempt = fragment.get('attempt')
    const token = fragment.get('token')
    // A fragment on this page is a sign-in link, whole or broken. Neither stays in the address bar.
    if (fragment.has('attempt') || fragment.has('token')) {
      if (attempt && token) arrival.link = { attempt, token }
      else arrival.brokenLink = true
      cleanHash = ''
    }
  }

  if (pathname === '/sign-up') {
    const query = new URLSearchParams(search)
    const invitation = query.get('invitation')
    if (invitation !== null) {
      arrival.invitation = invitation
      query.delete('invitation')
      const rest = query.toString()
      cleanSearch = rest ? `?${rest}` : ''
    }
  }

  if (cleanSearch !== search || cleanHash !== hash) {
    window.history.replaceState(window.history.state, '', pathname + cleanSearch + cleanHash)
  }
}

/** True while a sign-in link, whole or cut short, waits to be dealt with. */
export function hasLink(): boolean {
  return arrival.link !== null || arrival.brokenLink
}

/** The link the app was opened with, until it is used or dropped. */
export function pendingLink(): SignInLink | null {
  return arrival.link
}

/** Forgets the link: it worked, or it never will. */
export function dropLink(): void {
  arrival = { ...arrival, link: null }
}

/** True once, when the address had a sign-in link that was cut short. */
export function takeBrokenLink(): boolean {
  const { brokenLink } = arrival
  arrival = { ...arrival, brokenLink: false }
  return brokenLink
}

/** The invitation the app was opened with, until it is used. */
export function arrivedInvitation(): string | null {
  return arrival.invitation
}

/** Forgets the invitation once an account has been made with it. */
export function dropInvitation(): void {
  arrival = { ...arrival, invitation: null }
}
