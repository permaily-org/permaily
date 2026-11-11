import { useMemo, useRef, useState } from 'react'
import { TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { describeDevice } from '../../../lib/device.ts'
import { messageFor } from '../../auth/messages.ts'
import { SettingRow } from '../rows.tsx'
import { lastUsed, type SignInSection } from './shared.ts'

/** Every device signed in to the account: which one is this browser, when the others were last used, and signing one out. */
export function Sessions({
  sessions,
  section,
}: {
  sessions: api.Session[]
  section: SignInSection
}) {
  const [reviewing, setReviewing] = useState(false)
  const [closing, setClosing] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const toggleRef = useRef<HTMLButtonElement>(null)
  // This browser first, then the ones used most lately.
  const { ordered, now } = useMemo(
    () => ({
      ordered: [...sessions].sort(
        (a, b) =>
          Number(b.current) - Number(a.current) || b.last_used_at.localeCompare(a.last_used_at),
      ),
      now: Date.now(),
    }),
    [sessions],
  )

  async function signOut(session: api.Session, device: string) {
    if (closing) return
    setError(null)
    setClosing(session.id)
    try {
      await api.closeSession(session.id)
    } catch (failure) {
      const code = section.failed(failure)
      if (!code) return
      // Signed out there already is what was wanted.
      if (code !== 'not_found') {
        setClosing(null)
        setError(
          code === 'network'
            ? `Couldn't sign ${device} out: Permaily can't be reached. Check your connection, then try again.`
            : `Couldn't sign ${device} out. ${messageFor(code)}`,
        )
        return
      }
    }
    // Its row goes, so focus moves to the action that opened the list.
    section.announce(`Signed out ${device}`)
    toggleRef.current?.focus()
    await section.refresh()
    setClosing(null)
  }

  return (
    <>
      <SettingRow
        label="Sessions"
        action={
          <TextAction
            ref={toggleRef}
            aria-expanded={reviewing}
            aria-controls="session-list"
            onClick={() => setReviewing((open) => !open)}
          >
            {reviewing ? 'Done' : 'Review'}
            <span className="sr-only"> sessions</span>
          </TextAction>
        }
      >
        {sessions.length === 1 ? '1 device signed in' : `${sessions.length} devices signed in`}
      </SettingRow>
      {reviewing && (
        <div id="session-list" className="border-b border-rule py-1.5 sm:pl-[132px]">
          <ul>
            {ordered.map((session) => {
              const device = describeDevice(session.device)
              const when = session.current ? 'This browser' : lastUsed(session.last_used_at, now)
              return (
                <li
                  key={session.id}
                  className="flex items-center justify-between gap-3 py-1.5 text-small"
                >
                  <span className="min-w-0">
                    {device} <span className="text-ink-2">· {when}</span>
                  </span>
                  {!session.current && (
                    <TextAction
                      size="sm"
                      disabled={closing !== null}
                      onClick={() => signOut(session, device)}
                    >
                      Sign out
                      <span className="sr-only">
                        {' '}
                        {device}, {when}
                      </span>
                    </TextAction>
                  )}
                </li>
              )
            })}
          </ul>
          <p role="alert" className="py-1.5 text-small text-danger empty:hidden">
            {error}
          </p>
        </div>
      )}
    </>
  )
}
