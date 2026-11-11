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
  /**
   * Takes the account as the server answered a change to it: a new name, a deletion asked for or cancelled.
   * Everything that shows the account follows without a reload.
   */
  accountChanged: (account: api.Account) => void
  /**
   * Asks the server again who this is, without a word on screen: what changed elsewhere shows here, and a
   * session that ended leads to the sign-in screen. No answer changes nothing.
   */
  refresh: () => Promise<void>
  /**
   * The code of a failed request, or null when there is nothing to say where it failed: the session ended, or
   * the account is being deleted, and the app is already on its way to the screen that says so.
   */
  failed: (error: unknown) => api.ApiErrorCode | null
  /** Ends the session on the server. Throws an ApiError when it could not. */
  signOut: () => Promise<void>
}

const SessionContext = createContext<SessionValue | null>(null)

/** Whether what shows of an account is the same in both answers. */
function sameAccount(a: api.Account, b: api.Account): boolean {
  return (
    a.id === b.id &&
    a.state === b.state &&
    a.closes_at === b.closes_at &&
    a.name === b.name &&
    a.address === b.address
  )
}

/** Whether the person asked to delete this account: the app then shows that, and nothing else. */
export function isClosing(account: api.Account): boolean {
  return account.state === 'closing'
}

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

  // Asks again without a word on screen. Coming back to the tab does: the link may have been opened in another
  // tab, the session ended there, or the account's deletion asked for or cancelled there.
  const asking = useRef(false)
  const refresh = useCallback(async () => {
    if (asking.current) return
    if (status.current !== 'signedIn' && status.current !== 'signedOut') return
    asking.current = true
    const turn = epoch.current
    try {
      const who = await api.me()
      if (turn !== epoch.current) return
      // Signed in from another tab: the news that a link did not work here is of no use any more.
      if (status.current === 'signedOut') setLinkProblem(null)
      setState((now) =>
        now.status === 'signedOut' ||
        (now.status === 'signedIn' && !sameAccount(now.me.account, who.account))
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
      asking.current = false
    }
  }, [])

  useEffect(() => {
    const check = () => {
      if (document.visibilityState === 'visible') void refresh()
    }
    document.addEventListener('visibilitychange', check)
    window.addEventListener('focus', check)
    return () => {
      document.removeEventListener('visibilitychange', check)
      window.removeEventListener('focus', check)
    }
  }, [refresh])

  // A request was refused because the account is being deleted, asked for in another tab or on another device.
  // The server is asked how the account is, and the answer leads to the screen that says it is closing.
  const closing = state.status === 'signedIn' && isClosing(state.me.account)
  const known = useRef(closing)
  known.current = closing
  useEffect(() => {
    api.onAccountClosing(() => {
      if (status.current === 'signedIn' && !known.current) void load()
    })
    return () => api.onAccountClosing(null)
  }, [load])

  const failed = useCallback(
    (error: unknown) => {
      const code = api.errorCode(error)
      // The request itself told the session that the account is closing: see above.
      if (code === 'account_closing') return null
      if (code !== 'unauthorized') return code
      // The session ended, here or from another device. The app asks who this is, and leads to the sign-in screen.
      void load()
      return null
    },
    [load],
  )

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
      accountChanged: (account) => {
        // A slower answer to an earlier question must not put the account back as it was.
        epoch.current++
        setState((now) =>
          now.status === 'signedIn' && now.me.account.id === account.id
            ? { status: 'signedIn', me: { ...now.me, account } }
            : now,
        )
      },
      refresh,
      failed,
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
    [state, linkProblem, ask, settled, load, acceptLink, refresh, failed],
  )

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>
}
