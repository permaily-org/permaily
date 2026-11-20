import { useEffect, useId, useRef, useState } from 'react'
import { Button, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { messageFor } from '../auth/messages.ts'
import { Select } from './CriteriaEditor.tsx'
import { asLeavingUnavailable, partWords, shapeWords } from './words.ts'

/**
 * The editor's preview (ADR 0040, "Previews and consent"): a page of a mailbox's mail the draft applies to, and for a
 * rule that takes details out, one message as it would leave. The person's own, which decides nothing; the model is
 * asked only when they ask, under the rules' notice.
 */
export function RulePreview({
  draft,
  mailboxes,
  agreed,
}: {
  draft: api.RuleDraft
  mailboxes: api.Mailbox[]
  agreed: boolean
}) {
  const { failed } = useSession()
  const titleId = useId()
  const readable = mailboxes.filter((m) => m.state !== 'unreadable')
  const [mailbox, setMailbox] = useState('')
  const [page, setPage] = useState<api.RulePreviewPage | null>(null)
  const [askedModel, setAskedModel] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [removal, setRemoval] = useState<{ id: string; shown: api.RuleRemovalPreview } | null>(null)
  const draftKey = JSON.stringify(draft)
  const chosen = mailbox || readable[0]?.id || ''

  // A page shows the draft and the mailbox it was asked for: a change of either clears it, and an answer still on its way
  // for the one before is dropped.
  const shownKey = `${chosen}\n${draftKey}`
  const asked = useRef(0)
  // biome-ignore lint/correctness/useExhaustiveDependencies: what the draft and mailbox are, not their identity, clear the page
  useEffect(() => {
    asked.current += 1
    setPage(null)
    setRemoval(null)
    setAskedModel(false)
    setError(null)
    setPending(false)
  }, [shownKey])

  /**
   * Asks the server, and gives its answer to `then` only if the draft and the mailbox are still the ones asked for. A
   * refusal is said in `refused`'s words when it has some for its code, or in the code's own.
   */
  async function ask<T>(
    question: () => Promise<T>,
    then: (answer: T) => void,
    refused?: (code: api.ApiErrorCode) => string | undefined,
  ) {
    if (pending) return
    const ticket = asked.current
    setPending(true)
    setError(null)
    try {
      const answer = await question()
      if (ticket === asked.current) then(answer)
    } catch (problem) {
      const code = failed(problem)
      if (code && ticket === asked.current) setError(refused?.(code) ?? messageFor(code))
    } finally {
      if (ticket === asked.current) setPending(false)
    }
  }

  function show(askModel: boolean) {
    if (!chosen) return
    setRemoval(null)
    return ask(
      () => api.previewRule(draft, chosen, { askModel }),
      (answer) => {
        setPage(answer)
        setAskedModel(askModel)
      },
    )
  }

  function asLeaving(id: string) {
    return ask(
      () => api.previewRuleRemoval(draft, chosen, id, askedModel),
      (shown) => setRemoval({ id, shown }),
      // The server answers not_found until it can show a message as a removal leaves it (ADR 0040, pull request 5's
      // disclosure): the page stays, and only this is said.
      (code) => (code === 'not_found' ? asLeavingUnavailable : undefined),
    )
  }

  if (readable.length === 0) return null

  return (
    <section aria-labelledby={titleId} className="flex flex-col gap-3">
      <h2 id={titleId} className="text-title font-medium">
        Preview on your mail
      </h2>
      <p className="text-small text-ink-2">
        Only you see this. It decides nothing, and asks the model only when you ask.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        {readable.length > 1 && (
          <Select
            label="Mailbox"
            value={chosen}
            options={readable.map((m) => ({ value: m.id, label: m.label || m.address }))}
            onChange={setMailbox}
          />
        )}
        <Button
          variant="secondary"
          size="sm"
          aria-disabled={pending}
          onClick={() => void show(false)}
        >
          Show the mail it applies to
        </Button>
        {agreed && (
          <TextAction aria-disabled={pending} onClick={() => void show(true)}>
            Ask the model too
          </TextAction>
        )}
      </div>
      <p role="alert" className="text-small text-danger empty:hidden">
        {error}
      </p>
      {page && (
        <div role="status" className="flex flex-col gap-2">
          <p className="text-small">
            {page.page.messages.length === 0
              ? 'None of the newest mail.'
              : `${page.page.messages.length} of the newest ${page.page.messages.length === 1 ? 'email' : 'emails'}.`}
            {!askedModel && page.page.needs_model.length > 0
              ? ` ${page.page.needs_model.length} more only a model can decide: a rule applies to them while it can't.`
              : ''}
          </p>
          <ul className="border-t border-rule">
            {page.page.messages.map((m) => (
              <li key={m.id} className="flex flex-col gap-1 border-b border-rule py-2">
                <span className="flex flex-wrap items-baseline justify-between gap-x-3">
                  <bdi className="min-w-0 truncate text-small font-medium">
                    {m.subject || '(no subject)'}
                  </bdi>
                  <bdi className="text-small text-ink-2">
                    {m.from[0]?.name || m.from[0]?.address}
                  </bdi>
                </span>
                {draft.effect === 'remove' && (
                  <span>
                    <TextAction aria-disabled={pending} onClick={() => void asLeaving(m.id)}>
                      See it as it would leave
                    </TextAction>
                  </span>
                )}
                {removal?.id === m.id && <AsLeaving shown={removal.shown} />}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

/** One message as the removal leaves it, and what it took out. Never more than the server answered. */
function AsLeaving({ shown }: { shown: api.RuleRemovalPreview }) {
  const r = shown.removed
  const lines: string[] = []
  for (const part of r.parts ?? []) lines.push(`${partWords[part]} taken out whole`)
  for (const [kind, n] of Object.entries(r.shapes ?? {})) {
    lines.push(`${shapeWords[kind as api.ShapeKind]?.label ?? kind}: ${n} taken out`)
  }
  if (r.whole) lines.push('The whole subject and text taken out: the model could not decide')
  else if (r.sentences) lines.push(`${r.sentences} of ${r.of ?? r.sentences} sentences taken out`)
  if (r.attachments)
    lines.push(`${r.attachments} ${r.attachments === 1 ? 'attachment' : 'attachments'} taken out`)
  return (
    <div className="flex flex-col gap-1 rounded-sm bg-sunken px-3 py-2 text-small">
      <span>
        <span className="text-ink-2">Subject: </span>
        {shown.subject === undefined ? (
          <span className="text-ink-2">taken out</span>
        ) : (
          <bdi>{shown.subject}</bdi>
        )}
      </span>
      {shown.text !== undefined && (
        <bdi className="whitespace-pre-wrap break-words">{shown.text}</bdi>
      )}
      <ul className="text-ink-2">
        {lines.map((line) => (
          <li key={line}>{line}</li>
        ))}
      </ul>
    </div>
  )
}
