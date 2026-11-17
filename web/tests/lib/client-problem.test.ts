import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'

// PR #131's review, finding 5: the person's invalid_client carries one of three fixed reasons, typed, beside a
// formula's details, which keep their shape.
describe('the details of an error', () => {
  it('reads the three reasons of an invalid client', () => {
    expect(api.CLIENT_PROBLEMS).toEqual([
      'client_id_invalid',
      'client_document_invalid',
      'redirect_uri_invalid',
    ])
    for (const reason of api.CLIENT_PROBLEMS) expect(api.clientProblemOf({ reason })).toBe(reason)
  })

  it('reads nothing else as a reason', () => {
    for (const details of [
      undefined,
      null,
      'client_id_invalid',
      {},
      { reason: 'https://private-marker.invalid/c' },
      { reason: 'not_json' },
      { at: '', reason: 'client_id_invalid' },
    ])
      expect(api.clientProblemOf(details)).toBeUndefined()
  })

  it('types both shapes', () => {
    const shapes: api.ErrorDetails[] = [
      { at: 'all[0].any[0].var', reason: 'part_not_approved' },
      { reason: 'redirect_uri_invalid' },
    ]
    expect(shapes.map((d) => ('at' in d ? 'formula' : api.clientProblemOf(d)))).toEqual([
      'formula',
      'redirect_uri_invalid',
    ])
  })
})
