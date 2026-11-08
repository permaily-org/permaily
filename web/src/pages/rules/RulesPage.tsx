import { ChevronRight } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { ButtonLink, EffectMark, Headline } from '../../components/ui/index.ts'
import { useRules } from '../../data/ruleScopes.ts'
import { effectText, formatCount, type Rule } from '../../data/sample.ts'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { AppliesTo } from './AppliesTo.tsx'
import { SuggestedRule } from './SuggestedRule.tsx'

/**
 * The list fits its own width, so it works on a phone and beside an expanded sidebar. Every column shows from 896px,
 * and from 672px it leaves out when a rule changed. Below that a rule puts its name and count first, and what it does
 * and who it applies to underneath.
 */
const columns =
  'grid grid-cols-[minmax(0,1fr)_auto_16px] items-start gap-x-4 gap-y-1.5 @2xl:grid-cols-[minmax(0,1fr)_160px_minmax(0,1fr)_88px_16px] @2xl:items-center @2xl:gap-0 @4xl:grid-cols-[minmax(0,1fr)_200px_minmax(0,260px)_100px_80px_16px]'
const changedColumn = '@max-4xl:hidden'

export function RulesPage() {
  const [params] = useSearchParams()
  return <div className={pagePadding}>{params.get('state') === 'empty' ? <NoRules /> : <RuleList />}</div>
}

function RuleList() {
  const rules = useRules()
  const flagged = rules.filter((rule) => rule.matchNote).length
  const look = flagged === 0 ? 'None need a look.' : `${flagged} ${flagged === 1 ? 'needs' : 'need'} a look.`
  return (
    <>
      {/* On a phone the button goes under the headline. */}
      <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-4">
        <Headline>
          {rules.length} rules. {look}
        </Headline>
        <ButtonLink to="/rules/new">New rule</ButtonLink>
      </div>

      <div className="mt-5 @container">
        {/* Stacked rows have no columns to label, so the list keeps the strong rule itself. */}
        <div aria-hidden className={cx(columns, 'border-b border-rule-strong py-2 text-small text-ink-2 @max-2xl:hidden')}>
          <span>Rule</span>
          <span>What it does</span>
          <span>Applies to</span>
          <span>Matches</span>
          <span className={changedColumn}>Changed</span>
        </div>
        <ul aria-label="Your rules" className="@max-2xl:border-t @max-2xl:border-rule-strong">
          {rules.map((rule) => (
            <li key={rule.id}>
              <RuleRow rule={rule} />
            </li>
          ))}
        </ul>
      </div>
    </>
  )
}

function RuleRow({ rule }: { rule: Rule }) {
  return (
    <Link to={`/rules/${rule.id}`} className={cx(columns, 'border-b border-rule py-2.5 hover:bg-sunken', focusRing)}>
      <span className="flex min-w-0 flex-col @2xl:pr-6">
        <span className="text-body font-medium">{rule.name}</span>
        <span className="text-small text-ink-2">{rule.description}</span>
      </span>

      {/* Stacked, what it does and who it applies to share a line under the name. In columns, each is a cell. */}
      <span className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5 @max-2xl:col-span-full @2xl:contents">
        {/* The words, not the mark, give this cell its baseline, so it lines up with who it applies to. */}
        <span className="flex items-center gap-2">
          <EffectMark effect={rule.effect} />
          <span className="self-baseline text-small">{effectText(rule)}</span>
        </span>

        <span className="min-w-0 @max-2xl:text-ink-2 @2xl:pr-4">
          <span className="sr-only">Applies to </span>
          <AppliesTo appliesTo={rule.appliesTo} />
        </span>
      </span>

      <span className="flex flex-col @max-2xl:col-2 @max-2xl:row-1 @max-2xl:items-end">
        <span className="font-mono text-small">
          {formatCount(rule.matches)}
          <span className="sr-only"> emails</span>
        </span>
        {rule.matchNote && <span className="font-mono text-mono text-attention">{rule.matchNote}</span>}
      </span>

      <span className={cx('font-mono text-mono text-ink-2', changedColumn)}>
        <span className="sr-only">Changed </span>
        {rule.changed}
      </span>

      <ChevronRight aria-hidden size={16} className="text-ink-2 @max-2xl:col-3 @max-2xl:row-1 @max-2xl:mt-0.5" />
    </Link>
  )
}

function NoRules() {
  return (
    <div className="max-w-[560px]">
      <Headline>No rules yet.</Headline>
      <div className="mt-6">
        <SuggestedRule />
      </div>
      <div className="mt-5">
        <ButtonLink to="/rules/new" variant="secondary">
          New rule
        </ButtonLink>
      </div>
    </div>
  )
}
