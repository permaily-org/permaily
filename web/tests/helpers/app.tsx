import { act, cleanup, render } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { BrowserRouter } from 'react-router'
import { expect, vi } from 'vitest'

/**
 * Opens the app at an address, as a browser loading the page does (src/main.tsx): every module of the app
 * starts from nothing, the address is read and cleaned before anything renders, and the session is asked of
 * the server. Calling it again is a reload: what the page had in memory is gone, and storage, the address and
 * the history stay.
 */
export async function open(address = '/') {
  cleanup()
  vi.resetModules()
  if (address !== current()) window.history.pushState(null, '', address)
  const [{ App }, { SessionProvider }, arrival] = await Promise.all([
    import('../../src/App.tsx'),
    import('../../src/lib/session.tsx'),
    import('../../src/lib/arrival.ts'),
  ])
  arrival.takeArrival()
  const view = render(
    <BrowserRouter>
      <SessionProvider>
        <App />
      </SessionProvider>
    </BrowserRouter>,
  )
  return { ...view, user: userEvent.setup() }
}

/** The address bar, without the origin. */
export function current(): string {
  const { pathname, search, hash } = window.location
  return pathname + search + hash
}

/**
 * Nothing of these is anywhere it could be read back from: not in `localStorage`, not in `sessionStorage`,
 * not in the address bar and not in the history's state (docs/web.md).
 */
export function expectNotKept(...secrets: string[]) {
  const kept = [
    JSON.stringify({ ...localStorage }),
    JSON.stringify({ ...sessionStorage }),
    window.location.href,
    JSON.stringify(window.history.state),
  ]
    .join('\n')
    .toLowerCase()
  for (const secret of secrets) expect(kept).not.toContain(secret.toLowerCase())
}

/**
 * Takes the place of `window.location` so a test sees where the app sends the browser instead of jsdom
 * trying to go there. Everything else of the address stays the real one.
 */
export function watchLeaving() {
  const real = window.location
  const assign = vi.fn<(address: string) => void>()
  vi.stubGlobal('location', {
    get href() {
      return real.href
    },
    get origin() {
      return real.origin
    },
    get protocol() {
      return real.protocol
    },
    get host() {
      return real.host
    },
    get hostname() {
      return real.hostname
    },
    get pathname() {
      return real.pathname
    },
    get search() {
      return real.search
    },
    get hash() {
      return real.hash
    },
    assign,
  })
  return assign
}

/**
 * Makes a storage unavailable until the test ends, as in a browser that blocks site data: every use of it
 * throws.
 */
export function blockStorage(name: 'localStorage' | 'sessionStorage') {
  const blocked = () => {
    throw new DOMException('The operation is insecure.', 'SecurityError')
  }
  vi.stubGlobal(name, {
    getItem: blocked,
    setItem: blocked,
    removeItem: blocked,
    clear: blocked,
    key: blocked,
    get length(): number {
      return blocked()
    },
  })
}

/**
 * The name of a control whose last words are for screen readers only, such as "Edit" and " name". A browser
 * reads it as "Edit name"; the library these tests ask drops the space between the two, so either is taken.
 */
export function named(text: string): RegExp {
  const words = text.split(' ').map((word) => word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  return new RegExp(`^${words.join('\\s*')}$`)
}

/**
 * Lets everything the page has under way finish: what React does after it draws, such as moving focus. For
 * saying that something did not happen, which is true of any page that has not got round to it yet.
 */
export async function settle(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}
