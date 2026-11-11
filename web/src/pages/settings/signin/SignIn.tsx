import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { SectionHeading, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { useSession } from '../../../lib/session.tsx'
import { passkeysSupported } from '../../../lib/webauthn.ts'
import { messageFor } from '../../auth/messages.ts'
import { PasskeyOnly } from './PasskeyOnly.tsx'
import { Passkeys } from './Passkeys.tsx'
import { Providers } from './Providers.tsx'
import { Sessions } from './Sessions.tsx'
import type { SignInSection } from './shared.ts'

type Loaded = {
  list: api.PasskeyList
  sessions: api.Session[]
  /** The providers that are on, and the ones that sign in to this account. */
  on: api.Provider[]
  linked: api.LinkedProvider[]
}

/**
 * How the account is signed in to, as the server has it: its passkeys, "passkey only" with its recovery
 * codes, the providers linked to it, and the devices signed in. Every change is made on the server and the
 * section is read again after.
 */
export function SignIn() {
  const session = useSession()
  const [loaded, setLoaded] = useState<Loaded | null>(null)
  const [failure, setFailure] = useState<api.ApiErrorCode | null>(null)
  const [said, setSaid] = useState('')
  const [adding, setAdding] = useState(false)
  // A browser without passkeys is not offered to make one.
  const [supported] = useState(passkeysSupported)
  const sectionRef = useRef<HTMLElement>(null)
  const addRef = useRef<HTMLButtonElement>(null)
  // Counts the readings, so a slower answer to an earlier one cannot undo a later one.
  const turn = useRef(0)
  // Null when the session ended or the account is being deleted: the app is on its way to the screen that
  // says so, and there is nothing left to say here.
  const { failed } = session

  const refresh = useCallback(async () => {
    const mine = ++turn.current
    try {
      const [list, sessions, on, linked] = await Promise.all([
        api.passkeys(),
        api.sessions(),
        api.providersOn(),
        api.linkedProviders(),
      ])
      if (mine !== turn.current) return
      setLoaded({ list, sessions, on, linked })
      setFailure(null)
    } catch (error) {
      if (mine !== turn.current) return
      const code = failed(error)
      if (code) setFailure(code)
    }
  }, [failed])

  useEffect(() => {
    void refresh()
  }, [refresh])

  // A row that had focus went with what it showed: focus moves to the section once it is drawn without it,
  // which is after a dialog that was closing has handed focus back to that row.
  const focusWanted = useRef(false)
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when the section is drawn from a new reading
  useEffect(() => {
    if (!focusWanted.current) return
    focusWanted.current = false
    sectionRef.current?.focus()
  }, [loaded])

  const section = useMemo<SignInSection>(
    () => ({
      refresh,
      failed,
      announce: setSaid,
      focusSection: () => {
        focusWanted.current = true
      },
    }),
    [refresh, failed],
  )

  const address = session.state.status === 'signedIn' ? session.state.me.account.address : ''

  return (
    <section ref={sectionRef} tabIndex={-1} aria-labelledby="sign-in" className="outline-none">
      <SectionHeading
        id="sign-in"
        title="Sign-in"
        action={
          loaded && supported ? (
            <TextAction
              ref={addRef}
              aria-expanded={adding}
              onClick={() => setAdding((open) => !open)}
            >
              Add a passkey
            </TextAction>
          ) : undefined
        }
      />
      {loaded ? (
        <>
          <Passkeys
            list={loaded.list}
            supported={supported}
            adding={adding}
            onAddingDone={() => setAdding(false)}
            addRef={addRef}
            section={section}
          />
          <PasskeyOnly list={loaded.list} address={address} section={section} />
          <Providers on={loaded.on} linked={loaded.linked} list={loaded.list} section={section} />
          <Sessions sessions={loaded.sessions} section={section} />
        </>
      ) : (
        !failure && (
          <p role="status" className="border-b border-rule py-2 text-small text-ink-2">
            Loading
          </p>
        )
      )}
      {failure && (
        <div
          role="alert"
          className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-rule py-2 text-small text-danger"
        >
          <p>
            {loaded
              ? "This couldn't be brought up to date. "
              : "Your sign-in settings couldn't load. "}
            {messageFor(failure)}
          </p>
          <TextAction onClick={() => void refresh()}>Try again</TextAction>
        </div>
      )}
      <p role="status" className="sr-only">
        {said}
      </p>
    </section>
  )
}
