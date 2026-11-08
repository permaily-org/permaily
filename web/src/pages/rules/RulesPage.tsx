import { ChevronRight } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { ButtonLink, EffectMark, Headline } from '../../components/ui/index.ts'
import { effectText, formatCount, rules, type Rule } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { AppliesTo } from './AppliesTo.tsx'
import { SuggestedRule } from './SuggestedRule.tsx'

const columns = 'grid grid-cols-[minmax(0,1fr)_220px_200px_110px_90px_16px] items-center'

export function RulesPage() {
  const [params] = useSearchParams()
  return <div className="px-14 pt-9 pb-16">{params.get('state') === 'empty' ? <NoRules /> : <RuleList />}</div>
}

function RuleList() {
  const flagged = rules.filter((rule) => rule.matchNote).length
  const look = flagged === 0 ? 'None need a look.' : `${flagged} ${flagged === 1 ? 'needs' : 'need'} a look.`
  return (
    <>
      <div className="flex items-center justify-between gap-6">
        <div>
          <Headline>
            {rules.length} rules. {look}
          </Headline>
          <p className="mt-1 max-w-[720px] text-lead text-ink-2">
            Your rules run first, on all your mail. A connection's query only sees what's left, and it's never told what your rules took out.
          </p>
        </div>
        <ButtonLink to="/rules/new">New rule</ButtonLink>
      </div>

      <div aria-hidden className={cx(columns, 'mt-5 border-b border-rule-strong py-2 text-small text-ink-2')}>
        <span>Rule</span>
        <span>What it does</span>
        <span>Applies to</span>
        <span>Matches</span>
        <span>Changed</span>
      </div>
      <ul aria-label="Your rules">
        {rules.map((rule) => (
          <li key={rule.id}>
            <RuleRow rule={rule} />
          </li>
        ))}
      </ul>
    </>
  )
}

function RuleRow({ rule }: { rule: Rule }) {
  return (
    <Link to={`/rules/${rule.id}`} className={cx(columns, 'border-b border-rule py-2.5 hover:bg-sunken', focusRing)}>
      <span className="flex min-w-0 flex-col pr-6">
        <span className="text-body font-medium">{rule.name}</span>
        <span className="text-small text-ink-2">{rule.description}</span>
      </span>

      <span className="flex items-center gap-2">
        <EffectMark effect={rule.effect} />
        <span className="text-small">{effectText(rule)}</span>
      </span>

      <span className="pr-4">
        <span className="sr-only">Applies to </span>
        <AppliesTo appliesTo={rule.appliesTo} />
      </span>

      <span className="flex flex-col">
        <span className="font-mono text-small">
          {formatCount(rule.matches)}
          <span className="sr-only"> emails</span>
        </span>
        {rule.matchNote && <span className="font-mono text-mono text-attention">{rule.matchNote}</span>}
      </span>

      <span className="font-mono text-mono text-ink-2">
        <span className="sr-only">Changed </span>
        {rule.changed}
      </span>

      <ChevronRight aria-hidden size={16} className="text-ink-2" />
    </Link>
  )
}

function NoRules() {
  return (
    <div className="max-w-[560px]">
      <Headline>No rules yet.</Headline>
      <p className="mt-2 text-lead text-ink-2">
        A rule holds back mail, or removes details from it, before any connection's query runs. Start with the one we suggest for every account,
        or write your own.
      </p>
      <div className="mt-5">
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
