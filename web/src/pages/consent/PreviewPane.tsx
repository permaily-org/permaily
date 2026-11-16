import { useState } from 'react'
import {
  Button,
  Count as CountMark,
  Headline,
  MailRow,
  TextAction,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'
import { PreviewEyebrow } from './InboxPreview.tsx'
import type { Count } from './preview.ts'

/** The most rows the preview draws: the count goes on past them. */
const rowsShown = 50

function senderOf(message: api.MessageSummary): string {
  const from = message.from[0]
  return from ? from.name || from.address : ''
}

/**
 * How many messages pass the exact conditions for the draft on the screen, at least that many while there is more to
 * count, and the newest of them; and, only when the person asks, the same with the model, which also says what a rule
 * holds back.
 */
export function PreviewPane({
  request,
  draft,
  count,
  onMore,
  onModel,
  onRetry,
}: {
  request: api.AuthorizationRequest
  draft: api.ConsentDraft
  count: Count
  onMore: () => void
  onModel: () => void
  onRetry: () => void
}) {
  const { accountChanged, failed } = useSession()
  const [agreeing, setAgreeing] = useState(false)
  const [agreeError, setAgreeError] = useState<string | null>(null)
  const found = count.messages.length
  const subject = request.parts.includes('subject')
  const complete = !count.next && !count.pending

  // The model's notice is agreed to here only to ask the model for this preview: the approval records its own.
  async function agreeAndAsk() {
    if (agreeing) return
    setAgreeing(true)
    setAgreeError(null)
    try {
      accountChanged(await api.agreeModelNotice(request.model_notice))
      onModel()
    } catch (error) {
      const code = failed(error)
      if (code) setAgreeError(messageFor(code))
    } finally {
      setAgreeing(false)
    }
  }

  if (draft.mailboxes.length === 0) {
    return (
      <>
        <PreviewEyebrow>Preview, run on your own mail</PreviewEyebrow>
        <Headline as="h2" size="display" className="mt-2">
          No mailbox chosen.
        </Headline>
      </>
    )
  }

  if (draft.since.new) {
    return (
      <>
        <PreviewEyebrow>Preview, run on your own mail</PreviewEyebrow>
        <Headline as="h2" size="display" className="mt-2">
          <CountMark tone="bar">0</CountMark>
          <span>emails yet</span>
        </Headline>
        <p className="mt-2.5 text-ink-2">
          New mail only: it reads mail that arrives from when you allow.
        </p>
        <Failure count={count} onRetry={onRetry} />
      </>
    )
  }

  return (
    <>
      <PreviewEyebrow>Preview, run on your own mail</PreviewEyebrow>
      <div role="status" aria-live="polite">
        <Headline as="h2" size="display" className="mt-2">
          {count.pending && found === 0 ? (
            <span>Counting</span>
          ) : (
            <>
              {!complete && <span>At least</span>}
              <CountMark tone={found ? 'shared' : 'bar'}>{found.toLocaleString('en-GB')}</CountMark>
              <span>{found === 1 ? 'email' : 'emails'}</span>
            </>
          )}
        </Headline>
        <p className="mt-2.5 text-ink-2">
          {count.askModel ? 'Pass its conditions, the model asked.' : 'Pass its exact conditions.'}
          {count.pending && found > 0 ? ' Counting on.' : ''}
        </p>
      </div>

      <div className="mt-3.5 flex flex-col gap-1.5 text-small text-ink-2">
        {count.askModel && count.held > 0 && (
          <p>
            {count.held.toLocaleString('en-GB')} held back by your rules:{' '}
            <bdi>{request.vendor.domain}</bdi> isn't told.
          </p>
        )}
        {count.askModel && count.model === 'off' && (
          <p>Permaily can't ask its model now. Nothing it would decide is counted.</p>
        )}
        {count.askModel && count.model === 'limit' && (
          <p>A spend limit held some back from the model. They aren't counted.</p>
        )}
        {!count.askModel && count.undecided > 0 && (
          <p>
            {count.undecided.toLocaleString('en-GB')} more only a model can decide, and your rules
            may hold some back.
          </p>
        )}
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2">
        {count.next && !count.pending && <TextAction onClick={onMore}>Count more</TextAction>}
        {!count.askModel && !count.pending && (
          <TextAction aria-disabled={agreeing} onClick={onModel}>
            Preview with the model
          </TextAction>
        )}
      </div>

      {count.failure === 'model_notice_needed' ? (
        <div role="alert" className="mt-4 max-w-[560px] text-small">
          <p className="text-ink-2">
            Asking the model needs your agreement to what it is sent, above. Agreeing doesn't allow
            this request.
          </p>
          <Button
            className="mt-3"
            variant="secondary"
            size="sm"
            aria-disabled={agreeing}
            onClick={agreeAndAsk}
          >
            Agree and preview with the model
          </Button>
          <p className="mt-2 text-danger empty:hidden">{agreeError}</p>
        </div>
      ) : (
        <Failure count={count} onRetry={onRetry} />
      )}

      {found > 0 && (
        <ul
          aria-label="Emails it would read"
          className="mt-4 overflow-hidden border-t border-rule-strong"
        >
          {count.messages.slice(0, rowsShown).map((message) => (
            <li key={message.id}>
              <MailRow
                kind="shared"
                date={formatDay(message.received_at)}
                sender={senderOf(message)}
                subject={subject ? message.subject : ''}
              />
            </li>
          ))}
        </ul>
      )}
    </>
  )
}

function Failure({ count, onRetry }: { count: Count; onRetry: () => void }) {
  if (!count.failure || count.failure === 'model_notice_needed') return null
  return (
    <div
      role="alert"
      className="mt-4 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-small text-danger"
    >
      <p>The preview couldn't run. {messageFor(count.failure)}</p>
      <TextAction onClick={onRetry}>Try again</TextAction>
    </div>
  )
}
