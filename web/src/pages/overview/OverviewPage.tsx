import { ChevronRight } from 'lucide-react'
import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { ButtonLink, EffectMark, FilterButton, Headline, Mark, SectionHeading, TextLink, Tile } from '../../components/ui/index.ts'
import { useRevoked } from '../../data/revoked.ts'
import { useRules } from '../../data/ruleScopes.ts'
import { attention, connectionLabel, connections, effectText, formatCount, vendorOf, type Connection, type Rule } from '../../data/sample.ts'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { isHeadersOnly } from '../connections/ConnectionsAccess.ts'
import { Runs } from '../connections/Runs.tsx'
import { AppliesTo } from '../rules/AppliesTo.tsx'
import { SuggestedRule } from '../rules/SuggestedRule.tsx'
import { AttentionList } from './AttentionList.tsx'

export function OverviewPage() {
  const [params] = useSearchParams()
  return params.get('state') === 'first-use' ? <FirstUse /> : <Overview />
}

/**
 * Each list fits its own width, so it works on a phone and beside an expanded sidebar. Every column shows from 896px.
 * From 672px the columns narrow, and connections leave out when they run. Below that a row puts its name on the first
 * line, with its count or mark and the chevron, and the rest underneath.
 */
const ruleColumns =
  'grid grid-cols-[minmax(0,1fr)_auto_16px] items-center gap-x-4 gap-y-0.5 @2xl:grid-cols-[minmax(0,1fr)_150px_minmax(0,1fr)_56px_16px] @2xl:gap-y-0 @4xl:grid-cols-[minmax(0,1fr)_200px_minmax(0,260px)_80px_16px]'
const connectionColumns =
  'grid grid-cols-[minmax(0,1fr)_auto_16px] items-center gap-x-4 gap-y-1 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_130px_16px] @2xl:gap-y-0 @4xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_140px_130px_16px]'
/** Where a stacked row puts its count or mark, and its chevron: on the name's line. */
const besideName = '@max-2xl:col-2 @max-2xl:row-1'
const chevronCell = '@max-2xl:col-3 @max-2xl:row-1'

/** Rules run first on all mail, and each connection's query runs on what's left, so rules come first. */
function Overview() {
  const revoked = useRevoked()
  const rules = useRules()
  return (
    <div className={pagePadding}>
      <h1 className="sr-only">Overview</h1>
      <div className="flex justify-end">
        <FilterButton>All mailboxes</FilterButton>
      </div>

      <section aria-labelledby="rules-heading" className="mt-4">
        <SectionHeading id="rules-heading" title="Your rules" action={<TextLink to="/rules">See rules</TextLink>} />
        <ul className="@container">
          {rules.map((rule) => (
            <li key={rule.id}>
              <RuleRow rule={rule} />
            </li>
          ))}
        </ul>
      </section>

      <section aria-labelledby="connections-heading" className="mt-8">
        <SectionHeading id="connections-heading" title="Connections" action={<TextLink to="/connections">See connections</TextLink>} />
        <ul className="@container">
          {connections.map((connection) => (
            <li key={connection.id}>
              <ConnectionRow connection={connection} revoked={revoked.has(connection.id)} />
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

function RuleRow({ rule }: { rule: Rule }) {
  return (
    <Link to={`/rules/${rule.id}`} className={cx(ruleColumns, 'group border-b border-rule py-2.5 hover:bg-sunken', focusRing)}>
      <span className="flex min-w-0 items-center gap-2.5">
        <EffectMark effect={rule.effect} />
        <span className="truncate text-body font-medium">{rule.name}</span>
      </span>
      {/* Stacked, what it does and who it applies to share the line under the name. In columns, each is a cell. */}
      <span className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-0.5 pl-6 @max-2xl:col-span-full @2xl:contents">
        <span className="text-small">{effectText(rule)}</span>
        <span className="min-w-0 @max-2xl:text-ink-2 @2xl:contents">
          <AppliesTo appliesTo={rule.appliesTo} />
        </span>
      </span>
      <span className={cx('text-right font-mono text-mono', besideName)}>
        {formatCount(rule.matches)}
        <span className="sr-only"> emails</span>
      </span>
      <ChevronRight aria-hidden size={16} className={cx('text-ink-2 group-hover:text-ink', chevronCell)} />
    </Link>
  )
}

function ConnectionRow({ connection: c, revoked }: { connection: Connection; revoked: boolean }) {
  const vendor = vendorOf(c)
  return (
    <Link
      to={`/connections/${c.id}`}
      aria-label={connectionLabel(c)}
      className={cx(connectionColumns, 'group border-b border-rule py-2 hover:bg-sunken', focusRing)}
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <Tile initials={vendor.initials} />
        <span className="flex min-w-0 flex-col">
          <span className={cx('truncate text-body font-medium', revoked && 'text-ink-2')}>{c.name}</span>
          <span className="truncate text-small text-ink-2">{vendor.name}</span>
        </span>
      </span>
      {/* Stacked, it wraps under the name instead of being cut off. */}
      <span className="text-body @max-2xl:col-span-full @max-2xl:pl-[46px] @2xl:truncate">{c.asksFor}</span>
      <span className="text-small @max-4xl:hidden">
        <Runs trigger={c.trigger} />
      </span>
      <span className={cx('justify-self-end', besideName)}>
        {revoked ? (
          <span className="text-small text-ink-2">Revoked</span>
        ) : (
          <Mark tone={isHeadersOnly(c) ? 'soft' : 'shared'} size="sm">
            {c.readLabel}
          </Mark>
        )}
      </span>
      <ChevronRight aria-hidden size={16} className={cx('text-ink-2 group-hover:text-ink', chevronCell)} />
    </Link>
  )
}

function FirstUse() {
  const [suggest, setSuggest] = useState(true)
  return (
    <div className={pagePadding}>
      <Headline>Nothing can read your mail.</Headline>
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
