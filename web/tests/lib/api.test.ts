import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { meOf, network, ok, refused, server } from '../helpers/server.ts'

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
    'account_blocked',
    'address_in_use',
    'confirmation_needed',
    'passkey_required',
    'passkey_only_needed',
    'super_admin_only',
    'not_cancellable',
    'invalid_provider_app',
    'link_changed',
    'busy',
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

  it('asks to change the contact address, and takes the change only from an account', async () => {
    server.on('POST /api/v1/account/address/start', ok({ attempt: 'att_1', expires_in: 600 }))
    expect(await api.startAddressChange('ana@new.example')).toEqual({
      attempt: 'att_1',
      expires_in: 600,
    })
    server.on(
      'POST /api/v1/account/address/finish',
      ok({ ...account, address: 'ana@new.example', name: 'Ana' }),
    )
    expect(await api.finishAddressChange('att_1', '123456')).toMatchObject({
      id: 'acc_1',
      address: 'ana@new.example',
    })
    expect(server.calls.map((call) => call.body)).toEqual([
      { address: 'ana@new.example' },
      { attempt: 'att_1', code: '123456' },
    ])
    server.on('POST /api/v1/account/address/finish', ok({ address: 'ana@new.example' }))
    expect(await failure(api.finishAddressChange('att_1', '123456'))).toBe('other')
    server.on('POST /api/v1/account/address/finish', refused(409, 'address_in_use'))
    expect(await failure(api.finishAddressChange('att_1', '123456'))).toBe('address_in_use')
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

describe('confirming it is the person', () => {
  it('reads the ways the account has, in its order, without any it does not know', async () => {
    server.on(
      'GET /api/v1/auth/confirm',
      ok({ ways: ['email', 'carrier pigeon', 'passkey'], confirmed_until: '2026-11-12T10:10:00Z' }),
    )
    expect(await api.confirmWays()).toEqual({
      ways: ['passkey', 'email'],
      confirmed_until: '2026-11-12T10:10:00Z',
    })
    server.on('GET /api/v1/auth/confirm', ok({ ways: ['email'], confirmed_until: 7 }))
    expect(await api.confirmWays()).toEqual({ ways: ['email'] })
    server.on('GET /api/v1/auth/confirm', ok({ ways: 'email' }))
    expect(await failure(api.confirmWays())).toBe('other')
  })

  it('confirms with a passkey and with a code', async () => {
    server
      .on('POST /api/v1/auth/confirm/passkey/start', ok({ ceremony: 'cer_1', options: {} }))
      .on('POST /api/v1/auth/confirm/passkey/finish', ok(meOf()))
      .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on('POST /api/v1/auth/confirm/email/finish', ok(meOf()))
    expect(await api.startConfirmPasskey()).toEqual({ ceremony: 'cer_1', options: {} })
    await api.finishConfirmPasskey('cer_1', { id: 'cred' })
    expect(server.asked('POST /api/v1/auth/confirm/passkey/finish')[0].body).toEqual({
      ceremony: 'cer_1',
      credential: { id: 'cred' },
    })
    expect(await api.startConfirmEmail()).toEqual({ attempt: 'att_1', expires_in: 600 })
    expect(server.asked('POST /api/v1/auth/confirm/email/start')[0].body).toBeUndefined()
    await api.finishConfirmEmail('att_1', '123456')
    expect(server.asked('POST /api/v1/auth/confirm/email/finish')[0].body).toEqual({
      attempt: 'att_1',
      code: '123456',
    })
  })
})

describe('the admin API', () => {
  it('reads a page of accounts, and fills in what an answer leaves out', async () => {
    server
      .on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 2, next: 'cur_2' }))
      .on('GET /api/v1/admin/accounts?limit=10&after=cur_2', ok({ accounts: [], next: '' }))
      .on('GET /api/v1/admin/accounts?address=a+b%40example.org', ok({ accounts: null }))
    expect(await api.adminAccounts()).toEqual({ accounts: [], unreadable: 2, next: 'cur_2' })
    expect(await api.adminAccounts('cur_2', 10)).toEqual({ accounts: [], unreadable: 0 })
    expect(await failure(api.adminFindAccount('a b@example.org'))).toBe('other')
  })

  it('names an account in the path, whatever its id holds', async () => {
    server
      .on('GET /api/v1/admin/accounts/a%2Fb', ok({ id: 'a/b' }))
      .on('POST /api/v1/admin/accounts/a%2Fb/delete/cancel', ok({ id: 'a/b' }))
    expect(await api.adminAccount('a/b')).toEqual({ id: 'a/b' })
    expect(await api.adminAccountAction('a/b', 'delete/cancel')).toEqual({ id: 'a/b' })
  })

  it('reads invitations, and the codes only once they are made', async () => {
    server.on('GET /api/v1/admin/invitations', ok({ invitations: [], truncated: 'yes' }))
    expect(await api.adminInvitations()).toEqual({ invitations: [], truncated: false })
    server.on('GET /api/v1/admin/invitations', ok({}))
    expect(await failure(api.adminInvitations())).toBe('other')
    server.on('POST /api/v1/admin/invitations', ok({ invitations: ['A'] }))
    expect(await api.makeInvitations(1)).toEqual({ invitations: ['A'], ids: [], expires_at: '' })
    expect(server.asked('POST /api/v1/admin/invitations')[0].body).toEqual({ count: 1 })
    server.on('POST /api/v1/admin/invitations', ok({ invitations: [] }))
    expect(await failure(api.makeInvitations(1))).toBe('other')
    server.on('POST /api/v1/admin/invitations', ok({}))
    expect(await failure(api.makeInvitations(1))).toBe('other')
    server.on('DELETE /api/v1/admin/invitations/inv%2F1', ok())
    expect(await api.revokeInvitation('inv/1')).toBeUndefined()
  })

  it('reads the providers set, without any it does not know, and never a secret back', async () => {
    server.on(
      'GET /api/v1/admin/providers',
      ok({
        providers: [
          { provider: 'google', client_id: 'g' },
          { provider: 'myspace', client_id: 'm' },
          { provider: 'github' },
          null,
        ],
      }),
    )
    expect(await api.providerApps()).toEqual([{ provider: 'google', client_id: 'g' }])
    server.on('GET /api/v1/admin/providers', ok({}))
    expect(await failure(api.providerApps())).toBe('other')
    server
      .on('PUT /api/v1/admin/providers/meta', ok())
      .on('DELETE /api/v1/admin/providers/meta', ok())
    await api.setProviderApp('meta', 'id', 'secret')
    expect(server.asked('PUT /api/v1/admin/providers/meta')[0].body).toEqual({
      client_id: 'id',
      client_secret: 'secret',
    })
    await api.removeProviderApp('meta')
  })

  it('reads, makes and removes admins', async () => {
    server.on('GET /api/v1/admin/admins', ok({ admins: [] }))
    expect(await api.admins()).toEqual([])
    server.on('GET /api/v1/admin/admins', ok({}))
    expect(await failure(api.admins())).toBe('other')
    // A change answers the admins as they are after it.
    const after = [{ id: 'acc/1', address: 'a@example.org', name: '', super: false }]
    server
      .on('PUT /api/v1/admin/admins/acc%2F1', ok({ admins: after }))
      .on('DELETE /api/v1/admin/admins/acc%2F1', ok({ admins: [] }))
    expect(await api.makeAdmin('acc/1')).toEqual(after)
    expect(server.asked('PUT /api/v1/admin/admins/acc%2F1')[0].body).toBeUndefined()
    expect(await api.removeAdmin('acc/1')).toEqual([])
    server.on('DELETE /api/v1/admin/admins/acc%2F1', ok())
    expect(await failure(api.removeAdmin('acc/1'))).toBe('other')
  })

  it('reads the log a page at a time', async () => {
    server
      .on('GET /api/v1/admin/log?limit=50', ok({ entries: [], next: 'cur_2' }))
      .on('GET /api/v1/admin/log?limit=5&after=cur_2', ok({ entries: [], next: '' }))
    expect(await api.adminLog()).toEqual({ entries: [], next: 'cur_2' })
    expect(await api.adminLog('cur_2', 5)).toEqual({ entries: [] })
    server.on('GET /api/v1/admin/log?limit=50', ok({}))
    expect(await failure(api.adminLog())).toBe('other')
  })

  it('reads the numbers, taking them as complete unless the server says otherwise', async () => {
    const accounts = { active: 1, closing: 0, blocked: 0, total: 1 }
    server.on('GET /api/v1/admin/stats', ok({ accounts }))
    expect(await api.adminStats()).toEqual({ accounts, invitations: { unused: 0 }, complete: true })
    server.on(
      'GET /api/v1/admin/stats',
      ok({ accounts, invitations: { unused: 3 }, complete: false }),
    )
    expect(await api.adminStats()).toEqual({
      accounts,
      invitations: { unused: 3 },
      complete: false,
    })
    server.on('GET /api/v1/admin/stats', ok({ accounts: null }))
    expect(await failure(api.adminStats())).toBe('other')
  })
})

describe('the token of a link of a message', () => {
  it('is a number below 1,000 without leading zeros, a dot, and 16 base64url characters', () => {
    for (const ok of ['0.AAECAwQFBgcICQoL', '999.-_-_-_-_-_-_-_-_', '12.abcdefghijklmnop']) {
      expect(api.isMessageLinkToken(ok)).toBe(true)
    }
    for (const bad of [
      '',
      '0',
      '01.AAECAwQFBgcICQoL',
      '1000.AAECAwQFBgcICQoL',
      '1.AAECAwQFBgcICQo',
      '1.AAECAwQFBgcICQoLM',
      '1.AAECAwQFBgcICQo=',
      '1/AAECAwQFBgcICQoL',
      '1.AAECAwQFBgcICQoL\n',
    ]) {
      expect(api.isMessageLinkToken(bad)).toBe(false)
    }
  })
})
