import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { providers } from '../../src/lib/api.ts'
import { watchLeaving } from '../helpers/app.tsx'
import { network, ok, server } from '../helpers/server.ts'

/** The module as a page that just loaded has it: it has not asked which providers are on. */
async function load() {
  vi.resetModules()
  return import('../../src/lib/providers.ts')
}

describe('what each provider is called on screen', () => {
  it('is one name for each identifier, and Facebook for meta', async () => {
    const { providerNames } = await load()
    expect(providers).toEqual(['google', 'github', 'meta', 'microsoft'])
    expect(providerNames).toEqual({
      google: 'Google',
      github: 'GitHub',
      meta: 'Facebook',
      microsoft: 'Microsoft',
    })
  })
})

describe('leaving for a provider', () => {
  it.each([
    'https://accounts.google.com/o/oauth2/v2/auth?state=abc',
    'https://www.facebook.com/v23.0/dialog/oauth?state=abc',
    'http://127.0.0.1:18443/dev/provider/google?state=abc',
    'http://localhost:8080/authorize',
    'http://[::1]:8080/authorize',
  ])('goes to %s', async (address) => {
    const { leaveFor } = await load()
    const assign = watchLeaving()
    expect(leaveFor(address)).toBe(true)
    expect(assign).toHaveBeenCalledExactlyOnceWith(new URL(address).href)
  })

  it.each([
    '',
    'javascript:alert(document.cookie)',
    'JaVaScRiPt:alert(1)',
    'data:text/html,<script>alert(1)</script>',
    'blob:https://app.permaily.com/1',
    'file:///etc/passwd',
    'ftp://example.org/',
    'http://accounts.google.com/',
    'http://127.0.0.1.evil.example/',
    'http://localhost.evil.example/',
    'http://evil.example/127.0.0.1',
    '//evil.example/',
    '/settings',
    'accounts.google.com',
    'https://',
    'not an address',
  ])('goes nowhere for %s', async (address) => {
    const { leaveFor } = await load()
    const assign = watchLeaving()
    expect(leaveFor(address)).toBe(false)
    expect(assign).not.toHaveBeenCalled()
  })
})

describe('coming back with the Back button', () => {
  it('is told only when the browser shows the page as it was left', async () => {
    const { useCameBack } = await load()
    const back = vi.fn()
    const hook = renderHook(() => useCameBack(back))
    const shown = (persisted: boolean) =>
      window.dispatchEvent(Object.assign(new Event('pageshow'), { persisted }))
    shown(false)
    expect(back).not.toHaveBeenCalled()
    shown(true)
    expect(back).toHaveBeenCalledTimes(1)
    hook.unmount()
    shown(true)
    expect(back).toHaveBeenCalledTimes(1)
  })
})

describe('the providers that are on', () => {
  it('are none until the server answers, then what it said', async () => {
    const { useProvidersOn } = await load()
    server.on('GET /api/v1/auth/providers', ok({ providers: ['meta', 'google'] }))
    const hook = renderHook(() => useProvidersOn())
    expect(hook.result.current[0]).toEqual([])
    await waitFor(() => expect(hook.result.current[0]).toEqual(['google', 'meta']))
    // The next screen shows the same ones at once, and asks again.
    server.on('GET /api/v1/auth/providers', ok({ providers: ['google'] }))
    const next = renderHook(() => useProvidersOn())
    expect(next.result.current[0]).toEqual(['google', 'meta'])
    await waitFor(() => expect(next.result.current[0]).toEqual(['google']))
    // And on request.
    server.on('GET /api/v1/auth/providers', ok({ providers: [] }))
    act(() => next.result.current[1]())
    await waitFor(() => expect(next.result.current[0]).toEqual([]))
  })

  it('are none when the server gives no answer', async () => {
    const { useProvidersOn } = await load()
    server.on('GET /api/v1/auth/providers', network)
    const hook = renderHook(() => useProvidersOn())
    await waitFor(() => expect(server.calls).toHaveLength(1))
    expect(hook.result.current[0]).toEqual([])
  })
})
