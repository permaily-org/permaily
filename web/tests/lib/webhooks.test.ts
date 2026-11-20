import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { messageFor } from '../../src/pages/auth/messages.ts'
import { connectionOf } from '../helpers/connections.ts'
import { ok, refused, server } from '../helpers/server.ts'

// K56 (ADR 0039, pull request 5): the web app's shapes of a webhook are the contract's. Each value below is typed
// with api.ts's type, every member set (`Required`), so the type checker refuses a member the type lacks and one it
// has that is left out; its JSON is the contract's JSON byte for byte, as internal/contracts' TestWebhookShapes writes
// it (the same values, in Go's member order).

const weekly = { every: 'week', on: 'monday', at: '08:00' } satisfies Required<api.WebhookSchedule>
const at = '2026-11-20T08:00:00Z'

describe('K56: the shapes of a webhook are the contract’s', () => {
  it('writes what the contract writes', () => {
    const cases: [string, unknown, string][] = [
      [
        'every day',
        { every: 'day', at: '17:45' } satisfies api.WebhookSchedule,
        '{"every":"day","at":"17:45"}',
      ],
      [
        'what the screen shows',
        { host: 'trip-app.com', new_mail: true } satisfies api.WebhookAsked,
        '{"host":"trip-app.com","new_mail":true}',
      ],
      [
        'what the person sees, failing',
        {
          host: 'trip-app.com',
          schedule: weekly,
          time_zone: 'Europe/Madrid',
          state: 'on',
          next_at: at,
          last_sent_at: at,
          failing_since: at,
          failure: 'no_answer',
        } satisfies Omit<Required<api.ConnectionWebhook>, 'new_mail'>,
        '{"host":"trip-app.com","schedule":{"every":"week","on":"monday","at":"08:00"},"time_zone":"Europe/Madrid","state":"on","next_at":"2026-11-20T08:00:00Z","last_sent_at":"2026-11-20T08:00:00Z","failing_since":"2026-11-20T08:00:00Z","failure":"no_answer"}',
      ],
      [
        'turned off',
        { host: 'trip-app.com', new_mail: true, state: 'off' } satisfies api.ConnectionWebhook,
        '{"host":"trip-app.com","new_mail":true,"state":"off"}',
      ],
      [
        'an approval in a time zone',
        { allow: true, time_zone: 'America/New_York' } satisfies api.AuthorizationDecision,
        '{"allow":true,"time_zone":"America/New_York"}',
      ],
    ]
    for (const [name, value, json] of cases) expect(JSON.stringify(value), name).toBe(json)
  })

  it('names every member of the contract, and no other', () => {
    // Every member api.ts has, set: the type checker holds this list to the type.
    const webhook: Required<api.ConnectionWebhook> = {
      host: 'trip-app.com',
      new_mail: true,
      schedule: weekly,
      time_zone: 'Europe/Madrid',
      state: 'stopped',
      next_at: at,
      last_sent_at: at,
      failing_since: at,
      failure: 'refused',
    }
    // ConnectionWebhook's json tags in internal/contracts/webhooks.go, in order.
    expect(Object.keys(webhook)).toEqual([
      'host',
      'new_mail',
      'schedule',
      'time_zone',
      'state',
      'next_at',
      'last_sent_at',
      'failing_since',
      'failure',
    ])
    const asked: Required<api.WebhookAsked> = { host: 'h', new_mail: true, schedule: weekly }
    expect(Object.keys(asked)).toEqual(['host', 'new_mail', 'schedule'])
    expect(Object.keys(weekly)).toEqual(['every', 'on', 'at'])
    // The fixed words the contract has, each of them.
    const states: Record<api.WebhookState, true> = { on: true, off: true, stopped: true }
    const failures: Record<api.WebhookFailure, true> = {
      no_answer: true,
      error: true,
      redirect: true,
      refused: true,
    }
    const every: Record<api.WebhookEvery, true> = { day: true, week: true }
    expect(Object.keys(states)).toEqual(['on', 'off', 'stopped'])
    expect(Object.keys(failures)).toEqual(['no_answer', 'error', 'redirect', 'refused'])
    expect(Object.keys(every)).toEqual(['day', 'week'])
  })

  it('reads a connection’s webhook as the server answers it', async () => {
    const webhook = {
      host: 'trip-app.com',
      schedule: weekly,
      time_zone: 'Europe/Madrid',
      state: 'on',
      next_at: at,
    }
    server.on('GET /api/v1/connections/con_claudecode', ok(connectionOf({ webhook })))
    expect((await api.connection('con_claudecode')).webhook).toEqual(webhook)
    server.on('GET /api/v1/connections/con_claudecode', ok(connectionOf()))
    expect((await api.connection('con_claudecode')).webhook).toBeUndefined()
  })
})

describe('turning a webhook off', () => {
  it('posts the version shown to its route, and reads the connection answered', async () => {
    const off = connectionOf({
      webhook: { host: 'trip-app.com', new_mail: true, state: 'off' },
      version: 8,
    })
    server.on('POST /api/v1/connections/a%2Fb%3F/webhook/off', ok(off))
    const answer = await api.turnWebhookOff('a/b?', 7)
    expect(answer.webhook?.state).toBe('off')
    expect(answer.version).toBe(8)
    expect(server.calls.map((c) => [c.method, c.path, c.body])).toEqual([
      ['POST', '/api/v1/connections/a%2Fb%3F/webhook/off', { version: 7 }],
    ])
  })

  it('answers the server’s refusals by their codes', async () => {
    for (const [status, code] of [
      [409, 'consent_changed'],
      [409, 'connection_ended'],
      [404, 'not_found'],
    ] as const) {
      server.on('POST /api/v1/connections/con_1/webhook/off', refused(status, code))
      await expect(api.turnWebhookOff('con_1', 1)).rejects.toMatchObject({ code })
    }
    server.on('POST /api/v1/connections/con_1/webhook/off', ok(null))
    await expect(api.turnWebhookOff('con_1', 1)).rejects.toMatchObject({ code: 'other' })
  })

  it('knows the code of a zone the server does not know, with its words', async () => {
    server.on('POST /api/v1/oauth/requests/pmq_1/decision', refused(400, 'time_zone_invalid'))
    await expect(
      api.decideAuthorization('pmq_1', { allow: true, time_zone: 'Mars/Olympus' }),
    ).rejects.toMatchObject({ code: 'time_zone_invalid' })
    expect(messageFor('time_zone_invalid')).toBe(
      "That time zone isn't one Permaily knows. Choose another, then allow again.",
    )
  })
})
