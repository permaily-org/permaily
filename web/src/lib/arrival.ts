import { useSyncExternalStore } from 'react'
import { type Provider, providers } from './api.ts'

/**
 * What the address carried when the app was opened: the link of a sign-in message
 * (/sign-in#attempt=<id>&token=<token>), an invitation (/sign-up?invitation=<code>), or how a sign-in with a
 * provider ended (/sign-in#provider_confirm, /sign-in#provider_error=<reason>, /settings#provider_error=<reason>
 * and /settings#linked=<provider>). All are taken out of the address bar before anything renders, and live
 * only in this module's memory: never in storage, in history or in a log.
 */

export type SignInLink = { attempt: string; token: string }

type Arrival = {
  link: SignInLink | null
  /** The address had a sign-in link with a part missing, as when a mail app cuts it. */
  brokenLink: boolean
  invitation: string | null
}

let arrival: Arrival = { link: null, brokenLink: false, invitation: null }

/** The reasons the server ends a sign-in with a provider with. None says more than its name. */
export const providerReasons = [
  'provider_failed',
  'provider_unavailable',
  'invitation_needed',
  'invalid_invitation',
  'provider_in_use',
  'provider_linked',
  'account_closing',
] as const

export type ProviderReason = (typeof providerReasons)[number]

/**
 * How a trip to a provider ended, as the server said when it sent the browser back. It is a word of the
 * server's own, or of whoever wrote the address: it is only ever turned into fixed text, and what it says
 * of an account is asked of the server before it is believed.
 */
export type ProviderArrival =
  /** The provider did not vouch for an address: one waits to be confirmed, in this browser. */
  | { screen: 'sign-in'; kind: 'confirm' }
  /** Nobody was signed in, or nothing was linked. */
  | { screen: 'sign-in' | 'settings'; kind: 'failed'; reason: ProviderReason }
  /** Settings asked to link this provider, and the server sent the browser back without a reason. */
  | { screen: 'settings'; kind: 'linked'; provider: Provider }

let provider: ProviderArrival | null = null
const watching = new Set<() => void>()

function setProvider(next: ProviderArrival | null) {
  if (provider === next) return
  provider = next
  for (const tell of watching) tell()
}

/** What a fragment says of a trip to a provider, or null when it says nothing of one. */
function providerAnswer(pathname: string, fragment: URLSearchParams): ProviderArrival | null {
  const screen = pathname === '/sign-in' ? 'sign-in' : 'settings'
  const reason = fragment.get('provider_error')
  if (reason !== null) {
    // A reason this app does not know says the least of them: it did not work.
    const known = providerReasons.find((one) => one === reason) ?? 'provider_failed'
    return { screen, kind: 'failed', reason: known }
  }
  if (screen === 'sign-in') {
    return fragment.has('provider_confirm') ? { screen, kind: 'confirm' } : null
  }
  const linked = providers.find((one) => one === fragment.get('linked'))
  return linked ? { screen, kind: 'linked', provider: linked } : null
}

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
      // The newest one counts: a link that waited for an answer is forgotten either way.
      if (attempt && token) arrival = { ...arrival, link: { attempt, token }, brokenLink: false }
      else arrival = { ...arrival, link: null, brokenLink: true }
      cleanHash = ''
    }
  }

  if ((pathname === '/sign-in' || pathname === '/settings') && cleanHash.length > 1) {
    const answer = providerAnswer(pathname, new URLSearchParams(cleanHash.slice(1)))
    if (answer) {
      // The newest one counts.
      setProvider(answer)
      cleanHash = ''
    }
  }
  // Meta ends the address it sends the browser back to with "#_=_", and the browser carries that along to
  // wherever the server sends it next. It means nothing.
  if (cleanHash === '#_=_') cleanHash = ''

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

/**
 * The link the app was opened with, until it is used or dropped. Opening it signs nobody in: the page asks
 * the server whose sign-in it is, and uses it only when the person says so.
 */
export function pendingLink(): SignInLink | null {
  return arrival.link
}

/** Forgets the link: it worked, it never will, or the person said no. */
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

/**
 * Says how a trip to a provider ended, as if the address had: for a sign-in that ended while its address
 * was being confirmed, so the sign-in screen says why.
 */
export function providerEnded(reason: ProviderReason): void {
  setProvider({ screen: 'sign-in', kind: 'failed', reason })
}

/** Forgets how a trip to a provider ended: it was said, or it is of no use any more. */
export function dropProviderArrival(screen?: ProviderArrival['screen']): void {
  if (provider && (!screen || provider.screen === screen)) setProvider(null)
}

function watch(tell: () => void) {
  watching.add(tell)
  return () => {
    watching.delete(tell)
  }
}

/** How a trip to a provider ended, until it is dropped. A screen that shows it follows when another arrives. */
export function useProviderArrival(): ProviderArrival | null {
  return useSyncExternalStore(watch, () => provider)
}
