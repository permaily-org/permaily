import type { ApiErrorCode } from '../../../lib/api.ts'

/** What every part of the Sign-in section needs from the section around it. */
export type SignInSection = {
  /** Asks the server again for the passkeys, the providers and the sessions, after something changed. */
  refresh: () => Promise<void>
  /**
   * The code of a failure, or null when it was the session ending: the app then goes to the sign-in screen
   * and there is nothing left to say here.
   */
  failed: (error: unknown) => ApiErrorCode | null
  /** Says what just happened to a screen reader. The page shows it by changing. */
  announce: (text: string) => void
  /** Puts focus on the section once it is read again, for when the control that had focus goes with its row. */
  focusSection: () => void
}

const day = new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })

/** "6 Mar 2026", in the person's own time zone. */
export function formatDay(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? '' : day.format(date)
}

const hour = 3_600_000

/**
 * How long ago a session was used. The server writes the time of last use at most once an hour, so nothing
 * finer than that is claimed.
 */
export function lastUsed(iso: string, now: number): string {
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const hours = Math.floor((now - then) / hour)
  if (hours < 1) return 'Used in the last hour'
  if (hours < 24) return `Last used ${hours} ${hours === 1 ? 'hour' : 'hours'} ago`
  const days = Math.floor(hours / 24)
  return days === 1 ? 'Last used yesterday' : `Last used ${days} days ago`
}

/** The most characters the server keeps of a passkey's name. */
export const maxName = 64

/** The name as the server keeps it, or null when it cannot keep it: one line, with something in it, and short. */
export function passkeyName(typed: string): string | null {
  const name = typed.trim()
  // Letters, marks, digits, punctuation, symbols and the plain space: what the server takes for one line.
  if ([...name].length > maxName || !/^[\p{L}\p{M}\p{N}\p{P}\p{S} ]+$/u.test(name)) return null
  return name
}
