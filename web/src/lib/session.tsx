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
  /** Asking the server who this is. `link` is true while it is also asked whose sign-in a link is. */
  | { status: 'loading'; link: boolean }
  /** The server did not answer, so nobody knows yet. */
  | { status: 'unreachable' }
  /** `byChoice` is true when the person signed out here, so nothing takes them back where they were. */
  | { status: 'signedOut'; byChoice: boolean }
  | { status: 'signedIn'; me: api.Me }

/** Why the sign-in link the app was opened with did not sign anyone in. */
export type LinkProblem = 'expired' | 'failed'

/**
 * A sign-in link that waits for the person's word. Opening a link signs nobody in: someone may have started
 * a sign-in for their own account and sent its link, to have this person act inside that account.
 * `address` is the server's answer, never anything read from the link.
 */
export type LinkAsk = { address: string }

type SessionValue = {
  state: SessionState
  linkProblem: LinkProblem | null
  clearLinkProblem: () => void
  /** The link the app was opened with, while the person has not said whether to sign in with it. */
  linkAsk: LinkAsk | null
  /**
   * Signs in with the link, because the person said so. Throws an ApiError when the server gave no answer:
   * the link is kept and the question stays. Any other failure ends the question and sets `linkProblem`.
   */
  acceptLink: () => Promise<void>
  /** Forgets the link and signs nobody in. Whoever was signed in stays so. */
  declineLink: () => void
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
  const [ask, setAsk] = useState<LinkAsk | null>(null)
  // Counts every change made on purpose, so a slower answer to an earlier question cannot undo it.
  const epoch = useRef(0)
  const status = useRef(state.status)
  status.current = state.status
  const asked = useRef(ask)
  asked.current = ask

  const load = useCallback(async () => {
    const turn = ++epoch.current
    const link = pendingLink()
    setAsk(null)
    setState({ status: 'loading', link: link !== null })
    let pending: LinkAsk | null = null
    if (link) {
      // The link signs nobody in by being opened. The server says whose sign-in it is, and ends nothing.
      try {
        pending = await api.lookEmail(link.attempt, link.token)
        if (turn !== epoch.current) return
      } catch (error) {
        if (turn !== epoch.current) return
        const code = api.errorCode(error)
        if (code === 'network') {
          // The question never arrived, so the link is as good as it was: Try again uses it.
          setState({ status: 'unreachable' })
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
      if (turn !== epoch.current) return
      // Someone already signed in has no use for the news that a link did not work.
      setLinkProblem(null)
      // In the account of the link's address already, there is nothing to ask: see `settled` below.
      setAsk(pending)
      setState({ status: 'signedIn', me: who })
    } catch (error) {
      if (turn !== epoch.current) return
      if (api.errorCode(error) === 'unauthorized') {
        setAsk(pending)
        setState({ status: 'signedOut', byChoice: false })
      } else {
        // The link stays in memory, so Try again asks about it again.
        setState({ status: 'unreachable' })
      }
    }
  }, [])

  // The browser is in the account of the link's address already, here or since another tab signed in:
  // the app opens, nobody is signed in again and the link is forgotten unused.
  const settled =
    ask !== null && state.status === 'signedIn' && state.me.account.address === ask.address
  useEffect(() => {
    if (!settled) return
    dropLink()
    setAsk(null)
  }, [settled])

  const acceptLink = useCallback(async () => {
    const link = pendingLink()
    const pending = asked.current
    if (!link || !pending) return
    try {
      const who = await api.finishEmail(link.attempt, { token: link.token })
      // Another link arrived meanwhile: that one is being asked about, and its answer shows who is signed in.
      if (pendingLink() !== link) return
      dropLink()
      epoch.current++
      setAsk(null)
      setLinkProblem(null)
      setState({ status: 'signedIn', me: who })
      return
    } catch (error) {
      const code = api.errorCode(error)
      // No answer: the link is kept, and the question stays for another try.
      if (code === 'network') throw error
      if (pendingLink() !== link) return
      dropLink()
      const problem: LinkProblem = code === 'invalid_code' ? 'expired' : 'failed'
      const turn = ++epoch.current
      // An earlier try may have signed in and lost its answer on the way: ask before saying it did not work.
      try {
        const who = await api.me()
        if (turn !== epoch.current) return
        setAsk(null)
        setLinkProblem(who.account.address === pending.address ? null : problem)
        setState({ status: 'signedIn', me: who })
      } catch (failure) {
        if (turn !== epoch.current) return
        setAsk(null)
        setLinkProblem(problem)
        // Only the server saying so signs anyone out. No answer changes nothing.
        if (api.errorCode(failure) === 'unauthorized') {
          setState({ status: 'signedOut', byChoice: false })
        }
      }
    }
  }, [])

  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    void load()
  }, [load])

  // A link pasted over the sign-in screen changes only the fragment, so the page does not load again:
  // it is taken out of the address bar and asked about all the same.
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
        // Signed in from another tab: the news that a link did not work here is of no use any more.
        if (status.current === 'signedOut') setLinkProblem(null)
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
      linkAsk: settled ? null : ask,
      acceptLink,
      declineLink: () => {
        dropLink()
        setAsk(null)
      },
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
        // A link that waited for an answer is forgotten with the session.
        dropLink()
        setAsk(null)
        setLinkProblem(null)
        setState({ status: 'signedOut', byChoice: true })
      },
    }),
    [state, linkProblem, ask, settled, load, acceptLink],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
