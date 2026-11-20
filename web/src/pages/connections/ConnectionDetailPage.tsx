import { ChevronLeft } from 'lucide-react'
import { type ReactNode, type RefObject, useCallback, useEffect, useRef, useState } from 'react'
import { NavLink, useParams } from 'react-router'
import { useConfirmed } from '../../components/Confirm.tsx'
import {
  Button,
  Dialog,
  Headline,
  Notice,
  SectionHeading,
  TermRow,
  TextAction,
  TextLink,
  Tile,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { MODEL_NOTICE_VERSION, modelNotice, modelNoticeTitle } from '../../lib/model-notice.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { LoadState } from '../accounts/shared.tsx'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'
import { useLimitedConnections } from './ConnectionsLayout.tsx'
import { ConnectionRow, useConnections, vendorPath } from './ConnectionsPage.tsx'
import {
  endWords,
  formatWhen,
  mailboxWords,
  partsWords,
  resourceWords,
  Sentence,
  sinceWords,
  VendorLine,
  webhookFailureWords,
  webhookLines,
  webhookNeverWords,
  webhookStateWords,
} from './words.tsx'

/**
 * A vendor's page, by its domain. /connections/:vendorId/overview is its overview: its connections, active and
 * ended. Each connection opens in its own tab, /connections/:vendorId/:connectionId, read from the server.
 */
export function ConnectionDetailPage() {
  const { vendorId = '', connectionId } = useParams()
  const { list, failure, load, replace } = useConnections()
  if (list === null) {
    return (
      <div className={pagePadding}>
        <Headline>Connections</Headline>
        <div className="mt-6 max-w-[880px]">
          <LoadState failure={failure} what="Your connections" onRetry={() => void load()} />
        </div>
      </div>
    )
  }
  const siblings = list.filter((c) => c.vendor.domain === vendorId)
  const vendor = siblings[0]?.vendor
  if (!vendor) return <NotFound />
  if (!connectionId) {
    return (
      <VendorFrame vendor={vendor} siblings={siblings}>
        <Headline className="mt-6">
          {siblings.length} {siblings.length === 1 ? 'connection' : 'connections'}.
        </Headline>
        <ul className="mt-6 max-w-[880px] border-t border-rule-strong">
          {siblings.map((c) => (
            <ConnectionRow key={c.id} connection={c} />
          ))}
        </ul>
      </VendorFrame>
    )
  }
  if (!siblings.some((c) => c.id === connectionId)) return <NotFound />
  return (
    <VendorFrame vendor={vendor} siblings={siblings}>
      <Detail key={connectionId} id={connectionId} onChanged={replace} />
    </VendorFrame>
  )
}

/** The way back, the vendor and its tabs: Overview first, then one per connection. */
function VendorFrame({
  vendor,
  siblings,
  children,
}: {
  vendor: api.ConnectionVendor
  siblings: api.Connection[]
  children: ReactNode
}) {
  const tab = (isActive: boolean, dim = false) =>
    cx(
      'shrink-0 whitespace-nowrap border-b-2 pb-2.5 text-body',
      isActive
        ? 'border-ink font-medium text-ink'
        : cx('border-transparent hover:text-ink', dim ? 'text-ink-3' : 'text-ink-2'),
      focusRing,
    )
  return (
    <div className={pagePadding}>
      <div className="flex items-center gap-1">
        <ChevronLeft aria-hidden size={14} className="text-ink-2" />
        <TextLink to="/connections">Connections</TextLink>
      </div>
      <div className="mt-4 flex items-center gap-2.5">
        <Tile initials={vendor.domain.charAt(0).toUpperCase()} />
        <VendorLine vendor={vendor} clientName="" />
      </div>
      <nav
        aria-label={`Connections of ${vendor.domain}`}
        className="mt-5 -mb-px flex min-w-0 gap-6 overflow-x-auto border-b border-rule [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        <NavLink to={vendorPath(vendor.domain)} end className={({ isActive }) => tab(isActive)}>
          Overview
        </NavLink>
        {siblings.map((c) => (
          <NavLink
            key={c.id}
            to={vendorPath(vendor.domain, c.id)}
            className={({ isActive }) => tab(isActive, c.state === 'ended')}
          >
            <bdi>{c.client_name || vendor.domain}</bdi>
            {c.state === 'ended' && <span className="sr-only">, ended</span>}
          </NavLink>
        ))}
      </nav>
      {children}
    </div>
  )
}

/** Why renewing asks the person to show it's them again: the connection reads mail without them for longer. */
const renewProof = {
  says: "Renewing lets this connection read your mail without you for longer, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
}

/** One connection: what it reads, its devices, whether it gets mail now, and revoking or renewing it. */
function Detail({ id, onChanged }: { id: string; onChanged: (c: api.Connection) => void }) {
  const { failed } = useSession()
  const confirmed = useConfirmed()
  const limited = useLimitedConnections()
  const [connection, setConnection] = useState<api.Connection | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [mailboxes, setMailboxes] = useState<ReadonlyMap<string, api.Mailbox>>(new Map())
  const [asking, setAsking] = useState<'revoke' | 'renew' | 'webhook' | null>(null)
  const [pending, setPending] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const headingRef = useRef<HTMLHeadingElement>(null)
  const devicesRef = useRef<HTMLHeadingElement>(null)
  const webhookRef = useRef<HTMLHeadingElement>(null)
  const focusNext = useRef<Focus | null>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setConnection(await api.connection(id))
    } catch (problem) {
      const code = failed(problem)
      if (code) setFailure(messageFor(code))
    }
  }, [id, failed])

  useEffect(() => {
    void load()
  }, [load])

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

  // After a change the page is redrawn: focus goes where the change shows, not back to a control that is gone.
  useEffect(() => {
    if (!focusNext.current) return
    const target = { heading: headingRef, devices: devicesRef, webhook: webhookRef }[
      focusNext.current
    ].current
    focusNext.current = null
    target?.focus()
  })

  function took(next: api.Connection, focus: Focus, words: string) {
    setConnection(next)
    onChanged(next)
    setSaid(words)
    focusNext.current = focus
  }

  async function act(
    what: string,
    run: () => Promise<api.Connection>,
    focus: Focus,
    words: (c: api.Connection) => string,
  ) {
    if (pending) return
    setPending(what)
    setError(null)
    try {
      const next = await run()
      setAsking(null)
      took(next, focus, words(next))
    } catch (problem) {
      setAsking(null)
      if (isCancelled(problem)) return
      const code = failed(problem)
      if (!code) return
      setError(messageFor(code))
      // What the page showed is no longer so: it shows it again.
      if (
        code === 'consent_changed' ||
        code === 'connection_ended' ||
        code === 'model_notice_changed'
      ) {
        void load()
      }
    } finally {
      setPending(null)
    }
  }

  if (!connection) {
    return (
      <div className="mt-6 max-w-[880px]">
        <LoadState failure={failure} what="This connection" onRetry={() => void load()} />
      </div>
    )
  }

  const c = connection
  const active = c.state === 'active'
  const name = c.client_name || c.vendor.domain
  const canRenew = active && !limited

  return (
    <>
      <div className="mt-6 flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h1
            ref={headingRef}
            tabIndex={-1}
            className="text-display-s font-medium md:text-display outline-none"
          >
            <bdi>{name}</bdi> {active ? 'reads your mail.' : 'no longer reads your mail.'}
          </h1>
          <p className="mt-2.5 font-mono text-mono text-ink-2">
            {active
              ? `allowed ${formatDay(c.made_at)}, ends ${formatDay(c.ends_at)}`
              : `ended ${formatDay(c.ended_at ?? c.ends_at)}`}
          </p>
        </div>
        {active && (
          <div className="flex flex-wrap gap-2.5">
            {canRenew && (
              <Button variant="secondary" size="sm" onClick={() => setAsking('renew')}>
                Renew <span className="sr-only">{name}</span>
              </Button>
            )}
            <Button variant="danger" size="sm" onClick={() => setAsking('revoke')}>
              Revoke <span className="sr-only">{name}</span>
            </Button>
          </div>
        )}
      </div>

      <p role="status" className="sr-only">
        {said}
      </p>
      <p role="alert" className="mt-3 max-w-[560px] text-small text-danger empty:hidden">
        {error}
      </p>

      <div className="mt-4 flex max-w-[880px] flex-col gap-3">
        {!active && (
          <Notice tone="neutral" title={`${endWords(c.end_reason)}.`}>
            Permaily can't recall what it already read.
            {c.replaced_by && (
              <>
                {' '}
                <TextLink to={vendorPath(c.vendor.domain, c.replaced_by)}>
                  See the one that replaced it
                </TextLink>
              </>
            )}
          </Notice>
        )}
        {active && c.delivery === 'model_unavailable' && (
          <Notice title="It gets no mail now.">
            Permaily can't ask its model (no key, or a spend limit reached), so nothing leaves until
            it can.
          </Notice>
        )}
        {active && c.delivery === 'model_notice_changed' && (
          <Notice title="It gets no mail until you renew it.">
            What Permaily sends to its model changed since you allowed it.{' '}
            {limited
              ? 'Agree to the new terms first, then renew it.'
              : 'Renew it to agree to what applies now.'}
          </Notice>
        )}
      </div>

      <div className="mt-6 grid max-w-[880px] grid-cols-1 gap-9">
        <section aria-labelledby="reads-heading">
          <SectionHeading id="reads-heading" title="What it reads" />
          <TermRow label="Reads">
            {c.query ? <Sentence sentence={c.query.sentence} /> : 'All mail'}
          </TermRow>
          <TermRow label={c.mailboxes.length === 1 ? 'Mailbox' : 'Mailboxes'}>
            <span className="flex flex-col gap-0.5">
              {c.mailboxes.map((m) => (
                <bdi key={m} className="break-words">
                  {mailboxWords(m, mailboxes)}
                </bdi>
              ))}
            </span>
          </TermRow>
          <TermRow label="Mail since">{sinceWords(c.since)}</TermRow>
          <TermRow label="Parts">{partsWords(c.parts)}</TermRow>
          <TermRow label="Through">{resourceWords(c.resource)}</TermRow>
          <TermRow label="Calls itself">
            <bdi className="break-words">{c.client_name}</bdi>
          </TermRow>
        </section>

        {c.webhook && (
          <WebhookSection
            webhook={c.webhook}
            active={active}
            name={name}
            headingRef={webhookRef}
            onTurnOff={() => setAsking('webhook')}
          />
        )}

        <section aria-labelledby="devices-heading">
          <div className="flex items-center justify-between gap-4 border-b border-rule-strong pb-2.5">
            <h2
              id="devices-heading"
              ref={devicesRef}
              tabIndex={-1}
              className="text-title font-medium outline-none"
            >
              Devices
            </h2>
          </div>
          <ul>
            {c.devices.map((device, i) => (
              <DeviceRow
                key={device.id}
                device={device}
                number={i + 1}
                revocable={active && device.state === 'active'}
                pending={pending === device.id}
                onRevoke={() =>
                  void act(
                    device.id,
                    () => api.revokeDevice(c.id, device.id),
                    'devices',
                    () => `Device ${i + 1} revoked. The others keep working.`,
                  )
                }
              />
            ))}
          </ul>
        </section>
      </div>

      <Dialog
        open={asking === 'revoke'}
        onClose={() => setAsking(null)}
        labelledBy="revoke-heading"
      >
        <h2
          id="revoke-heading"
          data-dialog-focus
          tabIndex={-1}
          className="text-title font-medium outline-none"
        >
          Revoke <bdi>{name}</bdi>?
        </h2>
        <p className="mt-3 text-ink-2">
          It stops on every device at once. Permaily can't recall what it already read.
        </p>
        <div className="mt-6 flex flex-wrap gap-2.5">
          <Button
            variant="danger"
            aria-disabled={pending !== null}
            onClick={() =>
              void act(
                'revoke',
                () => api.revokeConnection(c.id),
                'heading',
                () => `${name} revoked.`,
              )
            }
          >
            Revoke
          </Button>
          <Button variant="secondary" onClick={() => setAsking(null)}>
            Cancel
          </Button>
        </div>
      </Dialog>

      {c.webhook && (
        <Dialog
          open={asking === 'webhook'}
          onClose={() => setAsking(null)}
          labelledBy="webhook-off-heading"
        >
          <h2
            id="webhook-off-heading"
            data-dialog-focus
            tabIndex={-1}
            className="text-title font-medium outline-none"
          >
            Stop telling <bdi>{c.webhook.host}</bdi>?
          </h2>
          <p className="mt-3 text-ink-2">
            <bdi>{name}</bdi> is no longer told when there's mail to fetch. This is final for this
            connection: only a new request from the app turns it on again. It can still read your
            mail when it asks.
          </p>
          <div className="mt-6 flex flex-wrap gap-2.5">
            <Button
              variant="danger"
              aria-disabled={pending !== null}
              onClick={() =>
                void act(
                  'webhook',
                  () => api.turnWebhookOff(c.id, c.version),
                  'webhook',
                  (next) => `Turned off. ${next.webhook?.host ?? 'The app'} is no longer told.`,
                )
              }
            >
              Turn off
            </Button>
            <Button variant="secondary" onClick={() => setAsking(null)}>
              Cancel
            </Button>
          </div>
        </Dialog>
      )}

      {canRenew && (
        <Dialog
          open={asking === 'renew'}
          onClose={() => setAsking(null)}
          labelledBy="renew-heading"
        >
          <h2
            id="renew-heading"
            data-dialog-focus
            tabIndex={-1}
            className="text-title font-medium outline-none"
          >
            Renew <bdi>{name}</bdi>?
          </h2>
          <p className="mt-3 text-ink-2">
            It reads as it does now for as many days again as you allowed it, from today, under what
            applies now:
          </p>
          <h3 className="mt-4 font-medium">{modelNoticeTitle}</h3>
          <div className="mt-2 flex flex-col gap-2 text-small text-ink-2">
            {modelNotice.map((paragraph) => (
              <p key={paragraph}>{paragraph}</p>
            ))}
          </div>
          <div className="mt-6 flex flex-wrap gap-2.5">
            <Button
              aria-disabled={pending !== null}
              onClick={() =>
                void act(
                  'renew',
                  () =>
                    confirmed(
                      () =>
                        api.renewConnection(c.id, {
                          model_notice: MODEL_NOTICE_VERSION,
                          version: c.version,
                        }),
                      renewProof,
                    ),
                  'heading',
                  (next) => `Renewed. It ends ${formatDay(next.ends_at)}.`,
                )
              }
            >
              Renew
            </Button>
            <Button variant="secondary" onClick={() => setAsking(null)}>
              Cancel
            </Button>
          </div>
        </Dialog>
      )}
    </>
  )
}

/** Where focus goes once a change shows. */
type Focus = 'heading' | 'devices' | 'webhook'

/**
 * A connection's webhook (ADR 0039, "Retries and giving up"): whom it tells and when, its state, the next run in the
 * person's time zone, when a notice was last taken, why calls fail in fixed words, and turning it off. Words drafted
 * for the designer's review.
 */
function WebhookSection({
  webhook,
  active,
  name,
  headingRef,
  onTurnOff,
}: {
  webhook: api.ConnectionWebhook
  active: boolean
  name: string
  headingRef: RefObject<HTMLHeadingElement | null>
  onTurnOff: () => void
}) {
  const zone = webhook.time_zone ?? ''
  const on = active && webhook.state === 'on'
  return (
    <section aria-labelledby="webhook-heading">
      <div className="flex flex-wrap items-center justify-between gap-4 border-b border-rule-strong pb-2.5">
        <h2
          id="webhook-heading"
          ref={headingRef}
          tabIndex={-1}
          className="text-title font-medium outline-none"
        >
          Tells the app
        </h2>
        {on && (
          <Button variant="secondary" size="sm" onClick={onTurnOff}>
            Turn off <span className="sr-only">telling {webhook.host}</span>
          </Button>
        )}
      </div>
      {on && webhook.failing_since && (
        <div className="mt-3">
          <Notice title={`Calls have failed since ${formatWhen(webhook.failing_since, zone)}.`}>
            <bdi>{webhookFailureWords(webhook)}</bdi>. Permaily keeps trying for 72 hours for each
            notice. <bdi>{name}</bdi> can still read your mail when it asks.
          </Notice>
        </div>
      )}
      <TermRow label="Calls">
        <bdi className="font-mono text-mono break-all">{webhook.host}</bdi>
      </TermRow>
      <TermRow label="When">
        <span className="flex flex-col gap-1">
          {webhookLines(webhook, zone).map((line) => (
            <bdi key={line} className="break-words">
              {line}
            </bdi>
          ))}
          <span className="text-small text-ink-2">
            <bdi>{webhookNeverWords(webhook.host)}</bdi>
          </span>
        </span>
      </TermRow>
      <TermRow label="State">
        {active ? webhookStateWords(webhook) : 'Ended with the connection'}
      </TermRow>
      {on && webhook.schedule && webhook.next_at && (
        <TermRow label="Next">
          {formatWhen(webhook.next_at, zone)}
          {zone && <span className="text-ink-2"> ({zone})</span>}
        </TermRow>
      )}
      <TermRow label="Last told">
        {webhook.last_sent_at ? formatWhen(webhook.last_sent_at, zone) : 'Not yet'}
      </TermRow>
    </section>
  )
}

function DeviceRow({
  device,
  number,
  revocable,
  pending,
  onRevoke,
}: {
  device: api.ConnectionDevice
  number: number
  revocable: boolean
  pending: boolean
  onRevoke: () => void
}) {
  return (
    <li className="flex flex-wrap items-baseline gap-x-4 gap-y-1 border-b border-rule py-[11px]">
      <span className="font-medium">Device {number}</span>
      <span className="font-mono text-mono text-ink-2">
        {device.state === 'active'
          ? `since ${formatDay(device.made_at)}`
          : `ended ${formatDay(device.ended_at ?? device.made_at)}`}
      </span>
      <span className="min-w-0 flex-1 text-small text-ink-2">
        {device.state === 'active'
          ? device.narrower
            ? `Reads less than the connection: ${partsWords(device.parts).toLowerCase()}, within its own scope`
            : partsWords(device.parts)
          : endWords(device.end_reason)}
      </span>
      {revocable && (
        <TextAction tone="danger" aria-disabled={pending} onClick={onRevoke}>
          Revoke<span className="sr-only"> device {number}</span>
        </TextAction>
      )}
    </li>
  )
}

function NotFound() {
  return (
    <div className={pagePadding}>
      <Headline size="display-s">We can't find that connection.</Headline>
      <div className="mt-6">
        <TextLink to="/connections">Back to connections</TextLink>
      </div>
    </div>
  )
}
