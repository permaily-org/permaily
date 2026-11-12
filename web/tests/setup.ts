import { cleanup, configure } from '@testing-library/react'
import { afterEach, beforeEach, expect, vi } from 'vitest'
import { server } from './helpers/server.ts'

// A query that finds nothing says so in a line, without the whole page. A find waits up to five seconds,
// not one: on a machine busy with other work the screens take longer to come, and a find only waits as
// long as what it looks for is not there (an absence is checked with a query, which does not wait).
configure({
  getElementError: (message) => new Error(message ?? 'not found'),
  asyncUtilTimeout: 5000,
})

// jsdom has no layout and no media queries. The app is wide: the sidebar is open.
function matchMedia(media: string) {
  return {
    matches: true,
    media,
    addEventListener: () => {},
    removeEventListener: () => {},
  }
}

// jsdom has no modal dialogs. This is as much of one as the app relies on: it opens, it closes and says so,
// and focus goes back to what had it, as a browser does.
const openers = new WeakMap<HTMLDialogElement, Element | null>()
if (typeof HTMLDialogElement.prototype.showModal !== 'function') {
  HTMLDialogElement.prototype.showModal = function showModal() {
    openers.set(this, document.activeElement)
    this.setAttribute('open', '')
  }
  HTMLDialogElement.prototype.close = function close() {
    if (!this.hasAttribute('open')) return
    this.removeAttribute('open')
    const opener = openers.get(this)
    if (opener instanceof HTMLElement && opener.isConnected) opener.focus()
    this.dispatchEvent(new Event('close'))
  }
}

beforeEach(() => {
  server.reset()
  vi.stubGlobal('fetch', server.fetch)
  vi.stubGlobal('matchMedia', matchMedia)
  localStorage.clear()
  sessionStorage.clear()
  window.history.replaceState(null, '', '/')
  document.title = 'Permaily'
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  // Before the next test clears the storages: a test may have made one unavailable.
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  // A browser with passkeys is one a test made (tests/helpers/passkeys.ts).
  Reflect.deleteProperty(navigator, 'credentials')
  // A request the test did not expect is a failure: the app asked for something nobody answered.
  expect(server.unexpected).toEqual([])
})
