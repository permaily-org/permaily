import { useCallback, useEffect, useState } from 'react'
import { useConfirmed } from '../../components/Confirm.tsx'
import { Button, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { RULES_NOTICE_VERSION, rulesNotice, rulesNoticeTitle } from '../../lib/rules-notice.ts'
import { useSession } from '../../lib/session.tsx'
import { messageFor } from '../auth/messages.ts'
import { Sentence } from '../connections/words.tsx'
import { ruleName, rulesProof } from './words.ts'

/** The account's rules as the server answers them, read again on demand. A failure says why, in words. */
export function useRuleList() {
  const { failed } = useSession()
  const [list, setList] = useState<api.RuleList | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      const next = await api.rules()
      setList(next)
      return next
    } catch (problem) {
      const code = failed(problem)
      if (code) setFailure(messageFor(code))
      return null
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  return { list, setList, failure, load }
}

/** Whether the account agreed to the rules' notice this app shows. */
export function noticeAgreed(list: api.RuleList): boolean {
  return list.notice?.version === RULES_NOTICE_VERSION
}

/** A rule's criteria as the server wrote them: "Any of these:" and each criteria's sentence. Never the app's words. */
export function RuleSentence({ sentence }: { sentence: api.RuleSentence }) {
  const [only] = sentence.formulas
  if (only && sentence.formulas.length === 1 && sentence.lead.length === 0) {
    return <Sentence sentence={only} />
  }
  if (sentence.formulas.length === 0) return <bdi>{sentence.text}</bdi>
  return (
    <span className="flex flex-col gap-1">
      <bdi>{sentence.lead.map((part) => part.text).join('')}</bdi>
      <ul className="flex list-disc flex-col gap-1 pl-5">
        {sentence.formulas.map((formula, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: a sentence's formulas have no identity but their place
          <li key={i}>
            <Sentence sentence={formula} />
          </li>
        ))}
      </ul>
    </span>
  )
}

/**
 * The rules' notice, and agreeing to it. Agreeing is a change of the account's rules, so it needs a fresh proof, and it
 * moves the rules' version: `onAgreed` reads them again, and the notice waits for it before taking another click.
 */
export function RulesNotice({
  onAgreed,
  headingLevel = 'h2',
}: {
  onAgreed: (agreement: api.RuleNoticeAgreement) => void | Promise<void>
  headingLevel?: 'h2' | 'h3'
}) {
  const { failed } = useSession()
  const confirmed = useConfirmed()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const Heading = headingLevel

  async function agree() {
    if (pending) return
    setPending(true)
    setError(null)
    try {
      await onAgreed(await confirmed(() => api.agreeRulesNotice(RULES_NOTICE_VERSION), rulesProof))
    } catch (problem) {
      if (isCancelled(problem)) return
      const code = failed(problem)
      if (code) setError(messageFor(code))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="rounded-sm border border-rule px-4 py-3.5">
      <Heading className="font-medium">{rulesNoticeTitle}</Heading>
      <div className="mt-2 flex flex-col gap-2 text-small text-ink-2">
        {rulesNotice.map((paragraph) => (
          <p key={paragraph}>{paragraph}</p>
        ))}
      </div>
      <div className="mt-3.5 flex flex-wrap items-center gap-3">
        <Button size="sm" aria-disabled={pending} onClick={() => void agree()}>
          Agree
        </Button>
      </div>
      <p role="alert" className="mt-2 text-small text-danger empty:hidden">
        {error}
      </p>
    </div>
  )
}

/** A rule as the consent screen and a connection's page list it: its sentence, then what it does and who for. */
export type Applied = {
  id: string
  /** The person's own rule, which opens on its page; absent for the default rule. */
  rule?: api.Rule
  sentence: api.QuerySentence[]
  /** What it does and who for, in the server's words, when the rules could be read. */
  does?: string
  for?: string
}

/** The default rule as it is listed: it holds back, for every connection. */
function defaultApplied(rule: api.RuleSummary): Applied {
  return {
    id: rule.id,
    sentence: rule.sentence,
    does: 'Holds the email back',
    for: 'Every connection',
  }
}

/**
 * The rules the server says apply (`summaries`, the consent screen's), each said by its sentence and by what it does and
 * who for in the server's words: the summary's own (ADR 0040, U25), else, from a server that does not send them, the
 * account's list's when it could be read.
 */
export function appliedOf(summaries: api.RuleSummary[], list: api.RuleList | null): Applied[] {
  return summaries.map((summary) => {
    const rule =
      list && summary.id !== list.default.rule.id
        ? list.rules.find((r) => r.id === summary.id && r.version === summary.version)
        : undefined
    if (summary.does && summary.for) {
      return {
        id: summary.id,
        rule,
        sentence: summary.sentence,
        does: summary.does,
        for: summary.for,
      }
    }
    if (list && summary.id === list.default.rule.id) return defaultApplied(summary)
    if (!rule) return { id: summary.id, sentence: summary.sentence }
    return {
      id: summary.id,
      rule,
      sentence: summary.sentence,
      does: rule.check.does,
      for: rule.check.for,
    }
  })
}

/** The rules a connection of `clientId` applies (`rules.Record.For`): the default first unless off, then each rule on whose scope covers it. */
export function applyingTo(list: api.RuleList, clientId: string): Applied[] {
  const own = list.rules
    .filter((r) => r.check.draft.on && api.ruleCovers(r.check.draft.scope, clientId))
    .sort(
      (a, b) =>
        Number(a.check.draft.effect === 'remove') - Number(b.check.draft.effect === 'remove'),
    )
  return [
    ...(list.default.on ? [defaultApplied(list.default.rule)] : []),
    ...own.map((rule) => ({
      id: rule.id,
      rule,
      sentence: rule.check.sentence.formulas,
      does: rule.check.does,
      for: rule.check.for,
    })),
  ]
}

/**
 * A list of rules that apply, each by its name and sentence, what it does and who for. With `links` a rule's name
 * opens its page; the consent screen has none, since leaving it forgets the request.
 */
export function AppliedList({ applied, links = false }: { applied: Applied[]; links?: boolean }) {
  return (
    <ul className="flex flex-col">
      {applied.map((a) => (
        <li key={a.id} className="flex flex-col gap-0.5 border-b border-rule py-[11px]">
          {a.rule && (
            <span className="font-medium">
              {links ? (
                <TextLink to={`/rules/${encodeURIComponent(a.rule.id)}`}>
                  <bdi>{ruleName(a.rule.check)}</bdi>
                </TextLink>
              ) : (
                <bdi>{ruleName(a.rule.check)}</bdi>
              )}
            </span>
          )}
          <span className={a.rule ? 'text-small text-ink-2' : undefined}>
            {a.sentence.map((sentence, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: a rule's sentences have no identity but their place
              <Sentence key={i} sentence={sentence} />
            ))}
          </span>
          {a.does && (
            <span className="text-small text-ink-2">
              <bdi>{a.does}</bdi>
            </span>
          )}
          {a.for && (
            <span className="text-small text-ink-2">
              <bdi>{a.for}</bdi>
            </span>
          )}
        </li>
      ))}
    </ul>
  )
}
