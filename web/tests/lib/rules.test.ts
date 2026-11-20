import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { messageFor, ruleProblemFor, ruleProblems } from '../../src/pages/auth/messages.ts'
import {
  complete,
  fromCriteria,
  fromFormula,
  newCondition,
  toCriteria,
  toFormula,
  usesModel,
} from '../../src/pages/rules/criteria.ts'
import { confirmationLines, inFlight, topicsOverRemove } from '../../src/pages/rules/words.ts'
import { ok, server } from '../helpers/server.ts'

// U32 (ADR 0040, pull request 7): the web app's calls of the rules routes, their refusals, and its shapes, which are the
// contract's (internal/contracts' TestU1TheRulesShapes, the same values in Go's member order).

const at = '2026-11-20T12:00:00Z'

async function failure(request: Promise<unknown>): Promise<api.ApiError> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return error as api.ApiError
  }
  throw new Error('the request did not fail')
}

const check: api.RuleCheck = {
  draft_id: 'rdr_x',
  draft: {
    name: 'Home address',
    criteria: [{ v: 1, all: [] }],
    effect: 'remove',
    removal: { parts: ['attachments'], shapes: ['phone_number'], topics: ['a home address'] },
    scope: { to: 'all_except', clients: ['https://claude.ai/oauth/mcp-oauth-client-metadata'] },
    on: true,
  },
  sentence: { lang: 'en', text: 'All mail', lead: [], formulas: [] },
  does: 'Takes out',
  for: 'Every connection but claude.ai/oauth/mcp-oauth-client-metadata',
  clients: [
    {
      client_id: 'https://claude.ai/oauth/mcp-oauth-client-metadata',
      shown: 'claude.ai/oauth/mcp-oauth-client-metadata',
      name: 'Claude',
    },
  ],
  model: true,
}

const list: api.RuleList = {
  default: { rule: { id: 'default-sign-in-and-security', version: 1, sentence: [] }, on: true },
  rules: [
    {
      id: 'rul_x',
      version: 2,
      check,
      notice: '2026-11-20',
      created_at: at,
      changed_at: at,
      model_off: true,
    },
  ],
  notice: { version: '2026-11-20', at },
  version: 7,
  max: 16,
}

describe('U32: the shapes of the rules are the contract’s', () => {
  it('writes the list as TestU1TheRulesShapes does, byte for byte', () => {
    expect(JSON.stringify(list)).toBe(
      '{"default":{"rule":{"id":"default-sign-in-and-security","version":1,"sentence":[]},"on":true},' +
        '"rules":[{"id":"rul_x","version":2,"check":{"draft_id":"rdr_x","draft":{"name":"Home address","criteria":[{"v":1,"all":[]}],' +
        '"effect":"remove","removal":{"parts":["attachments"],"shapes":["phone_number"],"topics":["a home address"]},' +
        '"scope":{"to":"all_except","clients":["https://claude.ai/oauth/mcp-oauth-client-metadata"]},"on":true},' +
        '"sentence":{"lang":"en","text":"All mail","lead":[],"formulas":[]},"does":"Takes out","for":"Every connection but claude.ai/oauth/mcp-oauth-client-metadata",' +
        '"clients":[{"client_id":"https://claude.ai/oauth/mcp-oauth-client-metadata","shown":"claude.ai/oauth/mcp-oauth-client-metadata","name":"Claude"}],"model":true},' +
        '"notice":"2026-11-20","created_at":"2026-11-20T12:00:00Z","changed_at":"2026-11-20T12:00:00Z","model_off":true}],' +
        '"notice":{"version":"2026-11-20","at":"2026-11-20T12:00:00Z"},"version":7,"max":16}',
    )
    const removed: api.RemovedSummary = {
      shapes: { link: 2 },
      sentences: 1,
      of: 9,
      attachments: 1,
    }
    expect(JSON.stringify(removed)).toBe(
      '{"shapes":{"link":2},"sentences":1,"of":9,"attachments":1}',
    )
  })

  it('reads back what the contract writes, every list there', async () => {
    server.on('GET /api/v1/rules', ok(JSON.parse(JSON.stringify(list))))
    expect(await api.rules()).toEqual({ ...list, rules: [{ ...list.rules[0], check }] })
    // A check the server sends without its optional lists reads with them empty.
    server.on(
      'POST /api/v1/rules/check',
      ok({ ...check, clients: undefined, sentence: { lang: 'en', text: 'All mail' } }),
    )
    const read = await api.checkRule(check.draft)
    expect(read.clients).toEqual([])
    expect(read.sentence.formulas).toEqual([])
    expect(read.sentence.lead).toEqual([])
  })

  it('names the members of the requests as the contract does', () => {
    const save: Required<api.RuleSave> = {
      draft_id: 'rdr_x',
      draft: check.draft,
      version: 7,
      notice: '2026-11-20',
    }
    expect(Object.keys(save)).toEqual(['draft_id', 'draft', 'version', 'notice'])
    const preview: Required<api.RulePreview> = {
      draft: check.draft,
      mailbox: 'mbx_x',
      after: 'a',
      limit: 50,
      ask_model: true,
      message: 'm',
    }
    expect(Object.keys(preview)).toEqual([
      'draft',
      'mailbox',
      'after',
      'limit',
      'ask_model',
      'message',
    ])
    const words: Required<api.RuleWords> = { words: 'w', notice: 'n' }
    expect(Object.keys(words)).toEqual(['words', 'notice'])
    expect(api.SHAPE_KINDS).toEqual([
      'email_address',
      'phone_number',
      'card_number',
      'account_number',
      'numeric_code',
      'link',
    ])
  })

  it('refuses an answer that is not the contract’s', async () => {
    server.on('GET /api/v1/rules', ok({ rules: 'no' }))
    expect((await failure(api.rules())).code).toBe('other')
    server.on('POST /api/v1/rules/check', ok({ draft_id: 'x' }))
    expect((await failure(api.checkRule(check.draft))).code).toBe('other')
    server.on('POST /api/v1/rules/default', ok({ on: true }))
    expect((await failure(api.setDefaultRule(true, 1))).code).toBe('other')
    server.on('POST /api/v1/account/rules-notice', ok({ version: 1 }))
    expect((await failure(api.agreeRulesNotice('v'))).code).toBe('other')
    server.on('POST /api/v1/rules/preview', ok({}))
    expect((await failure(api.previewRule(check.draft, 'mbx_x'))).code).toBe('other')
    expect((await failure(api.previewRuleRemoval(check.draft, 'mbx_x', 'm'))).code).toBe('other')
    server.on('POST /api/v1/rules', ok({ id: 'x' }))
    expect(
      (await failure(api.saveRule({ draft_id: 'd', draft: check.draft, version: 1 }))).code,
    ).toBe('other')
  })
})

describe('U32: the calls of the rules routes', () => {
  it('sends each change to its route, with the version the screen read', async () => {
    const rule = list.rules[0]
    server
      .on('POST /api/v1/rules', ok(rule))
      .on('PUT /api/v1/rules/rul_x', ok(rule))
      .on('POST /api/v1/rules/rul_x/delete', ok())
      .on('POST /api/v1/rules/default', ok({ ...list.default, on: false }))
      .on('POST /api/v1/account/rules-notice', ok({ version: '2026-11-20', at }))
      .on('POST /api/v1/rules/check', ok(check))
      .on('POST /api/v1/rules/words', ok({ check, model: 'gpt-6-luna', prompt: 'w1-2026-11-20' }))
    const save = { draft_id: 'rdr_x', draft: check.draft, version: 7, notice: '2026-11-20' }
    expect(await api.saveRule(save)).toEqual(rule)
    expect(await api.saveRule(save, 'rul_x')).toEqual(rule)
    await api.deleteRule('rul_x', 7)
    expect((await api.setDefaultRule(false, 7)).on).toBe(false)
    expect(await api.agreeRulesNotice('2026-11-20')).toEqual({ version: '2026-11-20', at })
    await api.checkRule(check.draft)
    const proposal = await api.ruleWords('my home address', '2026-11-20')
    expect(proposal.model).toBe('gpt-6-luna')
    expect(server.calls.map((c) => [`${c.method} ${c.path}`, c.body])).toEqual([
      ['POST /api/v1/rules', save],
      ['PUT /api/v1/rules/rul_x', save],
      ['POST /api/v1/rules/rul_x/delete', { version: 7 }],
      ['POST /api/v1/rules/default', { on: false, version: 7 }],
      ['POST /api/v1/account/rules-notice', { version: '2026-11-20' }],
      ['POST /api/v1/rules/check', { draft: check.draft }],
      ['POST /api/v1/rules/words', { words: 'my home address', notice: '2026-11-20' }],
    ])
  })

  it('asks a preview of a page, with the model when asked, and of one message as it would leave', async () => {
    server.on('POST /api/v1/rules/preview', (asked) =>
      (asked.body as { message?: string }).message
        ? ok({ draft_id: 'rdr_x', subject: 'Call me', removed: { shapes: { phone_number: 1 } } })
        : ok({
            draft_id: 'rdr_x',
            page: { messages: [], needs_model: ['m1'], unknown: [], removed: ['m2'], held: ['m3'] },
          }),
    )
    const page = await api.previewRule(check.draft, 'mbx_x', { askModel: true, after: 'm0' })
    expect(page.page.needs_model).toEqual(['m1'])
    expect(page.page.removed).toEqual(['m2'])
    const one = await api.previewRuleRemoval(check.draft, 'mbx_x', 'm1', true)
    expect(one.subject).toBe('Call me')
    await api.previewRule(check.draft, 'mbx_x')
    expect(server.calls.map((c) => c.body)).toEqual([
      { draft: check.draft, mailbox: 'mbx_x', after: 'm0', limit: 50, ask_model: true },
      { draft: check.draft, mailbox: 'mbx_x', message: 'm1', ask_model: true },
      { draft: check.draft, mailbox: 'mbx_x', limit: 50 },
    ])
  })

  it('reads why a rule was refused, and only from the reason’s own shape', async () => {
    const refuse = (details: unknown) => ({
      status: 400,
      body: { error: { code: 'invalid_rule', message: 'x', details } },
    })
    server.on('POST /api/v1/rules/check', refuse({ rule: 'scope_clients' }))
    let error = await failure(api.checkRule(check.draft))
    expect(error.code).toBe('invalid_rule')
    expect(error.rule).toBe('scope_clients')
    expect(ruleProblemFor(error.code, error.rule)).toBe(ruleProblems.scope_clients)
    // A reason that is not one of the server's, or details of another shape, say only that the rule can't be used.
    for (const details of [
      { rule: 'secret words' },
      { rule: 'too_large', at: '' },
      { reason: 'client_id_invalid' },
      null,
      'too_large',
    ]) {
      server.on('POST /api/v1/rules/check', refuse(details))
      error = await failure(api.checkRule(check.draft))
      expect(error.rule).toBeUndefined()
      expect(ruleProblemFor(error.code, error.rule)).toBe(messageFor('invalid_rule'))
    }
  })

  it('knows each refusal of the rules routes, and has words for each', async () => {
    for (const code of [
      'model_unavailable',
      'rule_changed',
      'rules_changed',
      'too_many_rules',
      'rules_notice_needed',
      'rule_words_unusable',
    ] as const) {
      server.on('POST /api/v1/rules/words', {
        status: 400,
        body: { error: { code, message: 'x' } },
      })
      const error = await failure(api.ruleWords('w', 'n'))
      expect(error.code).toBe(code)
      expect(messageFor(code)).not.toBe(messageFor('other'))
      expect(ruleProblemFor(code)).toBe(messageFor(code))
    }
    for (const problem of api.RULE_PROBLEMS) expect(ruleProblems[problem]).toMatch(/\.$/)
  })
})

describe('U32: who a rule is for, as the server decides it', () => {
  it('covers clients as rules.Scope.Covers does, matched exactly', () => {
    const a = 'https://vendor.example/client-a.json'
    const b = 'https://vendor.example/client-b.json'
    expect(api.ruleCovers({ to: 'all' }, a)).toBe(true)
    expect(api.ruleCovers({ to: 'only', clients: [a] }, a)).toBe(true)
    expect(api.ruleCovers({ to: 'only', clients: [a] }, b)).toBe(false)
    expect(api.ruleCovers({ to: 'all_except', clients: [a] }, a)).toBe(false)
    expect(api.ruleCovers({ to: 'all_except', clients: [a] }, b)).toBe(true)
    expect(api.ruleCovers({ to: 'only' }, a)).toBe(false)
    expect(api.ruleCovers({ to: 'all_except' }, a)).toBe(true)
    // A scope this app doesn't know covers everyone: a rule protects.
    expect(api.ruleCovers({ to: 'later' } as unknown as api.RuleScope, a)).toBe(true)
  })
})

describe('U32: the criteria in the editor’s terms', () => {
  it('turns a positive condition into one group, and a negative one into a group a value', () => {
    const subject = { ...newCondition('subject'), values: ['invoice', 'receipt'] }
    const notFrom = {
      ...newCondition('from_domain'),
      not: true,
      values: ['a.example', 'b.example'],
    }
    const files = { ...newCondition('attachments') }
    const empty = newCondition('body')
    expect(complete(files)).toBe(true)
    expect(complete(empty)).toBe(false)
    const formula = toFormula({ id: 'c', rows: [subject, notFrom, files, empty] })
    expect(formula).toEqual({
      v: 1,
      all: [
        {
          any: [
            { var: 'subject', op: 'contains', value: 'invoice' },
            { var: 'subject', op: 'contains', value: 'receipt' },
          ],
        },
        { any: [{ var: 'from_domain', op: 'is', value: 'a.example', not: true }] },
        { any: [{ var: 'from_domain', op: 'is', value: 'b.example', not: true }] },
        { any: [{ var: 'attachments', op: 'exists' }] },
      ],
    })
    // Read back, the rows are the same, and a formula made again from them is the same formula.
    const back = fromFormula(formula)
    expect(
      back.rows.map((r) => r.kind === 'condition' && [r.field, r.op, r.not, r.values]),
    ).toEqual([
      ['subject', 'contains', false, ['invoice', 'receipt']],
      ['from_domain', 'is', true, ['a.example', 'b.example']],
      ['attachments', 'exists', false, []],
    ])
    expect(toFormula(back)).toEqual(formula)
  })

  it('keeps a group it can’t show as one condition, said by the server’s line, and sends it as it was', () => {
    const mixed = {
      any: [
        { var: 'subject' as const, op: 'contains' as const, value: 'x' },
        { var: 'body' as const, op: 'contains' as const, value: 'y' },
      ],
    }
    const negatedExists = { any: [{ var: 'list' as const, op: 'exists' as const, not: true }] }
    const formula = { v: 1 as const, all: [mixed, negatedExists] }
    const line = { parts: [{ text: 'The subject contains x, or the text contains y' }] }
    const [criteria] = fromCriteria([formula], [{ lang: 'en', text: '', lead: [], lines: [line] }])
    expect(criteria?.rows[0]).toMatchObject({ kind: 'kept', group: mixed, line })
    expect(criteria?.rows[1]).toMatchObject({
      kind: 'condition',
      field: 'list',
      not: true,
      values: [],
    })
    expect(toCriteria([criteria as typeof criteria & object])).toEqual([formula])
    expect(usesModel([criteria as typeof criteria & object])).toBe(false)
    const about = fromFormula({
      v: 1,
      all: [
        {
          any: [
            { var: 'message', op: 'about', value: 'health' },
            { var: 'body', op: 'contains', value: 'x' },
          ],
        },
      ],
    })
    expect(usesModel([about])).toBe(true)
    expect(usesModel([{ id: 'c', rows: [{ ...newCondition('message'), values: ['x'] }] }])).toBe(
      true,
    )
  })
})

describe('U32: what every confirmation says', () => {
  it('says that an answer already being prepared can finish, and what a rule takes out besides', () => {
    const hold: api.RuleDraft = {
      name: '',
      criteria: [],
      effect: 'hold',
      scope: { to: 'all' },
      on: true,
    }
    expect(confirmationLines(hold)).toEqual([inFlight])
    expect(inFlight).toBe(
      'Applies to every request that begins after you save; an answer already being prepared can still finish.',
    )
    const parts = confirmationLines({ ...hold, effect: 'remove', removal: { parts: ['subject'] } })
    expect(parts).toEqual([inFlight])
    const shapes = confirmationLines({ ...hold, effect: 'remove', removal: { shapes: ['link'] } })
    expect(shapes.join(' ')).toContain('Patterns take out more than they name')
    expect(shapes.join(' ')).toContain('The sender and attachments are taken out too')
    const topics = confirmationLines({ ...hold, effect: 'remove', removal: { topics: ['health'] } })
    expect(topics).toContain(topicsOverRemove)
    // N2: a topic rule also takes out the fixed-shape pieces hidden from the model, whatever it answers.
    expect(topicsOverRemove).toContain(
      'The pieces it could not be shown (addresses, numbers and links) are taken out too, whatever it answers.',
    )
    expect(topics.join(' ')).toContain('The sender and attachments are taken out too')
    expect(topics.at(-1)).toBe(inFlight)
  })
})
