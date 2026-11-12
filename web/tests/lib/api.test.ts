import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { network, ok, refused, server } from '../helpers/server.ts'

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

describe('a request', () => {
  it('is JSON, to the same origin, with nothing cached and a time limit', async () => {
    const fetched = vi.fn(server.fetch)
    vi.stubGlobal('fetch', fetched)
    server.on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
    await api.startEmail('ana@example.org')
    const [path, init] = fetched.mock.calls[0]
    expect(path).toBe('/api/v1/auth/email/start')
    expect(init).toMatchObject({
      method: 'POST',
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: '{"address":"ana@example.org"}',
    })
    expect(init?.signal).toBeInstanceOf(AbortSignal)
  })

  it('sends no body and no content type when there is nothing to send', async () => {
    const fetched = vi.fn(server.fetch)
    vi.stubGlobal('fetch', fetched)
    server.signedOut()
    await failure(api.me())
    expect(fetched.mock.calls[0][1]).toMatchObject({ method: 'GET', body: undefined })
    expect(fetched.mock.calls[0][1]?.headers).toEqual({ Accept: 'application/json' })
  })

  it('answers nothing for a 204', async () => {
    server.on('POST /api/v1/auth/sign-out', ok())
    expect(await api.signOut()).toBeUndefined()
  })
})

describe('how a failure reads', () => {
  it.each([
    'invalid_address',
    'invalid_invitation',
    'invalid_code',
    'too_many_requests',
    'unauthorized',
    'not_found',
    'invalid_passkey',
    'invalid_name',
    'too_many_passkeys',
    'last_passkey',
    'passkey_needed',
    'passkey_only_off',
    'account_closing',
    'provider_off',
    'provider_failed',
    'provider_unavailable',
    'provider_in_use',
    'provider_linked',
    'invitation_needed',
    'last_way_in',
  ])('keeps the code %s the server answered', async (code) => {
    server.on('GET /api/v1/me', refused(400, code))
    expect(await failure(api.me())).toBe(code)
  })

  it.each([
    [401, 'unauthorized'],
    [404, 'not_found'],
    [429, 'too_many_requests'],
    [400, 'other'],
    [500, 'other'],
    [502, 'other'],
  ])('reads a %i with a code it does not know by its status: %s', async (status, code) => {
    server.on('GET /api/v1/me', refused(status, 'something_new'))
    expect(await failure(api.me())).toBe(code)
  })

  it('reads an answer that is not the API own, such as a proxy page, by its status', async () => {
    vi.stubGlobal('fetch', async () => new Response('<html>Bad gateway</html>', { status: 401 }))
    expect(await failure(api.me())).toBe('unauthorized')
    vi.stubGlobal('fetch', async () => new Response('<html>Bad gateway</html>', { status: 502 }))
    expect(await failure(api.me())).toBe('other')
    vi.stubGlobal('fetch', async () => new Response('null', { status: 500 }))
    expect(await failure(api.me())).toBe('other')
  })

  it('reads no answer as network', async () => {
    server.on('GET /api/v1/me', network)
    expect(await failure(api.me())).toBe('network')
  })

  it('reads a good answer that is not JSON as a failure, not as an account', async () => {
    vi.stubGlobal('fetch', async () => new Response('<html>', { status: 200 }))
    expect(await failure(api.me())).toBe('other')
  })

  it('reads anything that is not a failure of the API as other', () => {
    expect(api.errorCode(new Error('boom'))).toBe('other')
    expect(api.errorCode('invalid_code')).toBe('other')
    expect(api.errorCode(new api.ApiError('invalid_code'))).toBe('invalid_code')
    expect(new api.ApiError('network').name).toBe('ApiError')
  })

  it('tells whoever listens that the account is closing, whatever request found out', async () => {
    const heard = vi.fn()
    api.onAccountClosing(heard)
    server.on('PATCH /api/v1/account', refused(409, 'account_closing'))
    expect(await failure(api.renameAccount('Ana'))).toBe('account_closing')
    expect(heard).toHaveBeenCalledTimes(1)
    server.on('PATCH /api/v1/account', refused(409, 'invalid_name'))
    await failure(api.renameAccount('Ana'))
    expect(heard).toHaveBeenCalledTimes(1)
    api.onAccountClosing(null)
    server.on('PATCH /api/v1/account', refused(409, 'account_closing'))
    expect(await failure(api.renameAccount('Ana'))).toBe('account_closing')
    expect(heard).toHaveBeenCalledTimes(1)
  })
})

describe('signing in by mail', () => {
  beforeEach(() => {
    server.on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
  })

  it('sends the invitation only when there is one', async () => {
    await api.startEmail('ana@example.org')
    await api.startEmail('ana@example.org', 'abcd2345efgh')
    expect(server.calls.map((call) => call.body)).toEqual([
      { address: 'ana@example.org' },
      { address: 'ana@example.org', invitation: 'abcd2345efgh' },
    ])
  })

  it('asks whose sign-in a link is, and takes only an address for an answer', async () => {
    server.on('POST /api/v1/auth/email/look', ok({ address: 'ana@example.org' }))
    expect(await api.lookEmail('att_1', 'tok')).toEqual({ address: 'ana@example.org' })
    expect(server.calls[0].body).toEqual({ attempt: 'att_1', token: 'tok' })
    for (const answer of [{}, { address: '' }, { address: 7 }, null]) {
      server.on('POST /api/v1/auth/email/look', ok(answer))
      expect(await failure(api.lookEmail('att_1', 'tok'))).toBe('other')
    }
  })

  it('ends a sign-in with the code or with the token', async () => {
    server.on('POST /api/v1/auth/email/finish', ok({ account: {}, session: {} }))
    await api.finishEmail('att_1', { code: '123456' })
    await api.finishEmail('att_1', { token: 'tok' })
    expect(server.calls.map((call) => call.body)).toEqual([
      { attempt: 'att_1', code: '123456' },
      { attempt: 'att_1', token: 'tok' },
    ])
  })
})

describe('the account', () => {
  const account = { id: 'acc_1', address: 'ana@example.org', state: 'active' }

  it('takes a change only from an answer that is an account', async () => {
    server.on('PATCH /api/v1/account', ok({ ...account, name: 'Ana' }))
    expect(await api.renameAccount('Ana')).toMatchObject({ id: 'acc_1', name: 'Ana' })
    expect(server.calls[0].body).toEqual({ name: 'Ana' })
    // No name in the answer is no name.
    server.on('PATCH /api/v1/account', ok(account))
    expect((await api.renameAccount('')).name).toBe('')
    for (const answer of [null, {}, { id: 'acc_1' }, { address: 'ana@example.org' }]) {
      server.on('PATCH /api/v1/account', ok(answer))
      expect(await failure(api.renameAccount('Ana'))).toBe('other')
    }
  })

  it('asks to delete and to cancel without a body', async () => {
    server
      .on('POST /api/v1/account/delete', ok({ ...account, state: 'closing' }))
      .on('POST /api/v1/account/delete/cancel', ok(account))
    expect((await api.deleteAccount()).state).toBe('closing')
    expect((await api.cancelDeletion()).state).toBe('active')
    expect(server.calls.map((call) => call.body)).toEqual([undefined, undefined])
  })
})

describe('passkeys, recovery codes and sessions', () => {
  it('reads the list, and fills in what an answer leaves out', async () => {
    server.on('GET /api/v1/passkeys', ok({ passkeys: [] }))
    expect(await api.passkeys()).toEqual({
      passkeys: [],
      passkey_only: false,
      recovery_codes_left: 0,
    })
    server.on(
      'GET /api/v1/passkeys',
      ok({ passkeys: [{ id: 'a' }], passkey_only: true, recovery_codes_left: 7 }),
    )
    expect(await api.passkeys()).toMatchObject({ passkey_only: true, recovery_codes_left: 7 })
    for (const answer of [null, {}, { passkeys: 'none' }]) {
      server.on('GET /api/v1/passkeys', ok(answer))
      expect(await failure(api.passkeys())).toBe('other')
    }
  })

  it('names a passkey in the path, whatever its identifier holds', async () => {
    server
      .on('PATCH /api/v1/passkeys/a%2Fb%3Fc', ok({ id: 'a/b?c', name: 'Key' }))
      .on('DELETE /api/v1/passkeys/a%2Fb%3Fc', ok())
      .on('DELETE /api/v1/sessions/ses%2F1', ok())
      .on('DELETE /api/v1/providers/meta', ok())
    await api.renamePasskey('a/b?c', 'Key')
    await api.removePasskey('a/b?c')
    await api.closeSession('ses/1')
    await api.unlinkProvider('meta')
    expect(server.calls[0].body).toEqual({ name: 'Key' })
  })

  it('makes and uses a passkey with the ceremony the server named', async () => {
    server
      .on('POST /api/v1/passkeys/start', ok({ ceremony: 'cer_1', options: {} }))
      .on('POST /api/v1/passkeys/finish', ok({ id: 'a', name: 'Key' }))
      .on('POST /api/v1/auth/passkey/start', ok({ ceremony: 'cer_2', options: {} }))
      .on('POST /api/v1/auth/passkey/finish', ok({ account: {}, session: {} }))
      .on('POST /api/v1/auth/recovery', ok({ account: {}, session: {} }))
    expect((await api.startPasskey()).ceremony).toBe('cer_1')
    await api.finishPasskey('cer_1', 'Key', { id: 'a' })
    expect((await api.startPasskeySignIn()).ceremony).toBe('cer_2')
    await api.finishPasskeySignIn('cer_2', { id: 'a' })
    await api.recover('ana@example.org', 'abcd2345efgh6789')
    expect(server.calls.map((call) => call.body)).toEqual([
      undefined,
      { ceremony: 'cer_1', name: 'Key', credential: { id: 'a' } },
      undefined,
      { ceremony: 'cer_2', credential: { id: 'a' } },
      { address: 'ana@example.org', code: 'abcd2345efgh6789' },
    ])
  })

  it('reads recovery codes as text and nothing else', async () => {
    server.on(
      'POST /api/v1/account/passkey-only',
      ok({ passkey_only: true, recovery_codes: ['aaaa', 7, null, 'bbbb'] }),
    )
    expect(await api.setPasskeyOnly(true)).toEqual({
      passkey_only: true,
      recovery_codes: ['aaaa', 'bbbb'],
    })
    expect(server.calls[0].body).toEqual({ on: true })
    server.on('POST /api/v1/account/passkey-only', ok({ passkey_only: false }))
    expect(await api.setPasskeyOnly(false)).toEqual({ passkey_only: false, recovery_codes: [] })
    server.on('POST /api/v1/account/passkey-only', ok(null))
    expect(await api.setPasskeyOnly(false)).toEqual({ passkey_only: false, recovery_codes: [] })

    server.on('POST /api/v1/account/recovery-codes', ok({ recovery_codes: ['aaaa'] }))
    expect(await api.newRecoveryCodes()).toEqual(['aaaa'])
    // New codes that are not there are a failure: the old ones stopped working.
    for (const answer of [{}, { recovery_codes: [] }, { recovery_codes: 'aaaa' }, null]) {
      server.on('POST /api/v1/account/recovery-codes', ok(answer))
      expect(await failure(api.newRecoveryCodes())).toBe('other')
    }
  })

  it('reads the sessions', async () => {
    server.on('GET /api/v1/sessions', ok({ sessions: [{ id: 'ses_1' }] }))
    expect(await api.sessions()).toEqual([{ id: 'ses_1' }])
    for (const answer of [null, {}, { sessions: {} }]) {
      server.on('GET /api/v1/sessions', ok(answer))
      expect(await failure(api.sessions())).toBe('other')
    }
  })
})

describe('providers', () => {
  it('lists the ones that are on in the fixed order, without any it does not know', async () => {
    server.on(
      'GET /api/v1/auth/providers',
      ok({ providers: ['microsoft', 'apple', 'google', 7, 'meta'] }),
    )
    expect(await api.providersOn()).toEqual(['google', 'meta', 'microsoft'])
    for (const answer of [null, {}, { providers: 'google' }]) {
      server.on('GET /api/v1/auth/providers', ok(answer))
      expect(await failure(api.providersOn())).toBe('other')
    }
  })

  it('starts a sign-in with what there is to say, and needs an address to go to', async () => {
    server.on('POST /api/v1/auth/provider/start', ok({ url: 'https://idp.example/a' }))
    expect(await api.startProvider('google')).toEqual({
      url: 'https://idp.example/a',
      expires_in: 0,
    })
    await api.startProvider('meta', { invitation: 'abcd2345efgh', returnTo: '/rules' })
    await api.startProvider('meta', { invitation: '', returnTo: '' })
    expect(server.calls.map((call) => call.body)).toEqual([
      { provider: 'google' },
      { provider: 'meta', invitation: 'abcd2345efgh', return_to: '/rules' },
      { provider: 'meta' },
    ])
    for (const answer of [null, {}, { url: '' }, { url: 7 }]) {
      server.on('POST /api/v1/auth/provider/start', ok(answer))
      expect(await failure(api.startProvider('google'))).toBe('other')
    }
  })

  it('reads what waits for an address, only for a provider it knows', async () => {
    server.on('GET /api/v1/auth/provider/pending', ok({ provider: 'meta' }))
    expect(await api.pendingProvider()).toEqual({
      provider: 'meta',
      address: '',
      return_to: '/',
      expires_in: 0,
    })
    server.on(
      'GET /api/v1/auth/provider/pending',
      ok({ provider: 'github', address: 'ana@example.org', return_to: '/rules', expires_in: 60 }),
    )
    expect(await api.pendingProvider()).toEqual({
      provider: 'github',
      address: 'ana@example.org',
      return_to: '/rules',
      expires_in: 60,
    })
    for (const answer of [null, {}, { provider: 'apple' }]) {
      server.on('GET /api/v1/auth/provider/pending', ok(answer))
      expect(await failure(api.pendingProvider())).toBe('other')
    }
  })

  it('confirms an address for the sign-in that waits', async () => {
    server
      .on('POST /api/v1/auth/provider/confirm/start', ok({ attempt: 'att_2', expires_in: 600 }))
      .on('POST /api/v1/auth/provider/confirm/finish', ok({ account: {}, session: {} }))
    await api.startProviderConfirm('ana@example.org')
    await api.finishProviderConfirm('att_2', '123456')
    expect(server.calls.map((call) => call.body)).toEqual([
      { address: 'ana@example.org' },
      { attempt: 'att_2', code: '123456' },
    ])
  })

  it('reads the linked ones, without any that is not one', async () => {
    server.on(
      'GET /api/v1/providers',
      ok({
        providers: [
          { provider: 'google', linked_at: '2026-03-06T10:00:00Z' },
          { provider: 'apple', linked_at: '2026-03-06T10:00:00Z' },
          { provider: 'meta' },
          null,
        ],
      }),
    )
    expect(await api.linkedProviders()).toEqual([
      { provider: 'google', linked_at: '2026-03-06T10:00:00Z' },
    ])
    for (const answer of [null, {}, { providers: 'google' }]) {
      server.on('GET /api/v1/providers', ok(answer))
      expect(await failure(api.linkedProviders())).toBe('other')
    }
  })

  it('starts linking, with where to come back to when it is said', async () => {
    server.on('POST /api/v1/providers/start', ok({ url: 'https://idp.example/a', expires_in: 600 }))
    expect(await api.linkProvider('google', '/settings#linked=google')).toEqual({
      url: 'https://idp.example/a',
      expires_in: 600,
    })
    await api.linkProvider('google')
    expect(server.calls.map((call) => call.body)).toEqual([
      { provider: 'google', return_to: '/settings#linked=google' },
      { provider: 'google' },
    ])
  })
})
