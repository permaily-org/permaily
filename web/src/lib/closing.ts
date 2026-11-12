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
 * Where the day a closing account is removed is kept, so the sign-in screen can say why the person is there
 * once that day has passed: the server answers a removed account like any session that ended. It holds the
 * date and nothing else: no address, no id, no name.
 */
const closingKey = 'permaily-closes-at'

/** A date as this module writes it, and nothing else: what is in storage is never shown as it is. */
const written = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/

/** Remembers the day an account seen closing in this browser is removed. Without storage, nothing is kept. */
export function rememberClosing(iso: string | undefined): void {
  const when = closesAt(iso)
  if (!when) return
  try {
    window.localStorage.setItem(closingKey, when.toISOString())
  } catch {
    // Storage can be unavailable, for example in a private window: then nothing is said later.
  }
}

/** Forgets the day: the deletion was cancelled, or an account that works was seen here. */
export function forgetClosing(): void {
  try {
    window.localStorage.removeItem(closingKey)
  } catch {
    // Nothing was kept.
  }
}

/**
 * The day an account seen closing in this browser was to be removed, once that day has passed: it is given
 * once and forgotten. Null before the day, when nothing was kept, and when what is kept is not a date.
 */
export function takeClosed(now: number): Date | null {
  try {
    const kept = window.localStorage.getItem(closingKey)
    if (kept === null) return null
    const when = written.test(kept) ? closesAt(kept) : null
    if (when && when.getTime() > now) return null
    window.localStorage.removeItem(closingKey)
    return when
  } catch {
    return null
  }
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
