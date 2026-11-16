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
  /**
   * For a draft that replaces the client's connection: the messages counted that the connection reads too, by
   * identifier. Every other one is mail the new scope reaches that the old did not (ADR 0030, "Asking again").
   */
  reached?: ReadonlySet<string>
  /** How many of the messages counted could be new only because a model decides the connection's query. */
  unsure: number
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
          // What the connection being replaced reads of this page, worked out before the page is shown, so a row is
          // never shown unmarked first.
          const reach =
            answer.change === 'replace' && request.connection
              ? await reachedBefore(request, request.connection, mailbox, page.messages)
              : null
          if (mine !== turn.current || showing.current !== from.key) return
          now = {
            ...now,
            messages: [...now.messages, ...page.messages],
            ...(reach
              ? {
                  reached: new Set([...(now.reached ?? []), ...reach.reached]),
                  unsure: now.unsure + reach.unsure,
                }
              : {}),
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
    [draft, request, failed],
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
  return { key, messages: [], held: 0, undecided: 0, unsure: 0, pending: false, askModel }
}

/** The most pages of the connection's own query asked for one page of the preview; past them, a message is new. */
const reachPages = 4

/**
 * Which of a page of the preview, in one mailbox, the client's connection reads too: the messages of one of its
 * mailboxes, received since its mail since, that its query (when it has one other than the request's) matches. What
 * it can only tell from the connection's query by asking (`POST /api/v1/mailboxes/{id}/matches`, the owner's own
 * preview of a formula) is asked, around the page's received times; what that does not settle (a condition a model
 * decides, a failure, more pages than `reachPages`) is counted as new, never as read: the marks may say more is new
 * than is, never less.
 */
export async function reachedBefore(
  request: api.AuthorizationRequest,
  connection: api.Connection,
  mailbox: string,
  messages: readonly api.MessageSummary[],
): Promise<{ reached: string[]; unsure: number }> {
  if (!connection.mailboxes.includes(mailbox)) return { reached: [], unsure: 0 }
  const since = connection.since.at ? Date.parse(connection.since.at) : null
  // A connection of new mail only always says from when; without it, nothing can be said to be read.
  if (connection.since.new && since === null) return { reached: [], unsure: 0 }
  const inTime = messages.filter((m) => since === null || Date.parse(m.received_at) >= since)
  const query = connection.query
  if (!query || inTime.length === 0 || query.id === request.query?.id) {
    return { reached: inTime.map((m) => m.id), unsure: 0 }
  }
  const times = inTime.map((m) => Date.parse(m.received_at))
  // A second either side: the server's times may be finer than the formula's, and a message more matters not.
  const from = new Date(Math.floor(Math.min(...times) / 1000) * 1000 - 1000).toISOString()
  const to = new Date(Math.ceil(Math.max(...times) / 1000) * 1000 + 1000).toISOString()
  const formula: api.QueryFormula = {
    v: 1,
    all: [
      ...query.formula.all,
      { any: [{ var: 'received', op: 'since', value: from }] },
      { any: [{ var: 'received', op: 'before', value: to }] },
    ],
  }
  const matched = new Set<string>()
  const undecided = new Set<string>()
  try {
    let after: string | undefined
    for (let asked = 0; asked < reachPages; asked++) {
      const page = await api.matches(mailbox, formula, after ? { after } : {})
      for (const m of page.messages) matched.add(m.id)
      for (const id of [...page.needs_model, ...page.unknown]) undecided.add(id)
      after = page.next
      if (!after) break
    }
  } catch {
    // Not known: every one of them stays new.
  }
  const reached = inTime.filter((m) => matched.has(m.id)).map((m) => m.id)
  const unsure = inTime.filter((m) => !matched.has(m.id) && undecided.has(m.id)).length
  return { reached, unsure }
}
