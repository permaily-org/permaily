import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { MODEL_NOTICE_VERSION, modelNotice, modelNoticeTitle } from '../../src/lib/model-notice.ts'
import { ok, server } from '../helpers/server.ts'

// The model's calls (ADR 0028, pull request 4: A28, A29): asking it from a page of matches, agreeing to its
// notice, and the notice the app shows, whose version is the server's.

const formula = {
  v: 1,
  all: [{ any: [{ var: 'message', op: 'about', value: 'invoices' }] }],
} satisfies api.QueryFormula

const account = {
  id: 'acc_x',
  state: 'active',
  address: 'ann@example.org',
  name: '',
  language: 'en',
  created_at: '2026-11-15T10:00:00Z',
  terms: '2026-11-12',
  model_notice: MODEL_NOTICE_VERSION,
} satisfies api.Account

async function failure(request: Promise<unknown>): Promise<api.ApiError> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return error as api.ApiError
  }
  throw new Error('the request did not fail')
}

describe("the model's notice", () => {
  // A29: the app and the server ship in one image, so the notice agreed to is the version the server records.
  it('is the version the service records', () => {
    const dir = join(import.meta.dirname, '../../../internal/domain')
    const source = readdirSync(dir)
      .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
      .map((f) => readFileSync(join(dir, f), 'utf8'))
      .join('\n')
    expect(source.match(/\bModelNoticeVersion\s*=\s*"([^"]+)"/)?.[1]).toBe(MODEL_NOTICE_VERSION)
  })

  // Its words are docs/web.md's, which say what is sent, what never is, and what OpenAI may keep.
  it('says what docs/web.md says, in its words', () => {
    const doc = readFileSync(join(import.meta.dirname, '../../../docs/web.md'), 'utf8')
    expect(doc).toContain(`### The model's notice (version ${MODEL_NOTICE_VERSION})`)
    expect(doc).toContain(`**${modelNoticeTitle}**`)
    for (const paragraph of modelNotice) expect(doc).toContain(`> ${paragraph}`)
    const words = modelNotice.join(' ')
    for (const said of [
      'OpenAI',
      'subject',
      'never sends the sender, the recipients or the attachments',
      'up to 30 days',
      'zero data retention',
      'sealed',
    ])
      expect(words).toContain(said)
  })

  it('is agreed to by its version', async () => {
    server.on('POST /api/v1/account/model-notice', ok(account))
    expect(await api.agreeModelNotice(MODEL_NOTICE_VERSION)).toEqual(account)
    expect(server.asked('POST /api/v1/account/model-notice')[0].body).toEqual({
      version: MODEL_NOTICE_VERSION,
    })
  })

  it('refused is model_notice_needed', async () => {
    server.on('POST /api/v1/account/model-notice', {
      status: 400,
      body: { error: { code: 'model_notice_needed', message: 'x' } },
    })
    expect((await failure(api.agreeModelNotice('2020-01-01'))).code).toBe('model_notice_needed')
  })
})

describe('a page of matches that asks the model', () => {
  // A28: ask_model is sent only when asked for, and the page's model is kept when it is one of the three.
  it('sends ask_model only when asked, and keeps what the page says of the model', async () => {
    const page = {
      messages: [],
      needs_model: ['m1'],
      unknown: [],
      model: 'limit',
    } satisfies api.MatchPage
    server.on('POST /api/v1/mailboxes/mbx_x/matches', ok(page))
    expect(await api.matches('mbx_x', formula, { askModel: true })).toEqual(page)
    expect(await api.matches('mbx_x', formula, { askModel: false })).toEqual(page)
    expect(await api.matches('mbx_x', formula)).toEqual(page)
    expect(server.asked('POST /api/v1/mailboxes/mbx_x/matches').map((a) => a.body)).toEqual([
      { formula, limit: api.messagesPerPage, ask_model: true },
      { formula, limit: api.messagesPerPage },
      { formula, limit: api.messagesPerPage },
    ])
  })

  it('keeps asked, limit and off, and drops anything else', async () => {
    for (const model of ['asked', 'limit', 'off'] as const) {
      server.on(`POST /api/v1/mailboxes/mbx_${model}/matches`, ok({ messages: [], model }))
      expect((await api.matches(`mbx_${model}`, formula, { askModel: true })).model).toBe(model)
    }
    for (const [i, model] of ['on', 1, null, { x: 1 }].entries()) {
      server.on(`POST /api/v1/mailboxes/mbx_odd${i}/matches`, ok({ messages: [], model }))
      expect(await api.matches(`mbx_odd${i}`, formula, { askModel: true })).toEqual({
        messages: [],
        needs_model: [],
        unknown: [],
      })
    }
  })

  it('refused for the notice is model_notice_needed', async () => {
    server.on('POST /api/v1/mailboxes/mbx_x/matches', {
      status: 400,
      body: { error: { code: 'model_notice_needed', message: 'x' } },
    })
    expect((await failure(api.matches('mbx_x', formula, { askModel: true }))).code).toBe(
      'model_notice_needed',
    )
  })
})
