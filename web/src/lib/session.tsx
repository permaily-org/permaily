import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import * as api from './api.ts'
import { dropLink, hasLink, pendingLink, takeArrival, takeBrokenLink } from './arrival.ts'

export type SessionState =
  /** Asking the server who this is. `link` is true while a sign-in link is being used. */
  | { status: 'loading'; link: boolean }
  /** The server did not answer, so nobody knows yet. */
  | { status: 'unreachable' }
  /** `byChoice` is true when the person signed out here, so nothing takes them back where they were. */
  | { status: 'signedOut'; byChoice: boolean }
  | { status: 'signedIn'; me: api.Me }

/** Why the sign-in link the app was opened with did not sign anyone in. */
export type LinkProblem = 'expired' | 'failed'

type SessionValue = {
  state: SessionState
  linkProblem: LinkProblem | null
  clearLinkProblem: () => void
  /** Asks again after the server could not be reached. */
  retry: () => void
  /** Takes the account the server answered a finished sign-in with. */
  signedIn: (me: api.Me) => void
  /** Ends the session on the server. Throws an ApiError when it could not. */
  signOut: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

export function useSession(): SessionValue {
  const value = useContext(SessionContext)
  if (!value) throw new Error('useSession needs a SessionProvider above it')
  return value
}

/**
 * Who is signed in, known only by asking the server: the session is an HttpOnly cookie, and nothing of it
 * is kept in the page or in storage.
 */
export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>(() => ({
    status: 'loading',
    link: pendingLink() !== null,
  }))
  const [linkProblem, setLinkProblem] = useState<LinkProblem | null>(null)
  // Counts every change made on purpose, so a slower answer to an earlier question cannot undo it.
  const epoch = useRef(0)
  const status = useRef(state.status)
  status.current = state.status

  const load = useCallback(async () => {
    const turn = ++epoch.current
    const link = pendingLink()
    setState({ status: 'loading', link: link !== null })
    if (link) {
      // The link goes first and alone: an answer to /me could drop the cookie this is about to set.
      try {
        const who = await api.finishEmail(link.attempt, { token: link.token })
        dropLink()
        if (turn === epoch.current) setState({ status: 'signedIn', me: who })
        return
      } catch (error) {
        const code = api.errorCode(error)
        if (code === 'network') {
          // The request never arrived, so the link is still good: Try again uses it.
          if (turn === epoch.current) setState({ status: 'unreachable' })
          return
        }
        dropLink()
        setLinkProblem(code === 'invalid_code' ? 'expired' : 'failed')
      }
    } else if (takeBrokenLink()) {
      setLinkProblem('expired')
    }
    try {
      const who = await api.me()
      // Someone already signed in has no use for the news that a link did not work.
      setLinkProblem(null)
      if (turn === epoch.current) setState({ status: 'signedIn', me: who })
    } catch (error) {
      if (turn !== epoch.current) return
      setState(
        api.errorCode(error) === 'unauthorized'
          ? { status: 'signedOut', byChoice: false }
          : { status: 'unreachable' },
      )
    }
  }, [])

  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    void load()
  }, [load])

  // A link pasted over the sign-in screen changes only the fragment, so the page does not load again:
  // it is taken out of the address bar and used all the same.
  useEffect(() => {
    const arrived = () => {
      takeArrival()
      if (hasLink()) void load()
    }
    window.addEventListener('hashchange', arrived)
    return () => window.removeEventListener('hashchange', arrived)
  }, [load])

  // Coming back to the tab asks again: the link may have been opened in another tab, or the session ended there.
  useEffect(() => {
    let asking = false
    const check = async () => {
      if (document.visibilityState !== 'visible' || asking) return
      if (status.current !== 'signedIn' && status.current !== 'signedOut') return
      asking = true
      const turn = epoch.current
      try {
        const who = await api.me()
        if (turn !== epoch.current) return
        setState((now) =>
          now.status === 'signedOut' ||
          (now.status === 'signedIn' && now.me.account.id !== who.account.id)
            ? { status: 'signedIn', me: who }
            : now,
        )
      } catch (error) {
        // Only the server saying so signs anyone out. No answer changes nothing.
        if (turn !== epoch.current || api.errorCode(error) !== 'unauthorized') return
        setState((now) =>
          now.status === 'signedIn' ? { status: 'signedOut', byChoice: false } : now,
        )
      } finally {
        asking = false
      }
    }
    document.addEventListener('visibilitychange', check)
    window.addEventListener('focus', check)
    return () => {
      document.removeEventListener('visibilitychange', check)
      window.removeEventListener('focus', check)
    }
  }, [])

  const value = useMemo<SessionValue>(
    () => ({
      state,
      linkProblem,
      clearLinkProblem: () => setLinkProblem(null),
      retry: () => void load(),
      signedIn: (who) => {
        epoch.current++
        setLinkProblem(null)
        setState({ status: 'signedIn', me: who })
      },
      signOut: async () => {
        try {
          await api.signOut()
        } catch (error) {
          // No session on the server is what signing out wanted.
          if (api.errorCode(error) !== 'unauthorized') throw error
        }
        epoch.current++
        setLinkProblem(null)
        setState({ status: 'signedOut', byChoice: true })
      },
    }),
    [state, linkProblem, load],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
