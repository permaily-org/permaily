/**
 * The hand-off between a vendor and the consent screen (ADR 0030, "The hand-off between the vendor, the app and
 * the API"). A vendor sends the browser to /oauth/authorize?<its request>; the query string leaves the address bar
 * before the router or the session gate runs (`takeAuthorization`, called by src/main.tsx right after
 * `takeArrival`) and is held here, in this module's memory: never in the router's state, which the browser's
 * history keeps across a reload and restores, and never in storage. A reload, Back or a restored tab therefore
 * finds nothing, and the screen says to start again from the app; so does going anywhere else in the app
 * (`keepAuthorizationOn`). Signing in by passkey or by the code of the message happens in this tab and keeps it.
 */

import { matchRoutes } from 'react-router'

/** The page a vendor sends the browser to: the web app's, outside /api/. */
export const authorizePath = '/oauth/authorize'

/** The consent screen's route, as src/App.tsx declares it. */
export const authorizeRoute = authorizePath.slice(1)

/**
 * Whether the router shows the consent screen at this address's path: its matching, not a comparison of strings, so
 * that every spelling it reads as the page (another case, a trailing slash, an encoded letter) is cleaned too.
 */
export function isAuthorizePath(pathname: string): boolean {
  return matchRoutes([{ path: authorizeRoute }], pathname) !== null
}

/** The screens that keep the request: the consent screen, and signing in or up on the way to it, in this tab. */
const keeping = [{ path: authorizeRoute }, { path: 'sign-in' }, { path: 'sign-up' }]

/** The vendor's query string as the address bar had it after "?", or null. */
let held: string | null = null

const watching = new Set<() => void>()

function set(next: string | null) {
  if (held === next) return
  held = next
  for (const tell of watching) tell()
}

/**
 * Reads the address and cleans it: on /oauth/authorize, in any spelling the router reads as it, the query string
 * (and any fragment) leaves the address bar, which says /oauth/authorize, and a non-empty query string is held, the
 * newest counting. Call it before the router starts.
 */
export function takeAuthorization(): void {
  const { pathname, search, hash } = window.location
  if (!isAuthorizePath(pathname)) return
  if (search.length > 1) set(search.slice(1))
  if (search !== '' || hash !== '' || pathname !== authorizePath) {
    window.history.replaceState(window.history.state, '', authorizePath)
  }
}

/**
 * Forgets the request when the app shows another page than the consent screen or signing in: a link the screen
 * offers (to make room for a device or a connection) or anything else. Coming back to the screen, by Back within
 * the page too, then finds nothing. Called by src/App.tsx on every change of the address.
 */
export function keepAuthorizationOn(pathname: string): void {
  if (held !== null && matchRoutes(keeping, pathname) === null) set(null)
}

/** The vendor's request held for the consent screen, until it is decided or dropped. */
export function heldAuthorization(): string | null {
  return held
}

/** Forgets the vendor's request: it was decided, it ended, or the page was restored from the browser's cache. */
export function dropAuthorization(): void {
  set(null)
}

/** Calls `tell` whenever the request held changes, until the returned function is called. */
export function watchAuthorization(tell: () => void): () => void {
  watching.add(tell)
  return () => {
    watching.delete(tell)
  }
}

/** The hosts a vendor's loopback redirect may name (RFC 8252): its own listener on the person's computer. */
const loopbacks = new Set(['localhost', '127.0.0.1', '[::1]'])

/**
 * Whether an address the server answered is one the app may send the browser to: an absolute https address, or an
 * http one on the person's own computer. Anything else (a script, data, a relative path) is never followed.
 */
export function isVendorRedirect(address: string): boolean {
  let url: URL
  try {
    url = new URL(address)
  } catch {
    return false
  }
  if (url.username !== '' || url.password !== '') return false
  if (url.protocol === 'https:') return true
  return url.protocol === 'http:' && loopbacks.has(url.hostname)
}

/**
 * Sends the browser back to the vendor, to the address the server answered (`AuthorizationAnswer.redirect`, or
 * `AuthorizationStarted.redirect` for a request the server refused after its redirect URI was proven), and to
 * nothing else: the third place the app leaves for another site (web/AGENTS.md). The request held is forgotten
 * first. Answers false, and goes nowhere, for an address that is not one a vendor can be sent to.
 */
export function leaveForVendor(address: string): boolean {
  if (!isVendorRedirect(address)) return false
  set(null)
  window.location.assign(address)
  return true
}

// A page the browser restores from its back-forward cache keeps this module's memory: it is forgotten, so a
// restored tab says to start again, as a reload does.
window.addEventListener('pageshow', (event) => {
  if (event.persisted) set(null)
})
