import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router'
import {
  Headline,
  StepMarker,
  type StepState,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx } from '../../lib/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'
import { Confirmations } from './Confirmations.tsx'
import { FlowFrame } from './MailboxLayout.tsx'
import { CopyButton, LoadState, Support } from './shared.tsx'

/** How often the page asks for the mailbox while it waits for Gmail. */
export const pollEvery = 5_000
/** How long it goes on asking by itself; then the person asks with "Check again". */
export const pollFor = 15 * 60_000

/** Gmail's own path to adding a forwarding address, in its own words. */
const gmailSteps = [
  'In Gmail, open Settings, then See all settings.',
  'Open Forwarding and POP/IMAP, and choose Add a forwarding address.',
  'Paste the address, then choose Next and Proceed.',
]

/**
 * A mailbox's forwarding set-up, three steps on one page: the address to add in Gmail, the code Gmail sends
 * to it, which shows here as soon as it arrives, and turning forwarding on. Gmail forwards all mail: nothing is
 * filtered (ADR 0018).
 */
export function ForwardingSetupPage() {
  const { mailboxId = '' } = useParams()
  const { failed } = useSession()
  const [mailbox, setMailbox] = useState<api.Mailbox | null>(null)
  const [failure, setFailure] = useState<{ code: api.ApiErrorCode; text: string } | null>(null)
  // Since when the page has been asking by itself, and whether it stopped.
  const [since, setSince] = useState(() => Date.now())
  const [stopped, setStopped] = useState(false)
  const [checking, setChecking] = useState(false)

  const load = useCallback(async () => {
    try {
      setMailbox(await api.mailbox(mailboxId))
      setFailure(null)
    } catch (error) {
      const code = failed(error)
      if (code) setFailure({ code, text: messageFor(code) })
    }
  }, [failed, mailboxId])

  useEffect(() => {
    void load()
  }, [load])

  const waiting = mailbox?.state === 'waiting'
  // While Gmail's code or the first mail is awaited, the page asks again every few seconds while it is in view,
  // for fifteen minutes at most.
  useEffect(() => {
    if (!waiting || stopped) return
    let timer = 0
    const ask = async () => {
      if (Date.now() - since >= pollFor) {
        setStopped(true)
        return
      }
      if (document.visibilityState === 'visible') await load()
      timer = window.setTimeout(ask, pollEvery)
    }
    timer = window.setTimeout(ask, pollEvery)
    return () => window.clearTimeout(timer)
  }, [waiting, stopped, since, load])

  async function checkAgain() {
    if (checking) return
    setChecking(true)
    await load()
    setChecking(false)
    setSince(Date.now())
    setStopped(false)
  }

  if (!mailbox) {
    return (
      <FlowFrame>
        <Headline className="mt-7">Forwarding set-up</Headline>
        <div className="mt-7 max-w-2xl">
          {failure?.code === 'not_found' ? (
            <p className="text-body text-ink-2">
              That mailbox isn't there any more. <TextLink to="/accounts">Your mailboxes</TextLink>
            </p>
          ) : (
            <LoadState
              failure={failure?.text ?? null}
              what="The mailbox"
              onRetry={() => void load()}
            />
          )}
        </div>
      </FlowFrame>
    )
  }

  if (mailbox.state === 'unreadable') {
    return (
      <FlowFrame>
        <Headline className="mt-7">Forwarding set-up</Headline>
        <p className="mt-7 max-w-2xl text-body text-ink-2">
          This mailbox can't be read: it accepts no mail. Disconnect it in{' '}
          <TextLink to="/accounts">your mailboxes</TextLink>, and write to <Support />.
        </p>
      </FlowFrame>
    )
  }

  const receiving = mailbox.state === 'receiving' || Boolean(mailbox.receiving_since)
  const confirmed = mailbox.confirmations.length > 0
  const step1: StepState = receiving || confirmed ? 'done' : 'current'
  const step2: StepState = receiving ? 'done' : confirmed ? 'current' : 'next'
  const step3: StepState = receiving ? 'done' : 'next'
  return (
    // The heading stands where it stood while the mailbox loaded, so the focus it took stays.
    <FlowFrame>
      <Headline className="mt-7">Forward your Gmail here</Headline>
      <div className="flex flex-col gap-10 xl:flex-row xl:gap-14">
        <div className="min-w-0 flex-1">
          <p className="mt-2 text-body text-ink-2">
            {mailbox.label ? <span className="font-medium text-ink">{mailbox.label}. </span> : null}
            {receiving
              ? mailbox.receiving_since
                ? `Receiving since ${formatDay(mailbox.receiving_since)}.`
                : 'Receiving.'
              : 'Three steps, about three minutes in Gmail.'}
          </p>
          {mailbox.state === 'paused' && (
            <p className="mt-4 max-w-2xl rounded-sm bg-attention-bg px-3 py-2.5 text-body text-attention">
              Paused by Permaily: it accepts no new mail until Permaily resumes it. Write to{' '}
              <Support /> to ask why.
            </p>
          )}

          <ol className="mt-7 border-t border-rule">
            <Step number={1} state={step1} title="Add this address in Gmail">
              <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1 bg-sunken px-3 py-2.5">
                <span className="min-w-0 font-mono text-body wrap-anywhere select-all">
                  {mailbox.address}
                </span>
                <CopyButton value={mailbox.address} what="the address" />
              </div>
              <ol className="mt-3 flex list-decimal flex-col gap-1 pl-5 text-body text-ink-2">
                {gmailSteps.map((step) => (
                  <li key={step}>{step}</li>
                ))}
              </ol>
            </Step>

            <Step number={2} state={step2} title="Enter Gmail's code">
              <p className="text-body text-ink-2">
                Gmail sends a code to the address. It shows here: type it in Gmail, then choose
                Verify.
              </p>
              {confirmed ? (
                <div className="mt-3">
                  <Confirmations confirmations={mailbox.confirmations} />
                </div>
              ) : stopped ? (
                <div className="mt-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <p role="status" className="text-small text-ink-2">
                    Nothing from Gmail yet.
                  </p>
                  <TextAction size="sm" aria-disabled={checking} onClick={checkAgain}>
                    Check again
                  </TextAction>
                </div>
              ) : (
                !receiving && (
                  <p role="status" className="mt-3 text-small text-ink-2">
                    Waiting for Gmail's message. This page checks every few seconds.
                  </p>
                )
              )}
            </Step>

            <Step number={3} state={step3} title="Turn forwarding on">
              <p className="text-body text-ink-2">
                In Forwarding and POP/IMAP, choose Forward a copy of incoming mail to{' '}
                <span className="font-mono text-small text-ink wrap-anywhere">
                  {mailbox.address}
                </span>
                , keep Gmail's copy in the Inbox, then choose Save Changes.
              </p>
              <p role="status" className={cx('mt-3 text-body', receiving && 'text-ok')}>
                {receiving
                  ? mailbox.receiving_since
                    ? `Receiving since ${formatDay(mailbox.receiving_since)}.`
                    : 'Receiving.'
                  : 'Waiting for the first mail.'}
              </p>
              {receiving && (
                <TextLink
                  to={`/inbox/${encodeURIComponent(mailbox.id)}`}
                  size="sm"
                  className="mt-2"
                >
                  Open the inbox
                </TextLink>
              )}
            </Step>
          </ol>

          <p className="mt-10">
            <TextLink to="/accounts">{receiving ? 'Done' : 'Finish later'}</TextLink>
          </p>
        </div>
        <WhatToExpect />
      </div>
    </FlowFrame>
  )
}

function Step({
  number,
  state,
  title,
  children,
}: {
  number: number
  state: StepState
  title: string
  children: ReactNode
}) {
  return (
    <li
      className="flex gap-4 border-b border-rule pt-4.5 pb-5 sm:gap-5"
      aria-current={state === 'current' ? 'step' : undefined}
    >
      <StepMarker state={state} number={number} />
      <div className="min-w-0 flex-1">
        <h2 className={cx('text-title font-medium', state === 'next' && 'text-ink-2')}>{title}</h2>
        <div className="mt-2">{children}</div>
      </div>
    </li>
  )
}

const expectations = [
  {
    title: 'Only new mail',
    detail:
      'Mail forwarded from now on reaches Permaily. Nothing from before forwarding starts is here.',
  },
  {
    title: 'Gmail shows you a notice',
    detail: 'For a week after forwarding starts, to keep you informed. That is expected.',
  },
  {
    title: 'Gmail keeps its copy',
    detail: 'Forwarding sends a copy. Your Gmail inbox stays as it is.',
  },
  {
    title: 'You can stop in Gmail',
    detail: "Turn forwarding off in Gmail at any time: you don't need Permaily to do it.",
  },
]

function WhatToExpect() {
  return (
    <aside
      aria-label="What to expect"
      className="w-full shrink-0 border-t border-rule pt-6 xl:mt-9 xl:w-85 xl:border-t-0 xl:border-l xl:pt-0 xl:pl-8"
    >
      <h2 className="text-title font-medium">What to expect</h2>
      <ul className="mt-3 border-t border-rule-strong">
        {expectations.map((item) => (
          <li key={item.title} className="border-b border-rule py-2.5">
            <p className="text-body font-medium">{item.title}</p>
            <p className="text-small text-ink-2">{item.detail}</p>
          </li>
        ))}
      </ul>
    </aside>
  )
}
