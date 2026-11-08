import { ChevronRight } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Bar, ButtonLink, InfoTip, Mark, SectionHeading, TextLink } from '../../components/ui/index.ts'
import { connectionById, formatCount, rules, vendorOf, type Receipt } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { domainOf, fullTime, previousReceipt, shortHash } from './data.ts'

function Fact({ label, children }: { label: ReactNode; children: ReactNode }) {
  return (
    <div className="flex min-h-[34px] items-center gap-4 border-b border-rule py-[5px]">
      <dt className="flex w-[80px] shrink-0 items-center gap-1.5 text-small text-ink-2">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center gap-3">{children}</dd>
    </div>
  )
}

export function ReceiptPanel({ receipt }: { receipt: Receipt }) {
  const connection = connectionById(receipt.connectionId)
  const vendor = connection && vendorOf(connection)
  const name = vendor?.name ?? receipt.connectionId
  const page = `/connections/${receipt.connectionId}`
  const domain = domainOf(receipt.from)
  const removedBy = receipt.removed && rules.find((rule) => rule.id === receipt.removed?.ruleId)

  return (
    <div>
      <p className="font-mono text-mono text-ink-2">receipt {receipt.id}</p>
      <h2 className="mt-2 text-heading font-medium">Read by {name}</h2>
      <p className="mt-1 font-mono text-mono">{fullTime(receipt)}</p>

      <section aria-labelledby="what-left" className="mt-5">
        <SectionHeading id="what-left" title="What left" />
        <dl>
          <Fact label="From">
            <span className="truncate font-mono text-small">{receipt.from}</span>
          </Fact>
          <Fact label="Subject">
            <Mark>{receipt.subject}</Mark>
          </Fact>
          <Fact label="Body text">
            <Mark>{formatCount(receipt.bodyChars)} characters</Mark>
          </Fact>
          <Fact label="Attachment">
            {receipt.attachment === 'withheld' ? (
              <>
                <Bar width={96} />
                <span className="font-mono text-mono text-ink-2">withheld</span>
              </>
            ) : (
              <span className="text-body text-ink-2">None</span>
            )}
          </Fact>
          {receipt.removed && (
            <Fact label="Removed">
              <Bar width={64} />
              <span className="min-w-0 text-small">
                {receipt.removed.what}, by{' '}
                <TextLink to={`/rules/${receipt.removed.ruleId}`} size="sm">
                  {removedBy ? removedBy.name : 'your rule'}
                </TextLink>
              </span>
            </Fact>
          )}
        </dl>
      </section>

      <section aria-labelledby="why-allowed" className="mt-5">
        <SectionHeading id="why-allowed" title="Why it was allowed" />
        <dl>
          <Fact label="Connection">
            <TextLink to={page} size="sm">
              {connection?.name ?? receipt.connectionId}
            </TextLink>
          </Fact>
          <Fact label="Query">
            <span className={receipt.query.startsWith('"') ? 'font-mono text-mono' : 'text-body'}>{receipt.query}</span>
          </Fact>
          <Fact label="Access">
            <span className="text-body">{connection?.approved}</span>
          </Fact>
          <Fact label="Request">
            <span className="font-mono text-mono">{receipt.request}</span>
          </Fact>
        </dl>
      </section>

      <section aria-labelledby="not-wanted" className="mt-5">
        <SectionHeading id="not-wanted" title="Not what you wanted?" />
        <div className="mt-2.5 flex flex-wrap gap-2">
          <ButtonLink to={`/rules/private?add=${encodeURIComponent(domain)}`} variant="secondary" size="sm">
            Hold back {domain}
          </ButtonLink>
          <ButtonLink to={`/rules/new?add=${encodeURIComponent(domain)}`} variant="secondary" size="sm">
            New rule for this sender
          </ButtonLink>
        </div>
        <TextLink to={page} tone="danger" className="mt-2.5">
          Revoke {connection ? `${name} · ${connection.name}` : name}
        </TextLink>
      </section>

      <TechnicalRecord key={receipt.id} receipt={receipt} />
    </div>
  )
}

function TechnicalRecord({ receipt }: { receipt: Receipt }) {
  const [open, setOpen] = useState(false)
  const previous = previousReceipt(receipt)

  return (
    <div className="mt-5 border-t border-rule">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="technical-record"
        onClick={() => setOpen((v) => !v)}
        className={cx('flex w-full items-center justify-between py-3 text-small text-ink-2 hover:text-ink', focusRing)}
      >
        Technical record
        <ChevronRight aria-hidden size={16} className={cx('transition-transform', open && 'rotate-90')} />
      </button>
      {open && (
        <div id="technical-record" className="pb-2">
          <dl>
            <Fact label="Receipt">
              <span className="font-mono text-mono">{receipt.id}</span>
            </Fact>
            <Fact label="Hash">
              <span className="font-mono text-mono">{shortHash(receipt.id)}</span>
            </Fact>
            {previous && (
              <Fact
                label={
                  <>
                    Previous
                    <InfoTip label="About the hash chain" align="start">
                      Each receipt carries the hash of the one before it, so a changed or missing receipt breaks the chain.
                    </InfoTip>
                  </>
                }
              >
                <span className="font-mono text-mono">
                  {previous.id} <span className="text-ink-2">{shortHash(previous.id)}</span>
                </span>
              </Fact>
            )}
          </dl>
        </div>
      )}
    </div>
  )
}
