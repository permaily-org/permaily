import { describe, expect, it } from 'vitest'
import type * as api from '../../../src/lib/api.ts'
import { groupByDay, moreLine } from '../../../src/pages/activity/ActivityPage.tsx'
import { matches, type Row, rowsOf } from '../../../src/pages/activity/record.ts'
import {
  conditionWords,
  dayWords,
  givenWords,
  heldWhyWords,
  linkVerdictChip,
  linkVerdictWords,
  momentWords,
  receiptVerdictChip,
  receiptVerdictWords,
  refusalCountWords,
  refusedWords,
  sendersWords,
  senderWords,
  shortDigest,
} from '../../../src/pages/activity/words.ts'
import { linkOf, pageOf, receiptOf } from '../../helpers/receipts.ts'

// ADR 0041, E3 and E4 in parts: the words of every verdict and reason, and the rows and lines the screen builds.

describe('E3: the words of what was found', () => {
  it('has words and a chip for every verdict, and for one it does not know', () => {
    expect(receiptVerdictWords('ok', 2, 1)).toBe('Receipt 2.1 is kept as it was recorded.')
    expect(receiptVerdictChip('ok')).toBe('Kept')
    expect(linkVerdictWords('expired', 1234)).toBe('Link 1,234 is kept as it was written.')
    expect(linkVerdictWords(undefined, 1)).toBe("Link 1 couldn't be checked.")
    expect(linkVerdictChip('expired')).toBe('Kept')
    expect(linkVerdictChip(undefined)).toBe('Not checked')
  })

  it('says every reason a request got no mail, and a summary’s counts', () => {
    for (const reason of [
      'held',
      'outside_scope',
      'part_not_given',
      'invalid_formula',
      'formula_part_not_approved',
      'model_unavailable',
      'attachment_too_large',
      'invalid_body',
      'invalid_limit',
      'invalid_cursor',
      'rate_limited',
      'unavailable',
      'connection_ended',
      'too_late',
    ])
      expect(refusedWords(reason), reason).toBeTruthy()
    expect(refusedWords('later')).toBeNull()
    expect(refusalCountWords('search', 'later')).toBe('Searches: later')
    expect(refusalCountWords('list', 'rate_limited')).toBe('list: Too many requests')
  })

  it('says how a rule held, a model’s condition, the parts and the senders', () => {
    expect(heldWhyWords('recovered')).toBe('Held because its decision was lost')
    expect(heldWhyWords('undecided')).toBe("Held because it couldn't be decided")
    expect(heldWhyWords('later')).toBe('Held')
    expect(
      conditionWords({ var: 'from', op: 'is', topic: 'x', p: 1 } as unknown as api.ReceiptModel),
    ).toEqual({
      lead: 'from is',
      topic: 'x',
    })
    expect(
      conditionWords({
        var: 'message',
        op: 'about',
        topic: 'x',
        p: 1,
      } as unknown as api.ReceiptModel).lead,
    ).toBe('the message is about')
    expect(givenWords(['sender', 'body', 'attachments'])).toBe(
      'Sender and date, Body text, Attachments',
    )
    expect(senderWords([])).toBe('')
    expect(senderWords([{ address: 'a@example.org' }])).toBe('a@example.org')
    expect(
      sendersWords([{ address: 'a@example.org' }, { name: 'B', address: 'b@example.org' }]),
    ).toBe('a@example.org; B <b@example.org>')
    expect(shortDigest('abc')).toBe('abc')
    expect(dayWords('not a time')).toBeNull()
    expect(dayWords(undefined)).toBeNull()
    expect(momentWords(undefined)).toBe('')
  })
})

describe('E4: rows, filters and what a press found', () => {
  const page = pageOf([
    linkOf(5, [receiptOf('refusals'), receiptOf('search')]),
    linkOf(4, [], { verdict: 'lost', at: undefined, receipts: [] }),
  ])
  const rows = rowsOf(page as api.ReceiptsPage)

  it('puts what was refused or counted under held or refused', () => {
    const held = rows.filter((r) => matches(r, { connection: null, kind: 'held' }))
    expect(held.map((r) => r.key)).toEqual(['5.1:whole', '5.0:h0', '5.0:h1', '4:link'])
  })

  it('keeps a row with no time of its own under the day of the row before it', () => {
    const groups = groupByDay(rows)
    expect(groups).toHaveLength(1)
    expect(groups[0]?.day).toBe('Monday, 16 November 2026')
    const alone = groupByDay(rows.slice(-1) as Row[])
    expect(alone[0]?.day).toBe('Time unknown')
  })

  it('says how far a filtered press looked, by kind and by connection', () => {
    const kind = { connection: null, kind: 'decided' as const }
    const both = { connection: 'con_x', kind: 'held' as const }
    expect(moreLine(kind, 0, 500, { found: 0, links: 3 })).toBe(
      'Nothing of this kind in the newest 500 receipts.',
    )
    expect(moreLine(both, 0, 500, { found: 0, links: 3 })).toBe(
      'Nothing of this kind from this connection in the newest 500 receipts.',
    )
    expect(moreLine(both, 4, 1000, { found: 0, links: 3 })).toBe(
      'Nothing more of this kind from this connection in the newest 1,000 receipts.',
    )
    expect(moreLine({ connection: 'con_x', kind: 'all' }, 4, 600, { found: 0, links: 3 })).toBe(
      'No more receipts of this connection in the newest 600.',
    )
    expect(moreLine({ connection: null, kind: 'all' }, 4, 600, { found: 0, links: 1 })).toBe(
      'Looked through 1 link.',
    )
    expect(moreLine({ connection: null, kind: 'all' }, 4, 600, { found: 2, links: 1 })).toBeNull()
    expect(moreLine(kind, 3, 600, null)).toBeNull()
  })
})
