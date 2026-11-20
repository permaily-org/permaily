import { ChevronRight } from 'lucide-react'
import { type ReactNode, useId, useState } from 'react'
import { InfoTip, SectionHeading, TextAction, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing } from '../../lib/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { vendorPath } from '../connections/ConnectionsPage.tsx'
import { endWords, partsWords, resourceWords, Sentence, sinceWords } from '../connections/words.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import type { Row } from './record.ts'
import {
  conditionWords,
  consentTip,
  givenWords,
  heldWhyWords,
  linkKindTips,
  linkVerdictWords,
  momentWords,
  positionWords,
  probabilityWords,
  refusalCountWords,
  refusedWords,
  sendersWords,
} from './words.ts'

/** What the panel knows besides the receipt: the connections and mailboxes listed now, and whether it may link. */
export type Known = {
  connections: ReadonlyMap<string, api.Connection>
  /** Null until the mailboxes are read, or when they could not be. */
  mailboxes: ReadonlyMap<string, api.Mailbox> | null
  /** Whether the connections' pages can be opened from here (not while the account is closing). */
  linkConnections: boolean
}

function Fact({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-h-[34px] items-start gap-4 border-b border-rule py-[7px]">
      <dt className="flex w-[96px] shrink-0 items-center gap-1.5 text-small text-ink-2">{label}</dt>
      <dd className="min-w-0 flex-1 break-words text-body">{children}</dd>
    </div>
  )
}

function Mono({ children }: { children: ReactNode }) {
  return <span className="font-mono text-mono break-all">{children}</span>
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="mt-5">
      <SectionHeading id={id} title={title} />
      {children}
    </section>
  )
}

/** The receipt of a row, or what was found in its place. */
export function ReceiptPanel({ row, title, known }: { row: Row; title: string; known: Known }) {
  const position =
    row.type === 'link' || row.type === 'break'
      ? `link ${row.link.n}`
      : `receipt ${positionWords(row.link.n, row.entry.index)}`
  return (
    <div>
      <p className="font-mono text-mono text-ink-2">{position}</p>
      <h2 className="mt-2 flex items-center gap-1.5 text-heading font-medium">
        <span>{title}</span>
        {row.type === 'receipt' &&
          row.receipt.kind === 'consent' &&
          row.receipt.consent?.allowed && (
            <InfoTip label="About this approval" align="start">
              {consentTip}
            </InfoTip>
          )}
      </h2>
      {row.type === 'receipt' && (
        <p className="mt-1 font-mono text-mono">{momentWords(row.receipt.at)}</p>
      )}
      {row.type === 'receipt' && <Receipt receipt={row.receipt} known={known} />}
      {row.type === 'unknown' && (
        <p className="mt-3 text-body text-ink-2">
          It may be of a later version of Permaily. It is kept in your record and in its download
          for checking.
        </p>
      )}
      <TechnicalRecord key={row.key} row={row} />
    </div>
  )
}

function Receipt({ receipt: r, known }: { receipt: api.Receipt; known: Known }) {
  const models = [
    ...r.delivered.flatMap((d) => d.model),
    ...r.delivered.flatMap((d) => d.cleared.flatMap((c) => c.model)),
    ...r.held.flatMap((h) => h.model),
    ...(r.refused?.held?.model ?? []),
  ]
  return (
    <>
      {r.delivered.length > 0 && (
        <Section title="What left">
          {r.delivered.map((d) => (
            <dl key={`${d.mailbox}/${d.message}`} className="mb-3">
              <Fact label="Mailbox">{mailboxName(d.mailbox, known)}</Fact>
              <Fact label="Received">
                <span className="font-mono text-mono">{momentWords(d.received)}</span>
              </Fact>
              {d.parts.includes('sender') && d.from.length > 0 && (
                <Fact label="From">
                  <bdi>{sendersWords(d.from)}</bdi>
                </Fact>
              )}
              {d.parts.includes('subject') && d.subject !== undefined && (
                <Fact label="Subject">
                  <bdi>{d.subject}</bdi>
                </Fact>
              )}
              <Fact label="Parts given">{givenWords(d.parts, d.attachment)}</Fact>
              {d.cleared.map((c) => (
                <Fact key={`${c.rule.id}/${c.rule.version}`} label="Let through by">
                  <Mono>
                    {c.rule.id} v{c.rule.version}
                  </Mono>
                </Fact>
              ))}
            </dl>
          ))}
        </Section>
      )}
      {r.held.length > 0 && (
        <Section title="What a rule held">
          {r.held.map((h) => (
            <Held key={`${h.mailbox}/${h.message}`} held={h} known={known} />
          ))}
        </Section>
      )}
      {r.refused && (
        <Section title="Why it was refused">
          <dl>
            <Fact label="Reason">
              {refusedWords(r.refused.reason) ?? <Mono>{r.refused.reason}</Mono>}
            </Fact>
            {r.refused.mailbox && (
              <Fact label="Mailbox">{mailboxName(r.refused.mailbox, known)}</Fact>
            )}
          </dl>
          {r.refused.held && <Held held={r.refused.held} known={known} />}
        </Section>
      )}
      {r.consent && <Consent consent={r.consent} known={known} />}
      {r.ended && (
        <Section title="Why it ended">
          <p className="mt-2.5 text-body">{endWords(r.ended.reason)}.</p>
        </Section>
      )}
      {r.refusals && (
        <Section title="What was refused">
          <dl>
            <Fact label="From">
              <span className="font-mono text-mono">{momentWords(r.refusals.from)}</span>
            </Fact>
            <Fact label="Until">
              <span className="font-mono text-mono">{momentWords(r.refusals.until)}</span>
            </Fact>
            <Fact label="Recorded">{r.refusals.recorded} each in a receipt of its own</Fact>
            {r.refusals.counts.map((c) => (
              <Fact key={`${c.operation}/${c.reason}`} label={`${c.count}`}>
                {refusalCountWords(c.operation, c.reason)}
              </Fact>
            ))}
          </dl>
        </Section>
      )}
      {models.length > 0 && (
        <Section title="A model decided">
          <ul>
            {models.map((m, i) => (
              // A receipt's answers have no identity but their place.
              // biome-ignore lint/suspicious/noArrayIndexKey: the answers of a receipt never move
              <ModelAnswer key={i} model={m} />
            ))}
          </ul>
        </Section>
      )}
      <Allowed receipt={r} known={known} />
    </>
  )
}

function mailboxName(id: string, known: Known): ReactNode {
  if (!known.mailboxes) return 'A mailbox'
  const mailbox = known.mailboxes.get(id)
  if (!mailbox) return 'A mailbox that was disconnected'
  return <bdi>{mailbox.label || mailbox.address || id}</bdi>
}

function Held({ held: h, known }: { held: api.ReceiptHeld; known: Known }) {
  return (
    <dl className="mb-3">
      <Fact label="Mailbox">{mailboxName(h.mailbox, known)}</Fact>
      <Fact label="Rule">
        <Mono>
          {h.rule.id} v{h.rule.version}
        </Mono>
      </Fact>
      <Fact label="How">{heldWhyWords(h.why)}</Fact>
    </dl>
  )
}

function ModelAnswer({ model: m }: { model: api.ReceiptModel }) {
  const { lead, topic } = conditionWords(m)
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-0.5 border-b border-rule py-[7px] text-body">
      <span>
        {lead} <bdi>“{topic}”</bdi>
      </span>
      <span className="font-mono text-mono">{m.refused ? 'refused' : probabilityWords(m.p)}</span>
      {(m.model || m.prompt) && (
        <span className="font-mono text-mono text-ink-2">
          {[m.model, m.prompt].filter(Boolean).join(', ')}
        </span>
      )}
      {m.canary_checked && (
        <InfoTip label="About the model's check" align="end">
          Its answers were checked on {m.canary_checked}
          {m.canary_change ? `; they last changed on ${m.canary_change}` : ''}.
        </InfoTip>
      )}
    </li>
  )
}

function Consent({ consent: c, known }: { consent: api.ReceiptConsent; known: Known }) {
  return (
    <Section title="What you were shown">
      <dl>
        {c.sentences.length > 0 && (
          <Fact label="Reads">
            <ul className="flex flex-col gap-0.5">
              {c.sentences.map((s) => (
                <li key={s}>
                  <bdi>{s}</bdi>
                </li>
              ))}
            </ul>
          </Fact>
        )}
        {c.mailboxes.length > 0 && (
          <Fact label="Mailboxes">
            <ul className="flex flex-col gap-0.5">
              {c.mailboxes.map((m) => (
                <li key={m}>{mailboxName(m, known)}</li>
              ))}
            </ul>
          </Fact>
        )}
        <Fact label="Parts">{partsWords(c.parts)}</Fact>
        <Fact label="Mail since">{sinceWords(c.since)}</Fact>
        {c.ends_at && <Fact label="Ends">{formatDay(c.ends_at)}</Fact>}
        <Fact label="Model's notice">
          <span className="font-mono text-mono">{c.model_notice}</span>
        </Fact>
      </dl>
    </Section>
  )
}

/** Why it was allowed: the connection, the vendor, the resource, the device, and the search in words on a press. */
function Allowed({ receipt: r, known }: { receipt: api.Receipt; known: Known }) {
  const listed = r.connection ? known.connections.get(r.connection) : undefined
  const search = r.formulas.find((f) => f.role === 'search' && f.formula)?.formula
  return (
    <Section title="Why it was allowed">
      <dl>
        {r.connection && (
          <Fact label="Connection">
            {listed && known.linkConnections ? (
              <TextLink to={vendorPath(listed.vendor.domain, listed.id)} size="sm">
                <bdi>{listed.client_name || listed.vendor.domain}</bdi>
              </TextLink>
            ) : listed ? (
              <bdi>{listed.client_name || listed.vendor.domain}</bdi>
            ) : (
              <span className="text-ink-2">A connection no longer listed</span>
            )}
          </Fact>
        )}
        <Fact label="App">
          <bdi className="font-mono text-mono">{r.vendor}</bdi>
          {listed?.vendor.verified && (
            <span className="ml-2 text-small text-ink-2">Verified: {listed.vendor.verified}</span>
          )}
        </Fact>
        <Fact label="Through">{resourceWords(r.resource)}</Fact>
        {r.device && (
          <Fact label="Device">
            <Mono>{r.device}</Mono>
          </Fact>
        )}
      </dl>
      {search && <SearchWords formula={search} />}
    </Section>
  )
}

/** The vendor's search in words, asked of the check route only when the person presses. */
function SearchWords({ formula }: { formula: api.QueryFormula }) {
  const { failed } = useSession()
  const [shown, setShown] = useState<
    | { state: 'idle' | 'asking' }
    | { state: 'sentence'; sentence: api.QuerySentence }
    | { state: 'refused' }
    | { state: 'failed'; words: string }
  >({ state: 'idle' })

  async function ask() {
    if (shown.state === 'asking') return
    setShown({ state: 'asking' })
    try {
      setShown({ state: 'sentence', sentence: (await api.checkQuery(formula)).sentence })
    } catch (problem) {
      const code = failed(problem)
      if (code === 'invalid_formula') setShown({ state: 'refused' })
      else if (code) setShown({ state: 'failed', words: messageFor(code) })
      else setShown({ state: 'idle' })
    }
  }

  if (shown.state === 'sentence')
    return (
      <div className="mt-3 text-body">
        <p className="text-small text-ink-2">The search</p>
        <Sentence sentence={shown.sentence} />
      </div>
    )
  if (shown.state === 'refused')
    return (
      <div className="mt-3">
        <p className="text-small text-ink-2">
          This version of Permaily can't put this search in words. As it was asked:
        </p>
        {/* biome-ignore lint/a11y/noNoninteractiveTabindex: a region that scrolls is reached by keyboard (docs/accessibility.md) */}
        <pre tabIndex={0} className="mt-1 overflow-x-auto font-mono text-mono">
          {JSON.stringify(formula)}
        </pre>
      </div>
    )
  return (
    <div className="mt-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <TextAction size="sm" aria-disabled={shown.state === 'asking'} onClick={ask}>
        Show the search in words
      </TextAction>
      {shown.state === 'failed' && (
        <p role="alert" className="text-small text-danger">
          {shown.words}
        </p>
      )}
    </div>
  )
}

/** The receipt's place on the chain, folded: its identifier, position, digest, link's kind and the link before. */
function TechnicalRecord({ row }: { row: Row }) {
  const [open, setOpen] = useState(false)
  const id = useId()
  const link = row.link
  const entry = row.type === 'link' || row.type === 'break' ? null : row.entry
  return (
    <div className="mt-5 border-t border-rule">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((v) => !v)}
        className={cx(
          'flex w-full items-center justify-between py-3 text-small text-ink-2 hover:text-ink',
          focusRing,
        )}
      >
        Technical record
        <ChevronRight
          aria-hidden
          size={16}
          className={cx('transition-transform', open && 'rotate-90')}
        />
      </button>
      {open && (
        <div id={id} className="pb-2">
          <dl>
            {entry && (
              <>
                <Fact label="Receipt">
                  <Mono>{entry.id}</Mono>
                </Fact>
                <Fact label="Position">
                  <Mono>
                    {link.n}.{entry.index}
                  </Mono>
                </Fact>
                <Fact label="Digest">
                  <Mono>sha256 {entry.sha256}</Mono>
                </Fact>
              </>
            )}
            <Fact
              label={
                <>
                  Link
                  {link.verdict === 'expired' && (
                    <InfoTip label="About this link" align="start">
                      {linkKindTips.expired}
                    </InfoTip>
                  )}
                </>
              }
            >
              <Mono>{link.n}</Mono>{' '}
              <span className="text-small">{linkVerdictWords(link.verdict, link.n)}</span>
            </Fact>
            {link.kind && (
              <Fact
                label={
                  <>
                    Linked
                    {(link.kind === 'recovered' || link.kind === 'adopted') && (
                      <InfoTip label="About how it was linked" align="start">
                        {linkKindTips[link.kind]}
                      </InfoTip>
                    )}
                  </>
                }
              >
                {link.kind === 'recovered'
                  ? 'Late'
                  : link.kind === 'adopted'
                    ? 'From before'
                    : 'When it was written'}
              </Fact>
            )}
            {link.prev && (
              <Fact label="Link before">
                <Mono>sha256 {link.prev}</Mono>
              </Fact>
            )}
          </dl>
        </div>
      )}
    </div>
  )
}
