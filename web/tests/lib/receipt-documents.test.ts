import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { readReceipt } from '../../src/lib/api.ts'

// ADR 0041, E19: the receipts the Activity screen and the CSV read are the documents the server stores, the golden
// files the receipts service is held to (H13), read here as they are, every kind of them.
const dir = join(import.meta.dirname, '../../../internal/services/receipts/testdata/golden')
const golden = readdirSync(dir)
  .filter((f) => f.endsWith('.json'))
  .map((f) => ({
    name: f,
    doc: JSON.parse(readFileSync(join(dir, f), 'utf8')) as Record<string, unknown>,
  }))

describe('a receipt’s document', () => {
  it('reads every golden receipt, one of each kind', () => {
    const kinds = golden.map(({ doc }) => readReceipt(doc)?.kind)
    expect(kinds.sort()).toEqual([
      'attachment',
      'consent',
      'ended',
      'read',
      'refusals',
      'refused',
      'search',
    ])
    const search = readReceipt(golden.find((g) => g.name === 'search.json')?.doc)
    expect(search?.delivered[0]?.subject).toBe('Invoice 12 <for> "you"')
    expect(search?.formulas.some((f) => f.role === 'search' && f.formula)).toBe(true)
  })

  it('gives the lists a version leaves out as empty, and reads version 1 as written', () => {
    const ended = golden.find((g) => g.name === 'ended.json')?.doc ?? {}
    const v1 = readReceipt({ ...ended, v: 1, formulas: undefined, rules: undefined })
    expect(v1).toMatchObject({
      v: 1,
      kind: 'ended',
      formulas: [],
      rules: [],
      delivered: [],
      held: [],
    })
  })

  it('is null for what this app can’t show, never a guess', () => {
    const consent = golden.find((g) => g.name === 'consent.json')?.doc ?? {}
    const refusals = golden.find((g) => g.name === 'refusals.json')?.doc ?? {}
    for (const bad of [
      null,
      'rcp_x',
      { ...consent, v: 3 },
      { ...consent, id: 'con_x' },
      { ...consent, id: 7 },
      { ...consent, at: undefined },
      { ...consent, vendor: undefined },
      { ...consent, kind: 'webhook' },
      { ...consent, consent: undefined },
      { ...refusals, v: 1 },
      { ...refusals, refusals: 'many' },
    ])
      expect(readReceipt(bad)).toBeNull()
  })
})

// ADR 0041, "Changed after the design review", 5: readReceipt is the screen's and the CSV's compatibility boundary.
// The storage authenticates a receipt's bytes and its version and identifier, not this app's schema, so every part,
// list and nested field the screen and the CSV read is checked here, and a shape they cannot use is null, listed as
// "This app can't show this receipt", never a row that throws.
describe('a receipt’s shape', () => {
  const doc = (name: string): Record<string, unknown> =>
    structuredClone(golden.find((g) => g.name === name)?.doc ?? {})
  const at = <T>(d: Record<string, unknown>, path: string, value: T): Record<string, unknown> => {
    const keys = path.split('.')
    let o: Record<string, unknown> = d
    for (const k of keys.slice(0, -1)) o = o[k] as Record<string, unknown>
    const last = keys.at(-1) ?? ''
    if (value === undefined) delete o[last]
    else o[last] = value
    return d
  }

  it('is null for the review’s reproduction', () => {
    expect(
      readReceipt({
        v: 2,
        id: 'rcp_x',
        at: 'now',
        vendor: 'claude.ai',
        kind: 'consent',
        consent: null,
        delivered: 'not a list',
      }),
    ).toBeNull()
  })

  const cases: [string, string, unknown][] = [
    ['consent.json', 'consent', null],
    ['consent.json', 'consent', []],
    ['refused.json', 'refused', null],
    ['refused.json', 'refused', ['held']],
    ['ended.json', 'ended', null],
    ['ended.json', 'ended', []],
    ['refusals.json', 'refusals', null],
    ['refusals.json', 'refusals', []],
    ['search.json', 'client_id', undefined],
    ['search.json', 'resource', 7],
    ['search.json', 'connection', 7],
    ['search.json', 'device', {}],
    ['search.json', 'delivered', 'not a list'],
    ['search.json', 'delivered', {}],
    ['search.json', 'held', 'not a list'],
    ['search.json', 'formulas', {}],
    ['search.json', 'rules', 'default'],
    ['search.json', 'delivered.0', null],
    ['search.json', 'delivered.0', []],
    ['search.json', 'delivered.0.mailbox', undefined],
    ['search.json', 'delivered.0.message', 7],
    ['search.json', 'delivered.0.received', undefined],
    ['search.json', 'delivered.0.parts', 'sender'],
    ['search.json', 'delivered.0.parts', [7]],
    ['search.json', 'delivered.0.from', 'billing@example.com'],
    ['search.json', 'delivered.0.from', [null]],
    ['search.json', 'delivered.0.from', [{ name: 'Billing' }]],
    ['search.json', 'delivered.0.subject', 12],
    ['search.json', 'delivered.0.attachment', '1'],
    ['search.json', 'delivered.0.model', {}],
    ['search.json', 'delivered.0.model.0', null],
    ['search.json', 'delivered.0.model.0.topic', undefined],
    ['search.json', 'delivered.0.model.0.p', '0.91'],
    ['search.json', 'delivered.0.model.0.var', 7],
    ['search.json', 'delivered.0.cleared', 'yes'],
    ['search.json', 'delivered.0.cleared.0.rule', null],
    ['search.json', 'delivered.0.cleared.0.rule.version', '1'],
    ['search.json', 'delivered.0.cleared.0.model', [{}]],
    ['search.json', 'held.0', 'held'],
    ['search.json', 'held.0.rule', undefined],
    ['search.json', 'held.0.rule.id', 7],
    ['search.json', 'held.0.why', undefined],
    ['search.json', 'formulas.0', null],
    ['search.json', 'formulas.0.role', undefined],
    ['search.json', 'formulas.3.formula', 'subject about invoices'],
    ['search.json', 'formulas.3.formula', []],
    ['search.json', 'rules.0', 'default-sign-in-and-security'],
    ['search.json', 'rules.0.version', undefined],
    ['refused.json', 'refused.reason', undefined],
    ['refused.json', 'refused.mailbox', 7],
    ['refused.json', 'refused.held', 'decided'],
    ['refused.json', 'refused.held.rule', null],
    ['consent.json', 'consent.allowed', 'yes'],
    ['consent.json', 'consent.renewal', 'no'],
    ['consent.json', 'consent.sentences', 'Mail.'],
    ['consent.json', 'consent.sentences', [7]],
    ['consent.json', 'consent.parts', 'sender'],
    ['consent.json', 'consent.since', null],
    ['consent.json', 'consent.since', []],
    ['consent.json', 'consent.since.new', undefined],
    ['consent.json', 'consent.ends_at', 7],
    ['consent.json', 'consent.mailboxes', 'mbx_a'],
    ['consent.json', 'consent.model_notice', undefined],
    ['consent.json', 'consent.change', 7],
    ['ended.json', 'ended.reason', undefined],
    ['ended.json', 'ended.replaced_by', 7],
    ['refusals.json', 'refusals.from', undefined],
    ['refusals.json', 'refusals.recorded', '30'],
    ['refusals.json', 'refusals.counts', 'many'],
    ['refusals.json', 'refusals.counts.0', null],
    ['refusals.json', 'refusals.counts.0.count', '412'],
    ['refusals.json', 'refusals.counts.0.operation', undefined],
  ]
  it.each(cases)('is null for %s with %s as %j', (name, path, value) => {
    expect(readReceipt(at(doc(name), path, value))).toBeNull()
  })

  it('reads a list Go wrote as null (a nil slice) or left out as empty', () => {
    const consent = readReceipt(at(doc('consent.json'), 'consent.mailboxes', null))
    expect(consent?.consent?.mailboxes).toEqual([])
    const sentences = readReceipt(at(doc('consent.json'), 'consent.sentences', null))
    expect(sentences?.consent?.sentences).toEqual([])
    const search = readReceipt(at(at(doc('search.json'), 'delivered', null), 'held', undefined))
    expect(search).toMatchObject({ delivered: [], held: [] })
    const counts = readReceipt(at(doc('refusals.json'), 'refusals.counts', null))
    expect(counts?.refusals?.counts).toEqual([])
  })

  // The design's re-review, finding 5: Go writes a nil slice as null at every depth, not only at the top. Every list
  // the type declares is a list once read, so no reader of `Receipt` meets a null where it was told of an array.
  it('reads a nested list Go wrote as null, or left out, as empty, at every depth (E19)', () => {
    const nested: [string, string, (r: ReturnType<typeof readReceipt>) => unknown][] = [
      ['search.json', 'delivered.0.from', (r) => r?.delivered[0]?.from],
      ['search.json', 'delivered.0.model', (r) => r?.delivered[0]?.model],
      ['search.json', 'delivered.0.cleared', (r) => r?.delivered[0]?.cleared],
      ['search.json', 'delivered.0.cleared.0.model', (r) => r?.delivered[0]?.cleared[0]?.model],
      ['search.json', 'held.0.model', (r) => r?.held[0]?.model],
      ['refused.json', 'refused.held.model', (r) => r?.refused?.held?.model],
    ]
    for (const [name, path, read] of nested)
      for (const value of [null, undefined]) {
        const r = readReceipt(at(doc(name), path, value))
        expect(r, `${path} as ${value}`).not.toBeNull()
        expect(read(r), `${path} as ${value}`).toEqual([])
      }
    // The held message a search holds without a model's answer has none, as a list.
    expect(readReceipt(doc('search.json'))?.held[1]?.model).toEqual([])
    // All four at once, the review's own probe.
    const probe = doc('search.json')
    for (const path of ['delivered.0.from', 'delivered.0.model', 'delivered.0.cleared', 'held.0.model'])
      at(probe, path, null)
    const read = readReceipt(probe)
    expect(read?.delivered[0]).toMatchObject({ from: [], model: [], cleared: [] })
    expect(read?.held[0]?.model).toEqual([])
  })

  it('reads every golden receipt whole, nothing dropped', () => {
    for (const { name, doc: d } of golden) expect(readReceipt(d), name).toMatchObject(d)
  })
})
