import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { messageFor } from '../../src/pages/auth/messages.ts'
import {
  allowedRedirect,
  connectionOf,
  previewOf,
  requestOf,
  snapshotOf,
  summary,
} from '../helpers/connections.ts'
import { ok, refused, server } from '../helpers/server.ts'

/** The code a request fails with. */
async function failure(request: Promise<unknown>): Promise<string> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return api.errorCode(error)
  }
  throw new Error('the request did not fail')
}

// Every refusal the consent screen and the connections page handle is a code with its words (ADR 0030).
const refusals: readonly api.ApiErrorCode[] = api.CONSENT_REFUSALS

describe('the consent screen’s calls', () => {
  it('hands the vendor’s query string to the server as it was, and reads a request or a redirect', async () => {
    server.on('POST /api/v1/oauth/requests', ok({ request: requestOf() }))
    const started = await api.startAuthorization('a=1&b=2')
    expect(server.asked('POST /api/v1/oauth/requests')[0].body).toEqual({ query: 'a=1&b=2' })
    expect(started.request?.id).toBe(requestOf().id)
    expect(started.redirect).toBeUndefined()

    server.on(
      'POST /api/v1/oauth/requests',
      ok({ redirect: allowedRedirect, request: requestOf() }),
    )
    expect(await api.startAuthorization('a=1')).toEqual({ redirect: allowedRedirect })

    server.on('POST /api/v1/oauth/requests', ok({ redirect: '' }))
    expect(await failure(api.startAuthorization('a=1'))).toBe('other')
    server.on('POST /api/v1/oauth/requests', ok({ request: { id: 'x' } }))
    expect(await failure(api.startAuthorization('a=1'))).toBe('other')
    server.on('POST /api/v1/oauth/requests', ok(null))
    expect(await failure(api.startAuthorization('a=1'))).toBe('other')
  })

  it('names the request in the path, whatever its identifier holds', async () => {
    server.on('POST /api/v1/oauth/requests/a%2Fb%3F/matches', ok(previewOf()))
    server.on('POST /api/v1/oauth/requests/a%2Fb%3F/decision', ok({ redirect: allowedRedirect }))
    await api.previewConsent('a/b?', {
      draft: { mailboxes: ['m'], since: { new: true } },
      mailbox: 'm',
    })
    await api.decideAuthorization('a/b?', { allow: false })
    expect(server.calls.map((c) => c.path)).toEqual([
      '/api/v1/oauth/requests/a%2Fb%3F/matches',
      '/api/v1/oauth/requests/a%2Fb%3F/decision',
    ])
  })

  it('reads a preview’s page whole, and the change from its snapshot when it says none', async () => {
    const route = 'POST /api/v1/oauth/requests/r/matches'
    server.on(
      route,
      ok(previewOf([summary(1)], { next: 'n', held: ['h'], model: 'asked', needs_model: ['x'] })),
    )
    const page = await api.previewConsent('r', {
      draft: { mailboxes: ['m'], since: { new: false } },
      draft_id: 'd',
      mailbox: 'm',
      after: 'a',
      ask_model: true,
    })
    expect(server.asked(route)[0].body).toEqual({
      draft: { mailboxes: ['m'], since: { new: false } },
      draft_id: 'd',
      mailbox: 'm',
      after: 'a',
      ask_model: true,
    })
    expect(page.page).toMatchObject({ next: 'n', held: ['h'], model: 'asked', needs_model: ['x'] })

    server.on(
      route,
      ok({ draft_id: 'd', snapshot: snapshotOf({ change: 'replace' }), page: { messages: [] } }),
    )
    const bare = await api.previewConsent('r', {
      draft: { mailboxes: ['m'], since: { new: true } },
      mailbox: 'm',
    })
    expect(bare.change).toBe('replace')
    expect(bare.page).toEqual({ messages: [], needs_model: [], unknown: [] })

    server.on(
      route,
      ok({ draft_id: 'd', snapshot: snapshotOf(), page: { messages: [], model: 'weird' } }),
    )
    expect(
      (
        await api.previewConsent('r', {
          draft: { mailboxes: ['m'], since: { new: true } },
          mailbox: 'm',
        })
      ).page.model,
    ).toBeUndefined()

    server.on(route, ok({ draft_id: 'd', page: { messages: [] } }))
    expect(
      await failure(
        api.previewConsent('r', {
          draft: { mailboxes: ['m'], since: { new: true } },
          mailbox: 'm',
        }),
      ),
    ).toBe('other')
    server.on(route, ok({ draft_id: 'd', snapshot: snapshotOf() }))
    expect(
      await failure(
        api.previewConsent('r', {
          draft: { mailboxes: ['m'], since: { new: true } },
          mailbox: 'm',
        }),
      ),
    ).toBe('other')
  })

  it('sends the decision as given, and reads only an address back', async () => {
    const route = 'POST /api/v1/oauth/requests/r/decision'
    server.on(route, ok({ redirect: allowedRedirect }))
    const decision: api.AuthorizationDecision = {
      allow: true,
      draft: { mailboxes: ['m'], since: { new: true } },
      model_notice: 'v',
      snapshot: snapshotOf() as api.ConsentSnapshot,
    }
    expect(await api.decideAuthorization('r', decision)).toEqual({ redirect: allowedRedirect })
    expect(server.asked(route)[0].body).toEqual(decision)
    server.on(route, ok({ redirect: '' }))
    expect(await failure(api.decideAuthorization('r', { allow: false }))).toBe('other')
  })

  it('knows every refusal of a connection, each with its own words', async () => {
    for (const code of [
      ...refusals,
      'invalid_client',
      'vendor_unreachable',
      'request_too_large',
      'invalid_cursor',
    ] as const) {
      server.on('POST /api/v1/oauth/requests', refused(409, code))
      expect(await failure(api.startAuthorization('a=1'))).toBe(code)
      expect(messageFor(code)).not.toBe(messageFor('other'))
    }
  })
})

describe('the person’s connections', () => {
  it('reads the list and one connection, filling in what an answer leaves out', async () => {
    const bare = { id: 'con_x', vendor: { domain: 'x.example' }, devices: [{ id: 'd' }] }
    server.on('GET /api/v1/connections', ok({ connections: [connectionOf(), bare] }))
    const { connections } = await api.connections()
    expect(connections[0]).toEqual(connectionOf())
    expect(connections[1]).toMatchObject({
      parts: [],
      mailboxes: [],
      devices: [{ id: 'd', parts: [] }],
      delivery: 'delivering',
    })
    server.on('GET /api/v1/connections/con%2Fx', ok(bare))
    expect((await api.connection('con/x')).id).toBe('con_x')

    server.on('GET /api/v1/connections', ok({}))
    expect(await failure(api.connections())).toBe('other')
    server.on('GET /api/v1/connections/y', ok({ id: 'y' }))
    expect(await failure(api.connection('y'))).toBe('other')
    server.on('GET /api/v1/connections/z', ok(null))
    expect(await failure(api.connection('z'))).toBe('other')
  })

  it('revokes a connection, a device of one, and renews one with the notice and the version shown', async () => {
    const c = connectionOf()
    server
      .on('POST /api/v1/connections/con_claudecode/revoke', ok(c))
      .on('POST /api/v1/connections/con_claudecode/devices/dev%2Fone/revoke', ok(c))
      .on('POST /api/v1/connections/con_claudecode/renew', ok(c))
    await api.revokeConnection('con_claudecode')
    await api.revokeDevice('con_claudecode', 'dev/one')
    await api.renewConnection('con_claudecode', { model_notice: 'v', version: 7 })
    expect(server.asked('POST /api/v1/connections/con_claudecode/revoke')[0].body).toBeUndefined()
    expect(server.asked('POST /api/v1/connections/con_claudecode/renew')[0].body).toEqual({
      model_notice: 'v',
      version: 7,
    })
  })
})
