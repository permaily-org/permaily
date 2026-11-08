import { ChevronRight } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ButtonLink, Count, EffectMark, FilterButton, Headline, Mark, SectionHeading, TextLink, Tile } from '../../components/ui/index.ts'
import { attention, connections, effectText, formatCount, inbox, rules, type Connection, type Rule } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { isHeadersOnly } from '../connections/ConnectionsAccess.ts'
import { AppliesTo } from '../rules/AppliesTo.tsx'
import { SuggestedRule } from '../rules/SuggestedRule.tsx'
import { AttentionList } from './AttentionList.tsx'

export function OverviewPage() {
  const [params] = useSearchParams()
  return params.get('state') === 'first-use' ? <FirstUse /> : <Overview />
}

const ruleColumns = 'grid grid-cols-[minmax(0,1fr)_220px_200px_96px_16px] items-center gap-x-4'
const connectionColumns = 'grid grid-cols-[minmax(0,1fr)_minmax(0,1.2fr)_150px_140px_16px] items-center gap-x-4'

/** Rules run first on all mail. Each connection's query then runs on what's left. Overview shows them in that order. */
function Overview() {
  const headerReaders = connections.filter(isHeadersOnly).map((c) => c.name)
  const left = inbox.total - inbox.withheld

  return (
    <div className="px-14 pt-9 pb-14">
      <Headline>
        {connections.length} connections can read <Count>{formatCount(inbox.readable)}</Count> of your {formatCount(inbox.total)}{' '}
        emails.
      </Headline>
      <div className="mt-2 flex items-center justify-between gap-6">
        <p className="text-lead text-ink-2">
          {headerReaders.length > 0 &&
            `${headerReaders.join(' and ')} also ${headerReaders.length === 1 ? 'sees' : 'see'} the headers of ${formatCount(inbox.headersOnly)}.`}
        </p>
        <FilterButton>All mailboxes</FilterButton>
      </div>

      <section aria-labelledby="rules-heading" className="mt-7">
        <Step number={1} text="Runs first, on all your mail" />
        <SectionHeading id="rules-heading" title="Your rules" action={<TextLink to="/rules">See rules</TextLink>} />
        <ul>
          {rules.map((rule) => (
            <li key={rule.id}>
              <RuleRow rule={rule} />
            </li>
          ))}
        </ul>
        <p className="mt-2.5 font-mono text-mono text-ink-2">
          {formatCount(inbox.withheld)} held back from every connection. Details removed from {formatCount(inbox.trimmed)}.
        </p>
      </section>

      <section aria-labelledby="connections-heading" className="mt-7">
        <Step number={2} text={`Then each connection's query runs on the ${formatCount(left)} left`} />
        <SectionHeading id="connections-heading" title="What connections ask for" action={<TextLink to="/connections">See connections</TextLink>} />
        <ul>
          {connections.map((connection) => (
            <li key={connection.id}>
              <ConnectionRow connection={connection} />
            </li>
          ))}
        </ul>
      </section>

      <div className="mt-8">
        <AttentionList items={attention} />
      </div>
    </div>
  )
}

function Step({ number, text }: { number: number; text: ReactNode }) {
  return (
    <p className="mb-1.5 flex items-center gap-2 font-mono text-mono text-ink-2">
      <span aria-hidden className="inline-flex size-[18px] items-center justify-center rounded-full border border-ink-2 text-micro">
        {number}
      </span>
      {text}
    </p>
  )
}

function RuleRow({ rule }: { rule: Rule }) {
  return (
    <Link to={`/rules/${rule.id}`} className={cx(ruleColumns, 'group border-b border-rule py-2.5 hover:bg-sunken', focusRing)}>
      <span className="flex min-w-0 items-center gap-2.5">
        <EffectMark effect={rule.effect} />
        <span className="truncate text-body font-medium">{rule.name}</span>
      </span>
      <span className="text-small">{effectText(rule)}</span>
      <AppliesTo appliesTo={rule.appliesTo} />
      <span className="text-right font-mono text-mono">
        {formatCount(rule.matches)}
        <span className="sr-only"> emails</span>
      </span>
      <ChevronRight aria-hidden size={16} className="text-ink-2 group-hover:text-ink" />
    </Link>
  )
}

function ConnectionRow({ connection: c }: { connection: Connection }) {
  const headersOnly = isHeadersOnly(c)
  return (
    <Link to={`/connections/${c.id}`} className={cx(connectionColumns, 'group border-b border-rule py-2 hover:bg-sunken', focusRing)}>
      <span className="flex min-w-0 items-center gap-2.5">
        <Tile initials={c.initials} />
        <span className="flex min-w-0 flex-col">
          <span className="truncate text-body font-medium">{c.name}</span>
          <span className={cx('truncate font-mono text-micro', c.flag ? 'text-attention' : 'text-ink-2')}>{c.overviewDetail}</span>
        </span>
      </span>
      <span className="flex min-w-0 flex-col">
        <span className="truncate text-body">{c.asksFor}</span>
        <span className="truncate font-mono text-micro text-ink-2">{c.query}</span>
      </span>
      <span className="text-small text-ink-2">{c.parts}</span>
      <span className="justify-self-end">
        <Mark tone={headersOnly ? 'soft' : 'shared'} size="sm">
          {c.readLabel}
        </Mark>
      </span>
      <ChevronRight aria-hidden size={16} className="text-ink-2 group-hover:text-ink" />
    </Link>
  )
}

function FirstUse() {
  const [suggest, setSuggest] = useState(true)
  return (
    <div className="px-14 pt-9 pb-14">
      <Headline>Nothing can read your mail.</Headline>
      <p className="mt-3 max-w-[640px] text-lead text-ink-2">
        Your mailbox is connected. Every connection starts with nothing, until you approve what it asks for.
      </p>
      <div role="img" aria-label="Every kind of mail is withheld from every connection" className="mt-6 grid max-w-[560px] grid-cols-5 gap-1">
        {Array.from({ length: 15 }, (_, i) => (
          <span key={i} className="h-7 bg-bar" />
        ))}
      </div>
      {suggest && (
        <div className="mt-8 max-w-[560px]">
          <SuggestedRule onDismiss={() => setSuggest(false)} />
        </div>
      )}
      <div className="mt-6">
        <ButtonLink to="/connections/new">Connect an agent</ButtonLink>
      </div>
    </div>
  )
}
