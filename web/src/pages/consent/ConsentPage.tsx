import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react'
import { useConfirmed } from '../../components/Confirm.tsx'
import { Logo } from '../../components/Logo.tsx'
import { Button, Headline, Notice, TextLink, Tile } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { MODEL_NOTICE_VERSION } from '../../lib/model-notice.ts'
import {
  dropAuthorization,
  heldAuthorization,
  leaveForVendor,
  watchAuthorization,
} from '../../lib/oauth.ts'
import { useSession } from '../../lib/session.tsx'
import { messageFor } from '../auth/messages.ts'
import { vendorPath } from '../connections/ConnectionsPage.tsx'
import { VendorLine } from '../connections/words.tsx'
import { ConsentTerms } from './ConsentTerms.tsx'
import { PreviewPane } from './PreviewPane.tsx'
import { draftKey, useDraftCount } from './preview.ts'

/** Where the consent screen stands. */
type Phase =
  /** Nothing held: a reload, Back, a restored tab, or the page opened by hand. */
  | { kind: 'none' }
  | { kind: 'starting'; changed?: Reloaded }
  | { kind: 'failed'; code: api.ApiErrorCode }
  | { kind: 'shown'; request: api.AuthorizationRequest; changed?: Reloaded }
  /** The browser is on its way back to the vendor. */
  | { kind: 'leaving'; host: string }
  | { kind: 'ended' }

/** Why the screen loaded the request again: what it showed was no longer so when the person allowed. */
type Reloaded = 'model_notice_changed' | 'consent_changed'

function hostOf(address: string): string {
  try {
    return new URL(address).hostname
  } catch {
    return ''
  }
}

/**
 * The consent screen (ADR 0030, "The consent screen"), on /oauth/authorize, where a vendor sends the browser. The
 * vendor's request was taken out of the address bar before anything rendered (`src/lib/oauth.ts`) and is handed to the
 * server here, in the person's session; the screen shows what the server answered, and the browser leaves only for
 * the address the server answers.
 */
export function ConsentPage() {
  const { failed } = useSession()
  const held = useSyncExternalStore(watchAuthorization, heldAuthorization)
  const [phase, setPhase] = useState<Phase>(() => (held ? { kind: 'starting' } : { kind: 'none' }))
  // The request handed to the server, so that it is handed once however often the page draws.
  const handed = useRef<string | null>(null)

  const start = useCallback(
    async (changed?: Reloaded) => {
      const query = heldAuthorization()
      if (!query) {
        setPhase({ kind: 'none' })
        return
      }
      handed.current = query
      setPhase({ kind: 'starting', ...(changed ? { changed } : {}) })
      try {
        const answer = await api.startAuthorization(query)
        if (handed.current !== query) return
        if (answer.redirect !== undefined) {
          // The vendor's request was refused after its redirect URI was proven: the vendor is told.
          setPhase({ kind: 'leaving', host: hostOf(answer.redirect) })
          if (!leaveForVendor(answer.redirect)) setPhase({ kind: 'failed', code: 'other' })
          return
        }
        if (answer.request) {
          setPhase({ kind: 'shown', request: answer.request, ...(changed ? { changed } : {}) })
        }
      } catch (error) {
        if (handed.current !== query) return
        const code = failed(error)
        if (!code) {
          // The session ended, or the account is closing: the gate says so. Signing in again starts this again.
          handed.current = null
          return
        }
        if (code === 'request_ended') dropAuthorization()
        setPhase({ kind: 'failed', code })
      }
    },
    [failed],
  )

  useEffect(() => {
    if (!held) {
      // Forgotten while it was shown: the page came back from the browser's cache. Leaving, an end and a failure
      // keep their own words.
      setPhase((now) => (now.kind === 'shown' || now.kind === 'starting' ? { kind: 'none' } : now))
      return
    }
    if (handed.current !== held) void start()
  }, [held, start])

  const ended = useCallback(() => {
    dropAuthorization()
    setPhase({ kind: 'ended' })
  }, [])

  const leave = useCallback((address: string) => {
    const host = hostOf(address)
    if (leaveForVendor(address)) setPhase({ kind: 'leaving', host })
    else setPhase({ kind: 'failed', code: 'other' })
  }, [])

  switch (phase.kind) {
    case 'none':
      return (
        <Message title="This request isn't here any more">
          Reloading this page or going back to it forgets a request to connect. Start again from the
          app you were connecting.
        </Message>
      )
    case 'ended':
      return <Message title="That request ended">{messageFor('request_ended')}</Message>
    case 'leaving':
      return (
        <Message title="Going back to the app" status>
          {phase.host ? (
            <>
              Your browser is going back to <bdi className="font-mono text-mono">{phase.host}</bdi>.
            </>
          ) : (
            'Your browser is going back to the app.'
          )}
        </Message>
      )
    case 'starting':
      return (
        <Message title="Reading the request" status>
          {phase.changed ? messageFor(phase.changed) : 'Reading what the app asks for.'}
        </Message>
      )
    case 'failed':
      return (
        <Message
          title={
            phase.code === 'invalid_client' || phase.code === 'request_too_large'
              ? "That request can't be used"
              : "The request couldn't be read"
          }
          alert
          action={
            phase.code === 'invalid_client' ||
            phase.code === 'request_too_large' ||
            phase.code === 'request_ended' ? undefined : (
              <Button className="mt-8" onClick={() => void start()}>
                Try again
              </Button>
            )
          }
        >
          {messageFor(phase.code)}
        </Message>
      )
    case 'shown':
      return (
        <Screen
          key={phase.request.id}
          request={phase.request}
          changed={phase.changed}
          onReload={(why) => void start(why)}
          onEnded={ended}
          onLeave={leave}
        />
      )
  }
}

/** A page of the consent screen that says one thing: the wordmark, a headline, its words and an action. */
function Message({
  title,
  children,
  action,
  status = false,
  alert = false,
}: {
  title: string
  children: ReactNode
  action?: ReactNode
  status?: boolean
  alert?: boolean
}) {
  return (
    <main className="mx-auto w-full max-w-[600px] px-4 pt-6 pb-10 sm:px-8 lg:pt-7">
      <header>
        <Logo />
      </header>
      <Headline size="display-s" className="mt-11">
        {title}
      </Headline>
      <p
        role={alert ? 'alert' : status ? 'status' : undefined}
        className="mt-3 max-w-[488px] text-body text-ink-2"
      >
        {children}
      </p>
      {action}
    </main>
  )
}

/** Why allowing asks the person to show it's them again (ADR 0022, as ADR 0030 amends it). */
const allowProof = {
  says: "A connection reads your mail without you, for as long as it lasts, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
}

/** The request, the draft the person sets, the preview of that draft and the decision. */
function Screen({
  request,
  changed,
  onReload,
  onEnded,
  onLeave,
}: {
  request: api.AuthorizationRequest
  changed?: Reloaded
  onReload: (why: Reloaded) => void
  onEnded: () => void
  onLeave: (address: string) => void
}) {
  const { state, failed } = useSession()
  const confirmed = useConfirmed()
  const [draft, setDraft] = useState<api.ConsentDraft>(request.draft)
  const { count, more, withModel, retry } = useDraftCount(request, draft)
  const [pending, setPending] = useState<'allow' | 'deny' | null>(null)
  const [error, setError] = useState<api.ApiErrorCode | null>(null)
  const [mailboxes, setMailboxes] = useState<ReadonlyMap<string, api.Mailbox>>(new Map())

  // The mailboxes' names, for the picker and the sentence: without them, their identifiers.
  useEffect(() => {
    let live = true
    api.mailboxes().then(
      (list) => {
        if (live) setMailboxes(new Map(list.mailboxes.map((m) => [m.id, m])))
      },
      () => {},
    )
    return () => {
      live = false
    }
  }, [])

  // What approving this draft does, as shown: the request's for its starting draft, a preview's for any other.
  const starting = draftKey(draft) === draftKey(request.draft)
  const snapshot = count.snapshot ?? (starting ? request.snapshot : undefined)
  const noticeShown = request.model_notice === MODEL_NOTICE_VERSION
  const empty = draft.mailboxes.length === 0
  const name = request.client_name || request.vendor.domain
  const account = state.status === 'signedIn' ? state.me.account.address : ''

  async function allow() {
    if (pending || !snapshot || empty || !noticeShown) return
    setPending('allow')
    setError(null)
    try {
      const answer = await confirmed(
        () =>
          api.decideAuthorization(request.id, {
            allow: true,
            draft,
            model_notice: request.model_notice,
            snapshot,
          }),
        allowProof,
      )
      onLeave(answer.redirect)
    } catch (failure) {
      setPending(null)
      if (isCancelled(failure)) return
      decline(failure)
    }
  }

  async function deny() {
    if (pending) return
    setPending('deny')
    setError(null)
    try {
      onLeave((await api.decideAuthorization(request.id, { allow: false })).redirect)
    } catch (failure) {
      setPending(null)
      decline(failure)
    }
  }

  function decline(failure: unknown) {
    const code = failed(failure)
    if (!code) return
    if (code === 'model_notice_changed' || code === 'consent_changed') onReload(code)
    else if (code === 'request_ended') onEnded()
    else setError(code)
  }

  const note = empty
    ? 'Choose at least one mailbox.'
    : !noticeShown
      ? 'This page is older than Permaily. Reload it from the app to read what applies now.'
      : !snapshot
        ? 'Working out what allowing does. You decide once the preview says.'
        : 'You can revoke it at any time. Mail it has already read cannot be recalled.'

  return (
    <main className="flex min-h-svh flex-col xl:flex-row">
      <div className="w-full max-w-[600px] px-4 pt-6 pb-10 sm:px-8 lg:px-14 lg:pt-7 lg:pb-14 xl:sticky xl:top-0 xl:max-h-svh xl:shrink-0 xl:self-start xl:overflow-y-auto">
        <header className="flex flex-wrap items-center justify-between gap-4">
          <Logo />
          {account && (
            <span className="min-w-0 truncate font-mono text-mono text-ink-2">
              <span className="sr-only">Signed in as </span>
              {account}
            </span>
          )}
        </header>

        <div className="mt-11 flex items-center gap-2.5">
          <Tile initials={request.vendor.domain.charAt(0).toUpperCase()} />
          <span className="flex min-w-0 flex-col">
            <span className="sr-only">Who asks: </span>
            <VendorLine vendor={request.vendor} clientName={request.client_name} />
          </span>
        </div>
        <Headline size="display-m" className="mt-6">
          <span>
            <bdi>{request.vendor.domain}</bdi> asks to read your mail.
          </span>
        </Headline>

        {changed && (
          <div className="mt-4">
            <Notice title="This changed since you last read it">{messageFor(changed)}</Notice>
          </div>
        )}

        <ConsentTerms
          request={request}
          draft={draft}
          onDraft={setDraft}
          mailboxes={mailboxes}
          snapshot={snapshot}
        />

        <section aria-labelledby="decision-heading" className="mt-7">
          <h2 id="decision-heading" className="sr-only">
            Your decision
          </h2>
          <p role="alert" className="mb-3 max-w-[488px] text-small text-danger empty:hidden">
            {error ? <DecisionError code={error} request={request} /> : null}
          </p>
          {/* Two actions of the same weight, neither focused when the screen opens: this screen is for reading. */}
          <div className="flex flex-col gap-2.5 sm:flex-row">
            <Button
              variant="secondary"
              className="sm:flex-1"
              aria-disabled={pending !== null || !snapshot || empty || !noticeShown}
              aria-describedby="decision-note"
              onClick={allow}
            >
              Allow <bdi className="sr-only">{name}</bdi>
            </Button>
            <Button
              variant="secondary"
              className="sm:flex-1"
              aria-disabled={pending !== null}
              onClick={deny}
            >
              Deny <bdi className="sr-only">{name}</bdi>
            </Button>
          </div>
          <p
            id="decision-note"
            aria-live="polite"
            className="mt-3.5 max-w-[488px] text-small text-ink-2"
          >
            {pending === 'allow' ? 'Allowing' : pending === 'deny' ? 'Denying' : note}
          </p>
        </section>
      </div>
      <section
        aria-label="Preview"
        className="min-w-0 flex-1 border-t border-rule bg-raised px-4 pt-6 pb-10 sm:px-8 lg:px-14 lg:pt-7 lg:pb-14 xl:border-t-0 xl:border-l xl:px-12"
      >
        <PreviewPane
          request={request}
          draft={draft}
          count={count}
          onMore={more}
          onModel={withModel}
          onRetry={retry}
        />
      </section>
    </main>
  )
}

/** Why the decision failed, with where to go when it is the person's to fix. */
function DecisionError({
  code,
  request,
}: {
  code: api.ApiErrorCode
  request: api.AuthorizationRequest
}) {
  // Going to another page of the app forgets the request (src/lib/oauth.ts): the vendor has to ask again.
  const restart = (
    <span>
      {' '}
      Going there forgets this request: start again from{' '}
      <bdi>{request.client_name || request.vendor.domain}</bdi> afterwards.
    </span>
  )
  if (code === 'too_many_devices' && request.connection) {
    return (
      <>
        {messageFor(code)}{' '}
        <TextLink to={vendorPath(request.vendor.domain, request.connection.id)}>
          Open the connection
        </TextLink>
        {restart}
      </>
    )
  }
  if (code === 'too_many_connections') {
    return (
      <>
        {messageFor(code)} <TextLink to="/connections">Your connections</TextLink>
        {restart}
      </>
    )
  }
  return <>{messageFor(code)}</>
}
