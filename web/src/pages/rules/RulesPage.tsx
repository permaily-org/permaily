import { ChevronRight } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { Bar, ButtonLink, Headline, Tag } from '../../components/ui/index.ts'
import { formatCount, rules, type Rule } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { RulesTabs } from './RulesTabs.tsx'
import { SuggestedRule } from './SuggestedRule.tsx'
import { UsedBy } from './UsedBy.tsx'

const columns = 'grid grid-cols-[minmax(0,1fr)_226px_126px_166px_110px_16px] items-center'

function needsALook(rule: Rule): boolean {
  return Boolean(rule.basedOnFlag || rule.matchNote)
}

export function RulesPage() {
  const [params] = useSearchParams()
  return (
    <div className="px-14 pt-8 pb-16">
      <RulesTabs />
      {params.get('state') === 'empty' ? <NoRules /> : <RuleList />}
    </div>
  )
}

function RuleList() {
  const flagged = rules.filter(needsALook).length
  const look = flagged === 0 ? 'None need a look.' : `${flagged} ${flagged === 1 ? 'needs' : 'need'} a look.`
  return (
    <>
      <div className="mt-6 flex items-center justify-between gap-6">
        <div>
          <Headline>
            {rules.length} rules. {look}
          </Headline>
          <p className="mt-1 text-lead text-ink-2">
            A rule only defines what matches. Nothing is shared until you let a connection use it, and a rule withheld from all wins over
            the rest.
          </p>
        </div>
        <ButtonLink to="/rules/new">New rule</ButtonLink>
      </div>

      <div aria-hidden className={cx(columns, 'mt-4 border-b border-rule-strong py-2 text-small text-ink-2')}>
        <span>Rule</span>
        <span>Based on</span>
        <span>Matches</span>
        <span>Used by</span>
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

      <span className="flex flex-col items-start gap-0.5">
        <span className="sr-only">Based on</span>
        {rule.pack ? <Tag>{rule.pack}</Tag> : <span className="text-small text-ink-2">{rule.basedOnText}</span>}
        {rule.basedOnNote && <span className={cx('text-small', rule.basedOnFlag ? 'text-attention' : 'text-ink-2')}>{rule.basedOnNote}</span>}
      </span>

      <span className="flex flex-col">
        <span className="font-mono text-small">
          {formatCount(rule.matches)}
          <span className="sr-only"> matches</span>
        </span>
        {rule.matchNote && <span className="font-mono text-mono text-attention">{rule.matchNote}</span>}
      </span>

      <span>
        <span className="sr-only">Used by</span>
        {rule.withheld ? (
          <span className="flex items-center gap-2">
            <Bar width={14} height={14} />
            <span className="text-small text-ink-2">Withheld from all</span>
          </span>
        ) : (
          <UsedBy initials={rule.usedBy} note={rule.usedByNote} />
        )}
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
    <div className="mt-6 max-w-[560px]">
      <Headline>No rules yet.</Headline>
      <p className="mt-2 text-lead text-ink-2">Start with the rule we suggest for every account, or from a community pack.</p>
      <div className="mt-5">
        <SuggestedRule />
      </div>
      <p className="mt-6 text-small text-ink-2">Community packs</p>
      <div className="mt-2 flex flex-wrap gap-2">
        {['travel.bookings', 'finance.invoices'].map((id) => (
          <Link key={id} to={`/rules/packs?pack=${id}`} className={cx('group rounded-sm', focusRing)}>
            <Tag className="group-hover:border-ink">{id}</Tag>
          </Link>
        ))}
      </div>
      <div className="mt-5">
        <ButtonLink to="/rules/new" variant="secondary">
          New rule
        </ButtonLink>
      </div>
    </div>
  )
}
