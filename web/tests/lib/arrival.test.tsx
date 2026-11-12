import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { blockStorage, current } from '../helpers/app.tsx'

/** The module as a page that just loaded has it: nothing in its memory. */
async function load() {
  vi.resetModules()
  return import('../../src/lib/arrival.ts')
}

/** Loads the page at an address and reads it, as src/main.tsx does. */
async function arrive(address: string) {
  window.history.replaceState({ kept: 'by the history' }, '', address)
  const arrival = await load()
  arrival.takeArrival()
  return arrival
}

/** How a trip to a provider ended, as a screen sees it. */
function provider(arrival: Awaited<ReturnType<typeof load>>) {
  return renderHook(() => arrival.useProviderArrival()).result.current
}

describe('the link of a sign-in message', () => {
  it('is read and taken out of the address bar, and the history keeps its state', async () => {
    const arrival = await arrive('/sign-in#attempt=att_1&token=tok_secret')
    expect(current()).toBe('/sign-in')
    expect(window.history.state).toEqual({ kept: 'by the history' })
    expect(arrival.hasLink()).toBe(true)
    expect(arrival.pendingLink()).toEqual({ attempt: 'att_1', token: 'tok_secret' })
    arrival.dropLink()
    expect(arrival.pendingLink()).toBeNull()
    expect(arrival.hasLink()).toBe(false)
  })

  it.each([
    '/sign-in#attempt=att_1',
    '/sign-in#token=tok_secret',
    '/sign-in#attempt=&token=tok_secret',
  ])('is known to be cut short at %s, said once', async (address) => {
    const arrival = await arrive(address)
    expect(current()).toBe('/sign-in')
    expect(arrival.pendingLink()).toBeNull()
    expect(arrival.hasLink()).toBe(true)
    expect(arrival.takeBrokenLink()).toBe(true)
    expect(arrival.takeBrokenLink()).toBe(false)
  })

  it('is the newest one when another arrives', async () => {
    const arrival = await arrive('/sign-in#attempt=att_1&token=tok_1')
    window.history.replaceState(null, '', '/sign-in#attempt=att_2&token=tok_2')
    arrival.takeArrival()
    expect(arrival.pendingLink()).toEqual({ attempt: 'att_2', token: 'tok_2' })
    window.history.replaceState(null, '', '/sign-in#attempt=att_3')
    arrival.takeArrival()
    expect(arrival.pendingLink()).toBeNull()
    expect(arrival.takeBrokenLink()).toBe(true)
  })

  it('is only read on the sign-in screen', async () => {
    const arrival = await arrive('/rules#attempt=att_1&token=tok_1')
    expect(current()).toBe('/rules#attempt=att_1&token=tok_1')
    expect(arrival.hasLink()).toBe(false)
  })
})

describe('an invitation in the address', () => {
  it('is read and taken out, and the rest of the address stays', async () => {
    const arrival = await arrive('/sign-up?invitation=ABCD-2345-EFGH&ref=mail#top')
    expect(current()).toBe('/sign-up?ref=mail#top')
    expect(arrival.arrivedInvitation()).toBe('ABCD-2345-EFGH')
    arrival.dropInvitation()
    expect(arrival.arrivedInvitation()).toBeNull()
  })

  it('is only read on the sign-up screen', async () => {
    const arrival = await arrive('/sign-in?invitation=ABCD-2345-EFGH')
    expect(current()).toBe('/sign-in?invitation=ABCD-2345-EFGH')
    expect(arrival.arrivedInvitation()).toBeNull()
  })
})

describe('how a trip to a provider ended', () => {
  it('asks for an address to confirm', async () => {
    const arrival = await arrive('/sign-in#provider_confirm')
    expect(current()).toBe('/sign-in')
    expect(provider(arrival)).toEqual({ screen: 'sign-in', kind: 'confirm' })
  })

  it.each([
    'provider_failed',
    'provider_unavailable',
    'invitation_needed',
    'invalid_invitation',
    'provider_in_use',
    'provider_linked',
    'account_closing',
    'account_blocked',
  ] as const)('reads the reason %s on both screens', async (reason) => {
    let arrival = await arrive(`/sign-in#provider_error=${reason}`)
    expect(current()).toBe('/sign-in')
    expect(provider(arrival)).toEqual({ screen: 'sign-in', kind: 'failed', reason })
    arrival = await arrive(`/settings#provider_error=${reason}`)
    expect(current()).toBe('/settings')
    expect(provider(arrival)).toEqual({ screen: 'settings', kind: 'failed', reason })
  })

  it.each([
    '',
    'something_new',
    'PROVIDER_IN_USE',
    '<img src=x onerror=alert(1)>',
    'Your account was deleted. Call 555-0100',
    'provider_in_use&provider_error=provider_linked',
  ])('reads a reason it does not know (%s) as the one that says least', async (reason) => {
    const arrival = await arrive(`/sign-in#provider_error=${encodeURIComponent(reason)}`)
    expect(current()).toBe('/sign-in')
    expect(provider(arrival)).toEqual({
      screen: 'sign-in',
      kind: 'failed',
      reason: 'provider_failed',
    })
  })

  it.each(['google', 'github', 'meta', 'microsoft'] as const)(
    'names %s when the server said which provider it was',
    async (named) => {
      const arrival = await arrive(`/settings#provider_error=provider_in_use&provider=${named}`)
      expect(current()).toBe('/settings')
      expect(provider(arrival)).toEqual({
        screen: 'settings',
        kind: 'failed',
        reason: 'provider_in_use',
        provider: named,
      })
    },
  )

  it.each(['', 'apple', 'Google', 'google ', 'meta<script>', 'Ana (ana@example.org)', '__proto__'])(
    'names no provider for anything but the four it knows (%s)',
    async (named) => {
      const arrival = await arrive(
        `/sign-in#provider_error=provider_failed&provider=${encodeURIComponent(named)}`,
      )
      expect(current()).toBe('/sign-in')
      expect(provider(arrival)).toEqual({
        screen: 'sign-in',
        kind: 'failed',
        reason: 'provider_failed',
      })
    },
  )

  it('reads that Settings linked a provider, which the server list still has to agree with', async () => {
    const arrival = await arrive('/settings#linked=meta')
    expect(current()).toBe('/settings')
    expect(provider(arrival)).toEqual({ screen: 'settings', kind: 'linked', provider: 'meta' })
  })

  it('reads nothing into a provider it does not know, or into another screen', async () => {
    let arrival = await arrive('/settings#linked=apple')
    expect(provider(arrival)).toBeNull()
    arrival = await arrive('/sign-in#linked=google')
    expect(provider(arrival)).toBeNull()
    arrival = await arrive('/settings#provider_confirm')
    expect(provider(arrival)).toBeNull()
    arrival = await arrive('/rules#provider_error=provider_failed')
    expect(current()).toBe('/rules#provider_error=provider_failed')
    expect(provider(arrival)).toBeNull()
  })

  it('takes out the stray "#_=_" Meta ends an address with, on any screen', async () => {
    await arrive('/rules#_=_')
    expect(current()).toBe('/rules')
    await arrive('/settings#_=_')
    expect(current()).toBe('/settings')
  })

  it('is the newest one, and a screen that shows it follows', async () => {
    const arrival = await arrive('/sign-in#provider_error=provider_failed')
    const seen = renderHook(() => arrival.useProviderArrival())
    act(() => {
      window.history.replaceState(null, '', '/sign-in#provider_error=provider_in_use')
      arrival.takeArrival()
    })
    expect(seen.result.current).toMatchObject({ reason: 'provider_in_use' })
    act(() => arrival.providerEnded('account_closing', 'meta'))
    expect(seen.result.current).toEqual({
      screen: 'sign-in',
      kind: 'failed',
      reason: 'account_closing',
      provider: 'meta',
    })
    act(() => arrival.providerEnded('provider_linked'))
    expect(seen.result.current).toEqual({
      screen: 'sign-in',
      kind: 'failed',
      reason: 'provider_linked',
    })
    // What is for one screen is not dropped by the other.
    act(() => arrival.dropProviderArrival('settings'))
    expect(seen.result.current).not.toBeNull()
    act(() => arrival.dropProviderArrival('sign-in'))
    expect(seen.result.current).toBeNull()
    act(() => arrival.providerEnded('provider_failed'))
    act(() => arrival.dropProviderArrival())
    expect(seen.result.current).toBeNull()
    seen.unmount()
  })

  it('is said the same for a sign-in that ended at a blocked account without a provider', async () => {
    const arrival = await arrive('/sign-in')
    expect(provider(arrival)).toBeNull()
    const seen = renderHook(() => arrival.useProviderArrival())
    act(() => arrival.signInBlocked())
    expect(seen.result.current).toEqual({
      screen: 'sign-in',
      kind: 'failed',
      reason: 'account_blocked',
    })
    // Said again, it is a new answer: a screen that shows it follows, and puts focus where it belongs.
    const first = seen.result.current
    act(() => arrival.signInBlocked())
    expect(seen.result.current).toEqual(first)
    expect(seen.result.current).not.toBe(first)
    // Nothing of it is kept anywhere.
    expect(current()).toBe('/sign-in')
    expect({ ...sessionStorage }).toEqual({})
    expect({ ...localStorage }).toEqual({})
    seen.unmount()
  })

  it('is for Settings when the person turns out to be signed in, if it is a failure', async () => {
    let arrival = await arrive('/sign-in#provider_error=provider_failed&provider=github')
    arrival.providerArrivalSignedIn()
    expect(provider(arrival)).toEqual({
      screen: 'settings',
      kind: 'failed',
      reason: 'provider_failed',
      provider: 'github',
    })
    // Once is enough: it stays where it is.
    arrival.providerArrivalSignedIn()
    expect(provider(arrival)).toMatchObject({ screen: 'settings' })

    arrival = await arrive('/sign-in#provider_confirm')
    arrival.providerArrivalSignedIn()
    expect(provider(arrival)).toBeNull()

    arrival = await arrive('/settings#linked=google')
    arrival.providerArrivalSignedIn()
    expect(provider(arrival)).toEqual({ screen: 'settings', kind: 'linked', provider: 'google' })

    arrival = await arrive('/rules')
    arrival.providerArrivalSignedIn()
    expect(provider(arrival)).toBeNull()
  })
})

describe('an invitation across a trip to a provider', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/sign-up')
  })

  it('waits in the storage of the tab, as the server writes it, and nowhere else', async () => {
    const arrival = await load()
    arrival.keepInvitation('ABCD-2345-EFGH')
    expect({ ...sessionStorage }).toEqual({ 'permaily-invitation': 'abcd2345efgh' })
    expect({ ...localStorage }).toEqual({})
  })

  it('is back in memory and out of storage as soon as the app loads again', async () => {
    ;(await load()).keepInvitation('abcd2345efgh')
    const arrival = await arrive('/sign-in#provider_error=invitation_needed')
    expect({ ...sessionStorage }).toEqual({})
    expect(arrival.arrivedInvitation()).toBe('abcd2345efgh')
    // And only once: the next page has nothing to read.
    expect((await arrive('/sign-up')).arrivedInvitation()).toBeNull()
  })

  it('leaves storage whatever the trip came to, and wherever the app opens', async () => {
    for (const address of ['/', '/settings', '/sign-in#provider_confirm', '/connections']) {
      ;(await load()).keepInvitation('abcd2345efgh')
      await arrive(address)
      expect({ ...sessionStorage }).toEqual({})
    }
  })

  it('gives way to one in the address, which is newer', async () => {
    ;(await load()).keepInvitation('abcd2345efgh')
    const arrival = await arrive('/sign-up?invitation=WXYZ-2345-EFGH')
    expect(arrival.arrivedInvitation()).toBe('WXYZ-2345-EFGH')
    expect({ ...sessionStorage }).toEqual({})
  })

  it('keeps nothing that cannot be an invitation, and forgets on request', async () => {
    const arrival = await load()
    for (const nothing of [null, undefined, '', 'abcd', 'ana@example.org', 'abcd2345efgh6789']) {
      arrival.keepInvitation(nothing)
      expect({ ...sessionStorage }).toEqual({})
    }
    arrival.keepInvitation('abcd2345efgh')
    arrival.forgetKeptInvitation()
    expect({ ...sessionStorage }).toEqual({})
  })

  it('reads nothing that is not an invitation, and still takes it out', async () => {
    for (const written of ['<script>alert(1)</script>', 'ana@example.org', '', 'abcd2345efg0']) {
      sessionStorage.setItem('permaily-invitation', written)
      const arrival = await arrive('/sign-up')
      expect(arrival.arrivedInvitation()).toBeNull()
      expect({ ...sessionStorage }).toEqual({})
    }
  })

  it('is typed again where the storage of the tab is unavailable: nothing fails', async () => {
    blockStorage('sessionStorage')
    const arrival = await load()
    expect(() => arrival.keepInvitation('abcd2345efgh')).not.toThrow()
    expect(() => arrival.forgetKeptInvitation()).not.toThrow()
    expect(() => arrival.takeArrival()).not.toThrow()
    expect(arrival.arrivedInvitation()).toBeNull()
  })
})
