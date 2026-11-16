import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'

/** How many pages a draft's count asks for by itself before the person asks for more. */
export const pagesAtOnce = 4

/** A draft as a key: two drafts with the same key are the same scope. */
export function draftKey(draft: api.ConsentDraft): string {
  return JSON.stringify({ mailboxes: draft.mailboxes, since: draft.since })
}

/** Where counting goes on: a mailbox of the draft, by its place, and the page of it to continue from. */
type Place = { index: number; after?: string; draftId?: string }

/** Where a count stands for one draft: what it found so far and where it goes on. */
export type Count = {
  key: string
  /** The messages that pass the exact conditions so far, newest first in each mailbox. */
  messages: api.MessageSummary[]
  /** What a rule holds back, from a preview with the model. */
  held: number
  /** What only a model would decide, while the model was not asked. */
  undecided: number
  /** What became of asking the model, when it was asked. */
  model?: api.MatchModel
  /** What approving this draft does, and its snapshot, from the first page. */
  snapshot?: api.ConsentSnapshot
  /** Asking now. */
  pending: boolean
  /** More mail to count: the place to go on from. */
  next?: Place
  /** Why the last page failed. */
  failure?: api.ApiErrorCode
  askModel: boolean
}

/**
 * Counts what a request would read under a draft, page by page over each of its mailboxes, from the newest, again
 * whenever the draft changes (finding 13): an answer for a draft that is no longer the one on the screen is dropped.
 * A draft with no mailbox asks nothing. New mail only asks one page, which is empty, for what approving it does.
 */
export function useDraftCount(request: api.AuthorizationRequest, draft: api.ConsentDraft) {
  const { failed } = useSession()
  const key = draftKey(draft)
  const [count, setCount] = useState<Count>(() => ({
    ...empty(key, false),
    pending: draft.mailboxes.length > 0,
  }))
  // The draft the screen shows now: every answer for another is dropped.
  const showing = useRef(key)
  showing.current = key
  const turn = useRef(0)

  const run = useCallback(
    async (from: Count, pages: number) => {
      const mine = ++turn.current
      let now: Count = { ...from, pending: true, failure: undefined }
      setCount(now)
      let place: Place = from.next ?? { index: 0 }
      for (let asked = 0; asked < pages; asked++) {
        const mailbox = draft.mailboxes[place.index]
        if (mailbox === undefined) break
        try {
          const answer = await api.previewConsent(request.id, {
            draft,
            mailbox,
            ...(place.after ? { after: place.after, draft_id: place.draftId } : {}),
            ...(now.askModel ? { ask_model: true } : {}),
          })
          if (mine !== turn.current || showing.current !== from.key) return
          const { page } = answer
          const last = draft.since.new || place.index + 1 >= draft.mailboxes.length
          const next: Place | undefined = page.next
            ? { index: place.index, after: page.next, draftId: answer.draft_id }
            : last
              ? undefined
              : { index: place.index + 1 }
          now = {
            ...now,
            messages: [...now.messages, ...page.messages],
            held: now.held + (page.held?.length ?? 0),
            undecided: now.undecided + page.needs_model.length,
            ...(page.model ? { model: page.model } : {}),
            snapshot: now.snapshot ?? answer.snapshot,
            next,
          }
          setCount(now)
          if (!next) break
          place = next
        } catch (error) {
          if (mine !== turn.current || showing.current !== from.key) return
          const code = failed(error)
          now = { ...now, pending: false, ...(code ? { failure: code } : {}) }
          setCount(now)
          return
        }
      }
      if (mine === turn.current) setCount({ ...now, pending: false })
    },
    [draft, request.id, failed],
  )

  // Every change of the draft starts again from the newest.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the draft is its key
  useEffect(() => {
    const fresh = empty(key, false)
    if (draft.mailboxes.length === 0) {
      turn.current++
      setCount(fresh)
      return
    }
    void run(fresh, draft.since.new ? 1 : pagesAtOnce)
  }, [key])

  /** Counts on from where the count stopped. */
  const more = useCallback(() => {
    if (count.pending || !count.next || count.key !== key) return
    void run(count, pagesAtOnce)
  }, [count, key, run])

  /** Counts again from the newest, asking the model about what only it can decide. */
  const withModel = useCallback(() => {
    if (count.pending) return
    void run(empty(key, true), pagesAtOnce)
  }, [count.pending, key, run])

  /** Counts again from the newest after a failure. */
  const retry = useCallback(() => {
    void run(empty(key, count.askModel), draft.since.new ? 1 : pagesAtOnce)
  }, [key, count.askModel, draft.since.new, run])

  // Until the first answer for this draft, the count of the one before says nothing.
  const current =
    count.key === key ? count : { ...empty(key, false), pending: draft.mailboxes.length > 0 }
  return { count: current, more, withModel, retry }
}

function empty(key: string, askModel: boolean): Count {
  return { key, messages: [], held: 0, undecided: 0, pending: false, askModel }
}
