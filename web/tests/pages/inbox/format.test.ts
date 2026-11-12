import { describe, expect, it } from 'vitest'
import { formatWhen, mailboxName, stateWords } from '../../../src/pages/accounts/shared.tsx'
import { day, fullTime, listTime, senderOf, size } from '../../../src/pages/inbox/format.ts'
import { lost, mailboxOf } from '../../helpers/mailboxes.ts'

const now = Date.parse('2026-11-12T15:00:00Z')

describe('how the inbox writes times, sizes and senders', () => {
  it('writes the time for today, the day for this year, and the year before that', () => {
    expect(listTime('2026-11-12T09:05:00Z', now)).toBe('09:05')
    expect(listTime('2026-03-01T09:05:00Z', now)).toBe('1 Mar')
    expect(listTime('2025-12-31T09:05:00Z', now)).toBe('31 Dec 2025')
    expect(listTime('not a time', now)).toBe('')
    expect(fullTime('2026-11-12T09:05:00Z')).toBe('Thu, 12 Nov 2026, 09:05 UTC')
    expect(fullTime('')).toBe('')
    expect(day('2026-11-12T09:05:00Z')).toBe('12 Nov 2026')
    expect(day('x')).toBe('')
    expect(formatWhen('2026-11-12T09:05:00Z')).toBe('12 Nov 2026, 09:05 UTC')
    expect(formatWhen('x')).toBe('')
  })

  it('writes sizes people read', () => {
    expect(size(812)).toBe('812 bytes')
    expect(size(-1)).toBe('0 bytes')
    expect(size(2_400)).toBe('2.4 KB')
    expect(size(14_000)).toBe('14 KB')
    expect(size(2_400_000)).toBe('2.4 MB')
    expect(size(9e12)).toBe('9000 GB')
  })

  it('names the first sender by name, then address, and says when there is none', () => {
    expect(senderOf([{ name: 'Ana', address: 'ana@example.com' }])).toBe('Ana')
    expect(senderOf([{ address: 'ana@example.com' }])).toBe('ana@example.com')
    expect(senderOf([])).toBe('No sender')
  })

  it('says each state of a mailbox in words, and names it', () => {
    expect(stateWords(mailboxOf({ state: 'waiting' }) as never)).toBe('Waiting for Gmail')
    expect(stateWords(mailboxOf() as never)).toBe('Receiving since 3 Nov 2026')
    expect(stateWords(mailboxOf({ receiving_since: undefined }) as never)).toBe('Receiving')
    expect(stateWords(mailboxOf({ state: 'paused' }) as never)).toBe('Paused by Permaily')
    expect(stateWords(lost as never)).toBe("Can't be read")
    expect(mailboxName(mailboxOf() as never)).toBe('Personal')
    expect(mailboxName(mailboxOf({ label: '' }) as never)).toBe(
      'q7wz3kfn9tr2hmx4vp8c@inbox.permaily.com',
    )
    expect(mailboxName(lost as never)).toBe('Mailbox')
  })
})
