import { useState } from 'react'
import { Checkbox, Notice, SectionHeading, TermRow } from '../../components/ui/index.ts'
import type * as api from '../../lib/api.ts'
import { modelNotice, modelNoticeTitle } from '../../lib/model-notice.ts'
import {
  devicesMax,
  mailboxWords,
  partsWords,
  resourceWords,
  Sentence,
  sinceWords,
} from '../connections/words.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import { ChoiceTerm } from './ChoiceDialog.tsx'
import type { Choice } from './data.ts'

const day = 86_400_000

/** The choices of mail since a request without a query offers, and the ones a request with a date allows. */
type Since = 'new' | 'kept' | 'asked' | 'month' | 'year' | 'all'

function sinceOf(choice: Since, request: api.AuthorizationRequest): api.MailSince {
  switch (choice) {
    case 'new':
      return { new: true }
    case 'kept':
      return { new: false, ...(request.draft.since.at ? { at: request.draft.since.at } : {}) }
    case 'asked':
      return { new: false, ...(request.since.at ? { at: request.since.at } : {}) }
    case 'month':
      return { new: false, at: new Date(Date.now() - 30 * day).toISOString() }
    case 'year':
      return { new: false, at: new Date(Date.now() - 365 * day).toISOString() }
    default:
      return { new: false }
  }
}

/** The choice the screen starts on, from where the request's draft starts. */
function startingSince(request: api.AuthorizationRequest): Since {
  const { since } = request.draft
  if (since.new) return 'new'
  if (since.at) return request.query ? 'asked' : 'kept'
  return 'all'
}

/** What the person may choose of the mail since: only narrower than asked for a request with a query. */
function sinceChoices(request: api.AuthorizationRequest): Choice[] {
  if (request.query) {
    if (!request.since.at) return []
    return [
      { value: 'asked', label: sinceWords(request.since), note: 'What the app asked for' },
      { value: 'new', label: 'New mail only', note: 'Mail that arrives from when you allow' },
    ]
  }
  const choices: Choice[] = [
    { value: 'new', label: 'New mail only', note: 'Mail that arrives from when you allow' },
  ]
  if (request.draft.since.at && !request.draft.since.new) {
    choices.push({
      value: 'kept',
      label: sinceWords(request.draft.since),
      note: 'What your connection reads now',
    })
  }
  choices.push(
    { value: 'month', label: 'The last 30 days' },
    { value: 'year', label: 'The last year' },
    { value: 'all', label: 'All the mail Permaily keeps' },
  )
  return choices
}

/**
 * What the request asks for and what approving it does, in "The consent screen"'s order: where the browser goes, what
 * is read (the sentence, or all mail of the mailboxes the person picks), from when, which parts, until when, what never
 * leaves, the model, and what approving does to the client's connection.
 */
export function ConsentTerms({
  request,
  draft,
  onDraft,
  mailboxes,
  snapshot,
}: {
  request: api.AuthorizationRequest
  draft: api.ConsentDraft
  onDraft: (draft: api.ConsentDraft) => void
  mailboxes: ReadonlyMap<string, api.Mailbox>
  snapshot?: api.ConsentSnapshot
}) {
  const [since, setSince] = useState<Since>(() => startingSince(request))
  const choices = sinceChoices(request)
  const name = request.client_name || request.vendor.domain
  const label = (id: string) => mailboxWords(id, mailboxes)

  function pick(id: string, on: boolean) {
    // The draft keeps the order of the request's mailboxes.
    const chosen = request.mailboxes.filter((m) => (m === id ? on : draft.mailboxes.includes(m)))
    onDraft({ ...draft, mailboxes: chosen })
  }

  return (
    <>
      <div className="mt-5 border-t border-rule-strong">
        <TermRow label="Asks for">
          {request.query ? <Sentence sentence={request.query.sentence} /> : 'All mail'}
        </TermRow>
        {request.query ? (
          <TermRow label={request.mailboxes.length === 1 ? 'Mailbox' : 'Mailboxes'}>
            <MailboxList ids={draft.mailboxes} label={label} />
          </TermRow>
        ) : (
          <fieldset className="flex items-start gap-4 border-b border-rule py-[11px]">
            <legend className="sr-only">Mailboxes</legend>
            <div aria-hidden className="w-[84px] shrink-0 text-small text-ink-2">
              Mailboxes
            </div>
            <div className="flex min-w-0 flex-1 flex-col gap-2">
              {request.mailboxes.map((id) => (
                <Checkbox
                  key={id}
                  top
                  checked={draft.mailboxes.includes(id)}
                  onChange={(event) => pick(id, event.target.checked)}
                  label={<bdi className="break-words">{label(id)}</bdi>}
                />
              ))}
            </div>
          </fieldset>
        )}
        {choices.length > 0 ? (
          <ChoiceTerm
            label="Mail since"
            title={`From when ${name} can read`}
            choices={choices}
            value={since}
            display={sinceWords(draft.since)}
            onSave={(value) => {
              const chosen = value as Since
              setSince(chosen)
              onDraft({ ...draft, since: sinceOf(chosen, request) })
            }}
          />
        ) : (
          <TermRow label="Mail since">{sinceWords(draft.since)}</TermRow>
        )}
        <TermRow label="Parts">{partsWords(request.parts)}</TermRow>
        <TermRow label="Through">{resourceWords(request.resource)}</TermRow>
        <TermRow label="Ends">
          {snapshot
            ? `${formatDay(snapshot.ends_at)}, on its own`
            : `In ${request.ends_in_days} days, on its own`}
        </TermRow>
        <TermRow label="Goes back to">
          <bdi className="font-mono text-mono break-all">{request.redirect_host}</bdi>
        </TermRow>
      </div>

      {request.loopback && (
        <div className="mt-4">
          <Notice title="This app runs on your computer.">
            Any program on your computer can say it is <bdi>{name}</bdi>.
          </Notice>
        </div>
      )}

      <Change request={request} snapshot={snapshot} />

      <section aria-labelledby="rules-heading" className="mt-7">
        <SectionHeading id="rules-heading" title="Never leaves" />
        <ul className="flex flex-col">
          {request.rules.map((rule) => (
            <li key={rule.id} className="border-b border-rule py-[11px]">
              {rule.sentence.map((sentence, i) => (
                // biome-ignore lint/suspicious/noArrayIndexKey: a rule's sentences have no identity but their place
                <Sentence key={i} sentence={sentence} />
              ))}
            </li>
          ))}
        </ul>
        <p className="mt-2.5 text-small text-ink-2">
          Your rules apply first, and <bdi>{request.vendor.domain}</bdi> isn't told what they hold
          back.
        </p>
      </section>

      <section aria-labelledby="model-heading" className="mt-7">
        <SectionHeading id="model-heading" title={modelNoticeTitle} />
        <div className="mt-2.5 flex flex-col gap-2.5 text-small text-ink-2">
          {modelNotice.map((paragraph) => (
            <p key={paragraph}>{paragraph}</p>
          ))}
          <p className="text-ink">
            If Permaily can't ask its model (no key, or a spend limit reached), this connection gets
            no mail until it can.
          </p>
        </div>
      </section>
    </>
  )
}

function MailboxList({ ids, label }: { ids: string[]; label: (id: string) => string }) {
  return (
    <span className="flex flex-col gap-0.5">
      {ids.map((id) => (
        <bdi key={id} className="break-words">
          {label(id)}
        </bdi>
      ))}
    </span>
  )
}

/** What approving does to the client's connection, for the draft on the screen. */
function Change({
  request,
  snapshot,
}: {
  request: api.AuthorizationRequest
  snapshot?: api.ConsentSnapshot
}) {
  const vendor = request.vendor.domain
  if (!snapshot) {
    return (
      <p role="status" className="mt-4 text-small text-ink-2">
        Working out what allowing does
      </p>
    )
  }
  if (snapshot.change === 'add_device') {
    const devices = request.connection?.devices.filter((d) => d.state === 'active').length ?? 0
    return (
      <div className="mt-4">
        <Notice tone="neutral" title="This adds another device to your connection.">
          It adds another device to your connection with <bdi>{vendor}</bdi>, which keeps working on
          your other devices. It is on {devices} of {devicesMax} devices.
        </Notice>
      </div>
    )
  }
  if (snapshot.change === 'replace') {
    return (
      <div className="mt-4">
        <Notice title="This replaces your connection.">
          It asks for more than your connection with <bdi>{vendor}</bdi> reads. Allowing replaces
          it, and that connection stops working on every device at once. Denying leaves it as it is.
        </Notice>
      </div>
    )
  }
  return null
}
