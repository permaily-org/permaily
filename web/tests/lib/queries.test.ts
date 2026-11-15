import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { ok, server } from '../helpers/server.ts'

// Q36 (ADR 0027): the app's types hold the server's answers. Each fixture is the JSON that
// internal/contracts' TestQueryShapes pins, typed here with `satisfies`, so a change to either side that the other
// does not follow fails the type check of the tests.

const formula = {
  v: 1,
  all: [{ any: [{ var: 'from_address', op: 'is', value: 'ann@example.org' }] }],
} satisfies api.QueryFormula

const check = {
  formula,
  id: 'qry_x',
  sentence: {
    lang: 'en',
    text: 'Mail sent from “ann@example.org”.',
    lead: [],
    lines: [
      {
        parts: [
          { text: 'Mail sent from ' },
          { text: 'ann@example.org', value: true },
          { text: '.' },
        ],
      },
    ],
  },
  model: false,
} satisfies api.FormulaCheck

const page = {
  messages: [
    {
      id: 'm1',
      received_at: '2026-11-14T10:00:00Z',
      expires_at: '2026-11-14T10:00:00Z',
      size: 10,
      from: [{ address: 'a@example.org' }],
      subject: 'Hi',
      snippet: 'Hello',
      attachments: 0,
      confirmation: false,
    },
  ],
  needs_model: ['m2'],
  unknown: [],
  next: 'm3',
  searched_to: '2026-11-14T10:00:00Z',
} satisfies api.MatchPage

const problem = { at: 'all[2].any[0].value', reason: 'invalid_value' } satisfies api.FormulaProblem

/** The error a request fails with. */
async function failure(request: Promise<unknown>): Promise<api.ApiError> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return error as api.ApiError
  }
  throw new Error('the request did not fail')
}

describe('the query calls', () => {
  it('ask the routes of ADR 0027 with the shapes of the contract', async () => {
    server
      .on('POST /api/v1/queries/check', ok(check))
      .on('POST /api/v1/mailboxes/mbx%2Fodd/matches', ok(page))
    expect(await api.checkQuery(formula)).toEqual(check)
    expect(server.asked('POST /api/v1/queries/check')[0].body).toEqual({ formula })
    expect(await api.matches('mbx/odd', formula, { after: 'm0', limit: 20 })).toEqual(page)
    expect(await api.matches('mbx/odd', formula)).toEqual(page)
    expect(server.asked('POST /api/v1/mailboxes/mbx%2Fodd/matches').map((a) => a.body)).toEqual([
      { formula, after: 'm0', limit: 20 },
      { formula, limit: api.messagesPerPage },
    ])
  })

  it('fill what a page leaves out, and refuse an answer that is not one', async () => {
    server
      .on('POST /api/v1/mailboxes/mbx_x/matches', ok({ messages: [], next: '' }))
      .on('POST /api/v1/mailboxes/mbx_y/matches', ok({ needs_model: [] }))
      .on('POST /api/v1/queries/check', ok({ id: 'qry_x' }))
    expect(await api.matches('mbx_x', formula)).toEqual({
      messages: [],
      needs_model: [],
      unknown: [],
    })
    expect((await failure(api.matches('mbx_y', formula))).code).toBe('other')
    expect((await failure(api.checkQuery(formula))).code).toBe('other')
  })

  it('carry where and why a formula was refused, and nothing that is not that', async () => {
    const refusal = (details: unknown) => ({
      status: 400,
      body: { error: { code: 'invalid_formula', message: 'not for people', details } },
    })
    server
      .on('POST /api/v1/queries/check', refusal(problem))
      .on('POST /api/v1/mailboxes/mbx_a/matches', refusal({ at: 3, reason: 'invalid_value' }))
      .on('POST /api/v1/mailboxes/mbx_b/matches', refusal(undefined))
      .on('POST /api/v1/mailboxes/mbx_c/matches', {
        status: 400,
        body: { error: { code: 'invalid_query', message: 'x', details: problem } },
      })
    const refused = await failure(api.checkQuery(formula))
    expect(refused.code).toBe('invalid_formula')
    expect(refused.problem).toEqual(problem)
    for (const mailbox of ['mbx_a', 'mbx_b']) {
      const error = await failure(api.matches(mailbox, formula))
      expect(error.code).toBe('invalid_formula')
      expect(error.problem).toBeUndefined()
    }
    const limit = await failure(api.matches('mbx_c', formula))
    expect(limit.code).toBe('invalid_query')
    expect(limit.problem).toBeUndefined()
  })
})
