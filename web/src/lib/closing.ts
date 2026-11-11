/**
 * The days an account that its person asked to delete can still be kept. The server decides the day and says
 * it (`closes_at`); this is only for telling the person what to expect before they ask.
 */
export const closingDays = 7

const day = 86_400_000

const fullDay = new Intl.DateTimeFormat('en-GB', { dateStyle: 'full' })
const clock = new Intl.DateTimeFormat('en-GB', {
  hour: '2-digit',
  minute: '2-digit',
  timeZoneName: 'short',
})
const relative = new Intl.RelativeTimeFormat('en-GB', { numeric: 'auto' })

/** The whole day, "Wednesday, 18 November 2026", in the person's own time zone. */
export function formatFullDay(date: Date): string {
  return fullDay.format(date)
}

/** "14:32 CET": the time with the zone it is told in, so nobody has to guess whose clock it is. */
export function formatClock(date: Date): string {
  return clock.format(date)
}

/** The day an account asked to be deleted now would be removed, as near as this clock tells. */
export function closingDay(now: number): Date {
  return new Date(now + closingDays * day)
}

/** When a closing account is removed, or null when the server's answer has no day that can be read. */
export function closesAt(iso: string | undefined): Date | null {
  if (!iso) return null
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? null : date
}

/**
 * How many days are left until a date, by the person's own calendar: "in 7 days", "tomorrow", "today".
 * Counted in days of the calendar and not in spans of 24 hours, so it agrees with the day shown next to it.
 */
export function daysLeft(until: Date, now: number): string {
  const today = new Date(now)
  const midnight = (date: Date) => Date.UTC(date.getFullYear(), date.getMonth(), date.getDate())
  const days = Math.max(0, Math.round((midnight(until) - midnight(today)) / day))
  return relative.format(days, 'day')
}
