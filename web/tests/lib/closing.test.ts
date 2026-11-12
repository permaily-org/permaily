import { describe, expect, it } from 'vitest'
import {
  closesAt,
  closingDay,
  closingDays,
  daysLeft,
  forgetClosing,
  formatClock,
  formatFullDay,
  rememberClosing,
  takeClosed,
} from '../../src/lib/closing.ts'
import { blockStorage } from '../helpers/app.tsx'

const day = 86_400_000
const when = '2026-11-18T14:32:00Z'
const at = Date.parse(when)

describe('the day an account closes', () => {
  it('is written whole, with the zone the time is told in', () => {
    const date = new Date(when)
    expect(formatFullDay(date)).toBe('Wednesday, 18 November 2026')
    expect(formatClock(date)).toBe('14:32 UTC')
  })

  it('is seven days after it is asked for, as near as this clock tells', () => {
    expect(closingDays).toBe(7)
    expect(closingDay(Date.parse('2026-11-11T14:32:00Z')).toISOString()).toBe(
      '2026-11-18T14:32:00.000Z',
    )
  })

  it('is read from what the server answered, or not at all', () => {
    expect(closesAt(when)?.getTime()).toBe(at)
    for (const nothing of [undefined, '', 'soon', '2026-13-45']) {
      expect(closesAt(nothing)).toBeNull()
    }
  })

  it('counts the days left by the calendar, not in spans of 24 hours', () => {
    const until = new Date(when)
    expect(daysLeft(until, Date.parse('2026-11-11T23:59:00Z'))).toBe('in 7 days')
    expect(daysLeft(until, Date.parse('2026-11-16T00:01:00Z'))).toBe('in 2 days')
    // Less than a day and a half away, and still the day after.
    expect(daysLeft(until, Date.parse('2026-11-17T00:01:00Z'))).toBe('tomorrow')
    expect(daysLeft(until, Date.parse('2026-11-17T23:59:00Z'))).toBe('tomorrow')
    expect(daysLeft(until, Date.parse('2026-11-18T00:01:00Z'))).toBe('today')
    // Never a day that has passed.
    expect(daysLeft(until, at + 3 * day)).toBe('today')
  })
})

describe('what is kept of a closing account in this browser', () => {
  it('is the day and nothing else', () => {
    rememberClosing(when)
    expect({ ...localStorage }).toEqual({ 'permaily-closes-at': '2026-11-18T14:32:00.000Z' })
    expect({ ...sessionStorage }).toEqual({})
  })

  it('is nothing when the server gave no day that can be read', () => {
    for (const nothing of [undefined, '', 'soon', '<b>18 November</b>']) {
      rememberClosing(nothing)
      expect({ ...localStorage }).toEqual({})
    }
  })

  it('is not said before the day, and stays for when it comes', () => {
    rememberClosing(when)
    expect(takeClosed(at - 1)).toBeNull()
    expect({ ...localStorage }).toEqual({ 'permaily-closes-at': '2026-11-18T14:32:00.000Z' })
  })

  it('is said once from the day on, and forgotten', () => {
    rememberClosing(when)
    expect(takeClosed(at)?.toISOString()).toBe('2026-11-18T14:32:00.000Z')
    expect({ ...localStorage }).toEqual({})
    expect(takeClosed(at + day)).toBeNull()
  })

  it('is forgotten when the deletion is cancelled or an account that works is seen', () => {
    rememberClosing(when)
    forgetClosing()
    expect({ ...localStorage }).toEqual({})
    expect(takeClosed(at + day)).toBeNull()
  })

  it('is never shown as it is: what is not a date this app wrote is taken out and says nothing', () => {
    for (const written of [
      '',
      'tomorrow',
      'ana@example.org',
      '<img src=x onerror=alert(1)>',
      '2026-11-18',
      '2026-11-18T14:32:00Z',
      '2026-13-45T14:32:00.000Z',
      '2026-11-18T14:32:00.000Z and more',
    ]) {
      localStorage.setItem('permaily-closes-at', written)
      expect(takeClosed(at + day)).toBeNull()
      expect({ ...localStorage }).toEqual({})
    }
  })

  it('is nothing where storage is unavailable, and nothing fails', () => {
    blockStorage('localStorage')
    expect(() => rememberClosing(when)).not.toThrow()
    expect(() => forgetClosing()).not.toThrow()
    expect(takeClosed(at + day)).toBeNull()
  })
})
