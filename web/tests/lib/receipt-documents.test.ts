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
