import { ChevronRight } from 'lucide-react'
import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { ButtonLink, Headline, SectionHeading, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { LoadState, mailboxName, stateWords } from '../accounts/shared.tsx'
import { canChange, useStanding } from '../accounts/standing.ts'
import { messageFor } from '../auth/messages.ts'
import { useConnections, vendorPath } from '../connections/ConnectionsPage.tsx'
import { resourceWords, Sentence } from '../connections/words.tsx'
import { useRuleList } from '../rules/shared.tsx'
import { ruleName } from '../rules/words.ts'

/**
 * The overview: the account's mailboxes, its rules and the connections that read its mail, each as the server answers
 * it and worded as its own page words it, each opening that page. Nothing here is counted or made up: what the server
 * does not answer is not shown. Rules come before connections, as they run first.
 */
export function OverviewPage() {
  return (
    <div className={pagePadding}>
      <Headline>Overview</Headline>
      <div className="mt-6 flex max-w-[880px] flex-col gap-9">
        <Mailboxes />
        <Rules />
        <Connections />
      </div>
    </div>
  )
}

function Section({
  id,
  title,
  action,
  children,
}: {
  id: string
  title: string
  action: ReactNode
  children: ReactNode
}) {
  return (
    <section aria-labelledby={id}>
      <SectionHeading id={id} title={title} action={action} />
      {children}
    </section>
  )
}

/** A line under a section's heading that says there is nothing yet, and where to start. */
function Empty({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="border-b border-rule py-3">
      <p className="text-small text-ink-2">{children}</p>
      {action && <div className="mt-3">{action}</div>}
    </div>
  )
}

/** One row of a list that opens its own page, or that shows only its words when it has none to open. */
function Row({ to, children }: { to?: string; children: ReactNode }) {
  if (!to) {
    return (
      <li className="flex flex-col gap-0.5 border-b border-rule py-[11px]">
        <span className="flex min-w-0 flex-col gap-0.5">{children}</span>
      </li>
    )
  }
  return (
    <li className="border-b border-rule">
      <Link
        to={to}
        className={cx('flex items-start gap-2 rounded-sm py-[11px] hover:bg-sunken', focusRing)}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">{children}</span>
        <ChevronRight aria-hidden size={16} className="mt-1 shrink-0 text-ink-2" />
      </Link>
    </li>
  )
}

function Mailboxes() {
  const { failed } = useSession()
  const standing = useStanding()
  const [list, setList] = useState<api.Mailbox[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList((await api.mailboxes()).mailboxes)
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(messageFor(code))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <Section
      id="overview-mailboxes"
      title="Mailboxes"
      action={<TextLink to="/accounts">See accounts</TextLink>}
    >
      {list === null ? (
        <LoadState failure={failure} what="Your mailboxes" onRetry={() => void load()} />
      ) : list.length === 0 ? (
        <Empty
          action={
            canChange(standing) && (
              <ButtonLink to="/accounts/new" size="sm">
                Connect a mailbox
              </ButtonLink>
            )
          }
        >
          No mailbox yet. Mail you forward from Gmail to a mailbox of your own shows here.
        </Empty>
      ) : (
        <ul aria-label="Your mailboxes">
          {list.map((mailbox) => (
            <Row
              key={mailbox.id}
              // A mailbox that waits for Gmail or can't be read has no inbox to open, as in Accounts.
              to={
                mailbox.state === 'waiting' || mailbox.state === 'unreadable'
                  ? undefined
                  : `/inbox/${encodeURIComponent(mailbox.id)}`
              }
            >
              <bdi className="font-medium break-words">{mailboxName(mailbox)}</bdi>
              <span className="text-small text-ink-2">{stateWords(mailbox)}</span>
            </Row>
          ))}
        </ul>
      )}
    </Section>
  )
}

/** On or off, said only when a rule is off: a rule is on unless the person turned it off. */
function Off({ on }: { on: boolean }) {
  return on ? null : <span className="font-mono text-mono text-ink-2">Off</span>
}

function Rules() {
  const { list, failure, load } = useRuleList()
  return (
    <Section
      id="overview-rules"
      title="Your rules"
      action={<TextLink to="/rules">See rules</TextLink>}
    >
      {list === null ? (
        <LoadState failure={failure} what="Your rules" onRetry={() => void load()} />
      ) : (
        <>
          <ul aria-label="Your rules">
            {/* Every account's rule, worded as the Rules page words it. */}
            <Row to="/rules">
              <span className="font-medium">Sign-in and security</span>
              <span className="text-small text-ink-2">Holds the email back. Every connection.</span>
              <Off on={list.default.on} />
            </Row>
            {list.rules.map((rule) => (
              <Row key={rule.id} to={`/rules/${encodeURIComponent(rule.id)}`}>
                <bdi className="font-medium break-words">{ruleName(rule.check)}</bdi>
                <bdi className="text-small text-ink-2">{rule.check.does}</bdi>
                <bdi className="text-small text-ink-2">{rule.check.for}</bdi>
                <Off on={rule.check.draft.on} />
              </Row>
            ))}
          </ul>
          {list.rules.length === 0 && (
            <Empty
              action={
                <ButtonLink to="/rules/new" size="sm">
                  New rule
                </ButtonLink>
              }
            >
              No rules of your own yet. A rule of your own holds mail back from your connections, or
              takes details out of it, before any app reads it.
            </Empty>
          )}
        </>
      )}
    </Section>
  )
}

function Connections() {
  const { list, failure, load } = useConnections()
  // The overview lists what reads mail now; the ones that ended are on the Connections page.
  const active = list?.filter((c) => c.state === 'active') ?? []
  return (
    <Section
      id="overview-connections"
      title="Connections"
      action={<TextLink to="/connections">See connections</TextLink>}
    >
      {list === null ? (
        <LoadState failure={failure} what="Your connections" onRetry={() => void load()} />
      ) : active.length === 0 ? (
        <Empty>
          No app or agent reads your mail. An app or an agent asks to connect from its own settings.
          You decide here.
        </Empty>
      ) : (
        <ul aria-label="Your connections">
          {active.map((c) => (
            <Row key={c.id} to={vendorPath(c.vendor.domain, c.id)}>
              <bdi className="font-medium break-words">{c.client_name || c.vendor.domain}</bdi>
              <span className="flex flex-wrap gap-x-3 text-small text-ink-2">
                <bdi>{c.vendor.domain}</bdi>
                <span>{resourceWords(c.resource)}</span>
              </span>
              <span className="text-small text-ink-2">
                Reads: {c.query ? <Sentence sentence={c.query.sentence} /> : 'All mail'}
              </span>
              {c.delivery !== 'delivering' && (
                <span className="text-small text-attention">Gets no mail now</span>
              )}
            </Row>
          ))}
        </ul>
      )}
    </Section>
  )
}
