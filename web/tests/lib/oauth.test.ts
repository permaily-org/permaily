import { beforeEach, describe, expect, it, vi } from 'vitest'
import { current, expectNotKept, watchLeaving } from '../helpers/app.tsx'

type OAuth = typeof import('../../src/lib/oauth.ts')

/** The module as a page load starts it: nothing held. */
async function fresh(): Promise<OAuth> {
  vi.resetModules()
  return import('../../src/lib/oauth.ts')
}

const query = 'response_type=code&client_id=https%3A%2F%2Fclaude.ai%2Fx&state=st8te-secret'

describe('the vendor’s request on /oauth/authorize', () => {
  let oauth: OAuth
  beforeEach(async () => {
    oauth = await fresh()
  })

  it('leaves the address bar and is held in memory only', () => {
    window.history.replaceState(
      { usr: null, key: 'k', idx: 0 },
      '',
      `/oauth/authorize?${query}#frag`,
    )
    oauth.takeAuthorization()
    expect(current()).toBe('/oauth/authorize')
    expect(oauth.heldAuthorization()).toBe(query)
    // The router's own state is kept as it was, and holds nothing of the request.
    expect(window.history.state).toEqual({ usr: null, key: 'k', idx: 0 })
    expectNotKept('st8te-secret', 'claude.ai')
  })

  it('the newest counts, and an address without one keeps what is held', () => {
    window.history.replaceState(null, '', '/oauth/authorize?a=1')
    oauth.takeAuthorization()
    window.history.replaceState(null, '', '/oauth/authorize?b=2')
    oauth.takeAuthorization()
    expect(oauth.heldAuthorization()).toBe('b=2')
    window.history.replaceState(null, '', '/oauth/authorize')
    oauth.takeAuthorization()
    expect(oauth.heldAuthorization()).toBe('b=2')
    window.history.replaceState(null, '', '/oauth/authorize?')
    oauth.takeAuthorization()
    expect(oauth.heldAuthorization()).toBe('b=2')
    expect(current()).toBe('/oauth/authorize')
  })

  it('is read on every spelling of its page the router reads as it, and the address becomes the page’s own', () => {
    for (const alias of [
      '/oauth/authorize/',
      '/OAuth/Authorize',
      '/oauth/%61uthorize',
      '/OAUTH/AUTHORIZE/',
    ]) {
      window.history.replaceState(null, '', `${alias}?${query}#frag`)
      oauth.takeAuthorization()
      expect(current()).toBe('/oauth/authorize')
      expect(oauth.heldAuthorization()).toBe(query)
      oauth.dropAuthorization()
    }
  })

  it('is forgotten when the app goes anywhere but its page and signing in', () => {
    window.history.replaceState(null, '', `/oauth/authorize?${query}`)
    oauth.takeAuthorization()
    for (const kept of ['/oauth/authorize', '/OAuth/Authorize/', '/sign-in', '/sign-up']) {
      oauth.keepAuthorizationOn(kept)
      expect(oauth.heldAuthorization()).toBe(query)
    }
    oauth.keepAuthorizationOn('/connections')
    expect(oauth.heldAuthorization()).toBeNull()
  })

  it('is read only on its own page', () => {
    window.history.replaceState(null, '', `/oauth/authorize/x?${query}`)
    oauth.takeAuthorization()
    window.history.replaceState(null, '', `/connections?${query}`)
    oauth.takeAuthorization()
    expect(oauth.heldAuthorization()).toBeNull()
    expect(current()).toBe(`/connections?${query}`)
  })

  it('tells who watches when it changes, and stops when asked', () => {
    const tell = vi.fn()
    const stop = oauth.watchAuthorization(tell)
    window.history.replaceState(null, '', '/oauth/authorize?a=1')
    oauth.takeAuthorization()
    oauth.takeAuthorization()
    expect(tell).toHaveBeenCalledTimes(1)
    oauth.dropAuthorization()
    expect(tell).toHaveBeenCalledTimes(2)
    expect(oauth.heldAuthorization()).toBeNull()
    stop()
    window.history.replaceState(null, '', '/oauth/authorize?a=2')
    oauth.takeAuthorization()
    expect(tell).toHaveBeenCalledTimes(2)
  })

  it('is forgotten when the browser restores the page from its cache, and not on a plain show', () => {
    window.history.replaceState(null, '', '/oauth/authorize?a=1')
    oauth.takeAuthorization()
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }))
    expect(oauth.heldAuthorization()).toBe('a=1')
    window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    expect(oauth.heldAuthorization()).toBeNull()
  })
})

describe('leaving for the vendor', () => {
  it('goes only to an https address, or an http one on the person’s own computer', async () => {
    const oauth = await fresh()
    for (const good of [
      'https://claude.ai/api/mcp/auth_callback?code=c&state=s&iss=i',
      'http://localhost:33418/callback?code=c',
      'http://127.0.0.1/callback',
      'http://[::1]:8080/callback',
    ]) {
      expect(oauth.isVendorRedirect(good)).toBe(true)
    }
    for (const bad of [
      'javascript:alert(1)',
      'data:text/html,hi',
      '/connections',
      '//evil.example/x',
      'http://evil.example/callback',
      'http://localhost.evil.example/callback',
      'https://user:pass@claude.ai/x',
      'ftp://claude.ai/x',
      '',
    ]) {
      expect(oauth.isVendorRedirect(bad)).toBe(false)
    }
  })

  it('forgets the request and sends the browser there, and nowhere else', async () => {
    const oauth = await fresh()
    const assign = watchLeaving()
    window.history.replaceState(null, '', '/oauth/authorize?a=1')
    oauth.takeAuthorization()
    expect(oauth.leaveForVendor('javascript:alert(1)')).toBe(false)
    expect(assign).not.toHaveBeenCalled()
    expect(oauth.heldAuthorization()).toBe('a=1')
    expect(oauth.leaveForVendor('http://localhost:1/cb?code=c')).toBe(true)
    expect(assign).toHaveBeenCalledWith('http://localhost:1/cb?code=c')
    expect(oauth.heldAuthorization()).toBeNull()
  })
})
