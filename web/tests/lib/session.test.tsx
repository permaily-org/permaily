import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type * as apiModule from '../../src/lib/api.ts'
import type * as sessionModule from '../../src/lib/session.tsx'
import { ana, later, meOf, network, ok, refused, server } from '../helpers/server.ts'

type Session = ReturnType<typeof sessionModule.useSession>

/** The session as a page that just loaded has it, with a way to see it from outside. */
async function start(address = '/') {
  vi.resetModules()
  window.history.replaceState(null, '', address)
  const [session, arrival, api] = await Promise.all([
    import('../../src/lib/session.tsx'),
    import('../../src/lib/arrival.ts'),
    import('../../src/lib/api.ts'),
  ])
  arrival.takeArrival()
  const hook = renderHook(() => session.useSession(), { wrapper: session.SessionProvider })
  const seen = {
    get now(): Session {
      return hook.result.current
    },
  }
  const settled = (status: Session['state']['status']) =>
    waitFor(() => expect(seen.now.state.status).toBe(status))
  return { seen, settled, session, arrival, api: api as typeof apiModule }
}

const closing = { ...ana, state: 'closing' as const, closes_at: '2026-11-18T14:32:00Z' }

describe('who is signed in', () => {
  it('is asked of the server when the app opens', async () => {
    server.signedIn()
    const { seen, settled } = await start()
    expect(seen.now.state).toEqual({ status: 'loading', link: false })
    await settled('signedIn')
    expect(seen.now.state).toMatchObject({ me: { account: { address: 'ana@example.org' } } })
    expect(server.asked('GET /api/v1/me')).toHaveLength(1)
  })

  it('is nobody when the server says so, and unknown when it says nothing', async () => {
    server.signedOut()
    let { seen, settled } = await start()
    await settled('signedOut')
    expect(seen.now.state).toEqual({ status: 'signedOut', byChoice: false })
    for (const answer of [network, refused(500, 'internal')]) {
      server.on('GET /api/v1/me', answer)
      ;({ seen, settled } = await start())
      await settled('unreachable')
      server.signedIn()
      act(() => seen.now.retry())
      await settled('signedIn')
    }
  })

  it('cannot be used outside its provider', async () => {
    server.signedOut()
    const { session } = await start()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    expect(() => renderHook(() => session.useSession())).toThrow(
      'useSession needs a SessionProvider above it',
    )
  })

  it('knows a closing account from one that works', async () => {
    server.signedOut()
    const { session } = await start()
    expect(session.isClosing(closing)).toBe(true)
    expect(session.isClosing(ana)).toBe(false)
  })
})

describe('coming back to the tab', () => {
  it('shows a sign-in made in another tab', async () => {
    server.signedOut()
    const { settled } = await start()
    await settled('signedOut')
    server.signedIn()
    window.dispatchEvent(new Event('focus'))
    await settled('signedIn')
  })

  it('shows a session that ended elsewhere, and a name or a state changed elsewhere', async () => {
    server.signedIn()
    const { seen, settled } = await start()
    await settled('signedIn')
    const before = seen.now.state
    // Nothing changed: the state is the same one, so nothing draws again.
    document.dispatchEvent(new Event('visibilitychange'))
    await waitFor(() => expect(server.asked('GET /api/v1/me')).toHaveLength(2))
    expect(seen.now.state).toBe(before)
    server.signedIn({ ...ana, name: 'Ana María' })
    window.dispatchEvent(new Event('focus'))
    await waitFor(() =>
      expect(seen.now.state).toMatchObject({ me: { account: { name: 'Ana María' } } }),
    )
    server.signedIn(closing)
    window.dispatchEvent(new Event('focus'))
    await waitFor(() =>
      expect(seen.now.state).toMatchObject({ me: { account: { state: 'closing' } } }),
    )
    server.on('GET /api/v1/me', refused(401, 'unauthorized'))
    window.dispatchEvent(new Event('focus'))
    await settled('signedOut')
    expect(seen.now.state).toEqual({ status: 'signedOut', byChoice: false })
  })

  it('signs nobody out when the server gives no answer', async () => {
    server.signedIn()
    const { seen, settled } = await start()
    await settled('signedIn')
    server.on('GET /api/v1/me', network)
    await act(() => seen.now.refresh())
    expect(seen.now.state.status).toBe('signedIn')
  })

  it('asks once at a time, and not while nobody knows who this is', async () => {
    const first = later()
    server.on('GET /api/v1/me', first.answer)
    const { seen, settled } = await start()
    void seen.now.refresh()
    expect(server.asked('GET /api/v1/me')).toHaveLength(1)
    first.give(ok(meOf()))
    await settled('signedIn')
    const second = later()
    server.on('GET /api/v1/me', second.answer)
    window.dispatchEvent(new Event('focus'))
    window.dispatchEvent(new Event('focus'))
    expect(server.asked('GET /api/v1/me')).toHaveLength(2)
    second.give(ok(meOf()))
    await waitFor(() => expect(seen.now.state.status).toBe('signedIn'))
  })

  it('does not put back what a change made here replaced meanwhile', async () => {
    server.signedIn()
    const { seen, settled } = await start()
    await settled('signedIn')
    const slow = later()
    server.on('GET /api/v1/me', slow.answer)
    const asking = seen.now.refresh()
    act(() => seen.now.accountChanged({ ...ana, name: 'Renamed here' }))
    slow.give(ok(meOf(ana)))
    await act(() => asking)
    expect(seen.now.state).toMatchObject({ me: { account: { name: 'Renamed here' } } })
  })
})

describe('what the session takes', () => {
  it('takes the account of a finished sign-in, and a change to it', async () => {
    server.signedOut()
    const { seen, settled } = await start()
    await settled('signedOut')
    act(() => seen.now.signedIn(meOf()))
    expect(seen.now.state.status).toBe('signedIn')
    expect(seen.now.recovered).toBe(false)
    act(() => seen.now.accountChanged({ ...ana, name: '' }))
    expect(seen.now.state).toMatchObject({ me: { account: { name: '' } } })
    // A change to another account is not this one's.
    act(() => seen.now.accountChanged({ ...ana, id: 'acc_other', name: 'Eve' }))
    expect(seen.now.state).toMatchObject({ me: { account: { id: 'acc_ana', name: '' } } })
  })

  it('remembers that a recovery code was used until Settings has said so, or the session ends', async () => {
    server.signedOut()
    const { seen, settled } = await start()
    await settled('signedOut')
    act(() => seen.now.signedIn(meOf(), 'recovery'))
    expect(seen.now.recovered).toBe(true)
    act(() => seen.now.clearRecovered())
    expect(seen.now.recovered).toBe(false)
    act(() => seen.now.signedIn(meOf(), 'recovery'))
    server.on('GET /api/v1/me', refused(401, 'unauthorized'))
    await act(() => seen.now.refresh())
    expect(seen.now.recovered).toBe(false)
  })

  it('signs out on the server, and takes no session there for done', async () => {
    server.signedIn().on('POST /api/v1/auth/sign-out', ok())
    let { seen, settled } = await start()
    await settled('signedIn')
    await act(() => seen.now.signOut())
    expect(seen.now.state).toEqual({ status: 'signedOut', byChoice: true })

    server.signedIn().on('POST /api/v1/auth/sign-out', refused(401, 'unauthorized'))
    ;({ seen, settled } = await start())
    await settled('signedIn')
    await act(() => seen.now.signOut())
    expect(seen.now.state).toEqual({ status: 'signedOut', byChoice: true })
  })

  it('stays signed in when signing out got no answer', async () => {
    server.signedIn().on('POST /api/v1/auth/sign-out', network)
    const { seen, settled, api } = await start()
    await settled('signedIn')
    await expect(act(() => seen.now.signOut())).rejects.toBeInstanceOf(api.ApiError)
    expect(seen.now.state.status).toBe('signedIn')
  })
})

describe('a request that failed', () => {
  it('is said where it failed, by its code', async () => {
    server.signedIn()
    const { seen, settled, api } = await start()
    await settled('signedIn')
    expect(seen.now.failed(new api.ApiError('network'))).toBe('network')
    expect(seen.now.failed(new api.ApiError('invalid_name'))).toBe('invalid_name')
    expect(seen.now.failed(new Error('boom'))).toBe('other')
    expect(server.asked('GET /api/v1/me')).toHaveLength(1)
  })

  it('says nothing when the session ended: the app asks who this is', async () => {
    server.signedIn()
    const { seen, settled, api } = await start()
    await settled('signedIn')
    server.on('GET /api/v1/me', refused(401, 'unauthorized'))
    let code: unknown
    act(() => {
      code = seen.now.failed(new api.ApiError('unauthorized'))
    })
    expect(code).toBeNull()
    await settled('signedOut')
  })

  it('says nothing when the account is closing: the request itself told the session', async () => {
    server.signedIn().on('PATCH /api/v1/account', refused(409, 'account_closing'))
    const { seen, settled, api } = await start()
    await settled('signedIn')
    server.signedIn(closing)
    let code: unknown = 'unset'
    await act(async () => {
      try {
        await api.renameAccount('Ana')
      } catch (error) {
        code = seen.now.failed(error)
      }
    })
    expect(code).toBeNull()
    await waitFor(() =>
      expect(seen.now.state).toMatchObject({ me: { account: { state: 'closing' } } }),
    )
    // Once it is known, another refusal asks nothing more.
    const asked = server.asked('GET /api/v1/me').length
    await act(async () => {
      await api.renameAccount('Ana').catch(() => {})
    })
    expect(server.asked('GET /api/v1/me')).toHaveLength(asked)
  })
})

describe('the day a closing account is removed', () => {
  it('is kept while the account is seen closing, and nothing else of it', async () => {
    server.signedIn(closing)
    const { settled } = await start()
    await settled('signedIn')
    await waitFor(() =>
      expect({ ...localStorage }).toEqual({ 'permaily-closes-at': '2026-11-18T14:32:00.000Z' }),
    )
  })

  it('is forgotten when an account that works is seen, or the deletion is cancelled', async () => {
    localStorage.setItem('permaily-closes-at', '2026-11-18T14:32:00.000Z')
    server.signedIn()
    let { seen, settled } = await start()
    await settled('signedIn')
    await waitFor(() => expect({ ...localStorage }).toEqual({}))

    server.signedIn(closing)
    ;({ seen, settled } = await start())
    await settled('signedIn')
    await waitFor(() => expect(localStorage.length).toBe(1))
    act(() => seen.now.accountChanged(ana))
    await waitFor(() => expect({ ...localStorage }).toEqual({}))
  })

  it('is said once when it has passed and nobody is signed in, and not before', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-17T10:00:00Z') })
    localStorage.setItem('permaily-closes-at', '2026-11-18T14:32:00.000Z')
    server.signedOut()
    let { seen, settled } = await start()
    await settled('signedOut')
    expect(seen.now.deletedOn).toBeNull()
    expect(localStorage.length).toBe(1)

    vi.setSystemTime(Date.parse('2026-11-19T10:00:00Z'))
    ;({ seen, settled } = await start())
    await settled('signedOut')
    await waitFor(() => expect(seen.now.deletedOn?.toISOString()).toBe('2026-11-18T14:32:00.000Z'))
    expect({ ...localStorage }).toEqual({})
    // Signing in takes it away, and a reload has nothing to say.
    act(() => seen.now.signedIn(meOf()))
    await waitFor(() => expect(seen.now.deletedOn).toBeNull())
    ;({ seen, settled } = await start())
    await settled('signedOut')
    expect(seen.now.deletedOn).toBeNull()
  })
})
