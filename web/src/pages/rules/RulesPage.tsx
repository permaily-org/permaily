import { ChevronRight } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Link, useLocation } from 'react-router'
import { useConfirmed } from '../../components/Confirm.tsx'
import {
  Button,
  ButtonLink,
  Dialog,
  Headline,
  Notice,
  SectionHeading,
  Switch,
  TextAction,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { RULES_NOTICE_VERSION } from '../../lib/rules-notice.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { LoadState } from '../accounts/shared.tsx'
import { ruleProblemFor } from '../auth/messages.ts'
import { Sentence } from '../connections/words.tsx'
import { noticeAgreed, RulesNotice, useRuleList } from './shared.tsx'
import { inFlight, ruleName, rulesProof } from './words.ts'

/** A change the page asks about before making it: a rule's switch, or the default's. */
type Asking = { kind: 'rule'; rule: api.Rule; on: boolean } | { kind: 'default'; on: boolean }

/**
 * The person's rules (ADR 0040, "The screens"): the default rule with its switch, then their own, each with what it does,
 * who for and its switch, "New rule", and the rules' notice when it is not agreed. Every change asks first, says that an
 * answer already being prepared can still finish, and needs a fresh proof. Words drafted for the designer's review.
 */
export function RulesPage() {
  const { failed } = useSession()
  const confirmed = useConfirmed()
  const { list, setList, failure, load } = useRuleList()
  const location = useLocation()
  const [asking, setAsking] = useState<Asking | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [said, setSaid] = useState<string>(
    () => (location.state as { said?: string } | null)?.said ?? '',
  )
  const [reading, setReading] = useState(false)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const focusHeading = useRef(false)

  useEffect(() => {
    if (!focusHeading.current) return
    focusHeading.current = false
    headingRef.current?.focus()
  })

  if (list === null) {
    return (
      <div className={pagePadding}>
        <Headline>Rules</Headline>
        <div className="mt-6 max-w-[880px]">
          <LoadState failure={failure} what="Your rules" onRetry={() => void load()} />
        </div>
      </div>
    )
  }

  const rules = list
  const agreed = noticeAgreed(rules)
  const full = rules.rules.length >= rules.max

  async function change() {
    if (pending || !asking) return
    setPending(true)
    setError(null)
    try {
      if (asking.kind === 'default') {
        const set = await confirmed(() => api.setDefaultRule(asking.on, rules.version), rulesProof)
        setSaid(
          asking.on
            ? 'The Sign-in and security rule is on.'
            : 'The Sign-in and security rule is off.',
        )
        setList({ ...rules, default: set })
      } else {
        const draft = { ...asking.rule.check.draft, on: asking.on }
        const check = await api.checkRule(draft)
        await confirmed(
          () =>
            api.saveRule(
              {
                draft_id: check.draft_id,
                draft: check.draft,
                version: rules.version,
                ...(check.model ? { notice: RULES_NOTICE_VERSION } : {}),
              },
              asking.rule.id,
            ),
          rulesProof,
        )
        setSaid(`${ruleName(asking.rule.check)} is ${asking.on ? 'on' : 'off'}.`)
      }
      setAsking(null)
      // Every change moves the rules' version: read them again, for the next change to name it.
      await load()
      focusHeading.current = true
    } catch (problem) {
      setAsking(null)
      if (isCancelled(problem)) return
      const code = failed(problem)
      if (!code) return
      setError(ruleProblemFor(code, problem instanceof api.ApiError ? problem.rule : undefined))
      if (code === 'rules_changed' || code === 'rule_changed' || code === 'not_found') void load()
    } finally {
      setPending(false)
    }
  }

  return (
    <div className={pagePadding}>
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-4">
        <h1
          ref={headingRef}
          tabIndex={-1}
          className="text-display-s font-medium outline-none md:text-display"
        >
          {rules.rules.length === 0
            ? 'No rules of your own yet.'
            : `${rules.rules.length} ${rules.rules.length === 1 ? 'rule' : 'rules'} of your own.`}
        </h1>
        {full ? (
          <Button aria-disabled aria-describedby="rules-full">
            New rule
          </Button>
        ) : (
          <ButtonLink to="/rules/new">New rule</ButtonLink>
        )}
      </div>
      {full && (
        <p id="rules-full" className="mt-2 text-small text-ink-2">
          You have {rules.max} rules, as many as an account can. Delete one to make another.
        </p>
      )}

      <p role="status" className="sr-only">
        {said}
      </p>
      <p role="alert" className="mt-3 max-w-[560px] text-small text-danger empty:hidden">
        {error}
      </p>

      <div className="mt-6 flex max-w-[880px] flex-col gap-9">
        {!agreed && (
          <div className="flex flex-col gap-3">
            <p className="text-small text-ink-2">
              Rules a model helps with need your agreement to what Permaily sends it.{' '}
              {!reading && <TextAction onClick={() => setReading(true)}>Read and agree</TextAction>}
            </p>
            {reading && (
              <RulesNotice
                onAgreed={(notice) => {
                  setReading(false)
                  setSaid('Agreed. Rules a model decides can ask it now.')
                  setList({ ...rules, notice })
                }}
              />
            )}
          </div>
        )}

        <section aria-labelledby="default-heading">
          <SectionHeading id="default-heading" title="Every account's rule" />
          <div className="flex items-start gap-4 border-b border-rule py-3">
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="font-medium">Sign-in and security</span>
              <span className="text-small text-ink-2">
                {rules.default.rule.sentence.map((sentence, i) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: a rule's sentences have no identity but their place
                  <Sentence key={i} sentence={sentence} />
                ))}
              </span>
              <span className="text-small text-ink-2">Holds the email back. Every connection.</span>
            </div>
            <Switch
              checked={rules.default.on}
              label="Sign-in and security"
              onChange={(on) => setAsking({ kind: 'default', on })}
            />
          </div>
        </section>

        <section aria-labelledby="own-heading">
          <SectionHeading id="own-heading" title="Your rules" />
          {rules.rules.length === 0 ? (
            <p className="border-b border-rule py-3 text-small text-ink-2">
              A rule of your own holds mail back from your connections, or takes details out of it,
              before any app reads it.
            </p>
          ) : (
            <ul aria-label="Your rules">
              {rules.rules.map((rule) => (
                <RuleRow
                  key={rule.id}
                  rule={rule}
                  onSwitch={(on) => setAsking({ kind: 'rule', rule, on })}
                />
              ))}
            </ul>
          )}
        </section>
      </div>

      <Dialog open={asking !== null} onClose={() => setAsking(null)} labelledBy="switch-heading">
        {asking && (
          <>
            <h2
              id="switch-heading"
              data-dialog-focus
              tabIndex={-1}
              className="text-title font-medium outline-none"
            >
              {asking.kind === 'default'
                ? `Turn the Sign-in and security rule ${asking.on ? 'on' : 'off'}?`
                : `Turn ${ruleName(asking.rule.check)} ${asking.on ? 'on' : 'off'}?`}
            </h2>
            <div className="mt-3 flex flex-col gap-2 text-ink-2">
              {!asking.on && (
                <p>
                  {asking.kind === 'default'
                    ? 'Sign-in codes, password resets and other security mail it holds back can then leave to every connection that reads them.'
                    : 'Mail it holds back or takes details out of can then leave to the connections it is for.'}
                </p>
              )}
              <p>{inFlight}</p>
            </div>
            <div className="mt-6 flex flex-wrap gap-2.5">
              <Button
                variant={asking.on ? 'primary' : 'danger'}
                aria-disabled={pending}
                onClick={() => void change()}
              >
                {asking.on ? 'Turn on' : 'Turn off'}
              </Button>
              <Button variant="secondary" onClick={() => setAsking(null)}>
                Cancel
              </Button>
            </div>
          </>
        )}
      </Dialog>
    </div>
  )
}

/** One of the person's rules: its name, what it does and who for in the server's words, and its switch. */
function RuleRow({ rule, onSwitch }: { rule: api.Rule; onSwitch: (on: boolean) => void }) {
  const name = ruleName(rule.check)
  return (
    <li className="flex items-start gap-4 border-b border-rule py-3">
      <Link
        to={`/rules/${encodeURIComponent(rule.id)}`}
        className={cx(
          'flex min-w-0 flex-1 items-start gap-2 rounded-sm hover:bg-sunken',
          focusRing,
        )}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <bdi className="font-medium break-words">{name}</bdi>
          <bdi className="text-small text-ink-2">{rule.check.does}</bdi>
          <bdi className="text-small text-ink-2">{rule.check.for}</bdi>
          {rule.model_off && (
            <span className="font-mono text-mono text-attention">
              Asks the model under an older notice
            </span>
          )}
        </span>
        <ChevronRight aria-hidden size={16} className="mt-1 shrink-0 text-ink-2" />
      </Link>
      <Switch checked={rule.check.draft.on} label={name} onChange={onSwitch} />
    </li>
  )
}

/** Shown where a rule that asks the model under an older notice is edited. */
export function ModelOffNotice() {
  return (
    <Notice title="This rule asks the model under an older notice.">
      Until you agree to the rules' notice in force and save it again, it asks nothing: it holds
      every message it might apply to, or takes out the whole subject and text.
    </Notice>
  )
}
