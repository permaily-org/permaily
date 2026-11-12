import type * as api from '../../lib/api.ts'

const clock = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' })
const dayMonth = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short' })
const dayMonthYear = new Intl.DateTimeFormat('en-GB', {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
})
const full = new Intl.DateTimeFormat('en-GB', {
  weekday: 'short',
  day: 'numeric',
  month: 'short',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  timeZoneName: 'short',
})

function date(iso: string): Date | null {
  const when = new Date(iso)
  return Number.isNaN(when.getTime()) ? null : when
}

/**
 * When a message arrived, as short as a list can say it in the person's own time zone: the time for today,
 * the day for this year, the day and year before.
 */
export function listTime(iso: string, now: number): string {
  const when = date(iso)
  if (!when) return ''
  const today = new Date(now)
  if (when.toDateString() === today.toDateString()) return clock.format(when)
  if (when.getFullYear() === today.getFullYear()) return dayMonth.format(when)
  return dayMonthYear.format(when)
}

/** "Thu, 12 Nov 2026, 14:32 UTC". */
export function fullTime(iso: string): string {
  const when = date(iso)
  return when ? full.format(when) : ''
}

/** "12 Nov 2026". */
export function day(iso: string): string {
  const when = date(iso)
  return when ? dayMonthYear.format(when) : ''
}

const units = ['bytes', 'KB', 'MB', 'GB']

/** A size people read: "812 bytes", "14 KB", "2.4 MB". */
export function size(bytes: number): string {
  let value = Math.max(0, bytes)
  let unit = 0
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000
    unit++
  }
  const shown = unit === 0 || value >= 10 ? Math.round(value) : Math.round(value * 10) / 10
  return `${shown} ${units[unit]}`
}

/** Who a message is from, as short as a list can say it: the first sender's name, or its address. */
export function senderOf(from: api.MailAddress[]): string {
  const first = from[0]
  if (!first) return 'No sender'
  return first.name || first.address
}
