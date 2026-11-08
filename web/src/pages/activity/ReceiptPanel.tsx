import { ChevronRight } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { Bar, ButtonLink, Mark, SectionHeading, Tag, TextLink } from '../../components/ui/index.ts'
import { connectionById, formatCount, type Receipt } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { domainOf, fullTime, previousReceipt, shortHash } from './data.ts'

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-h-[34px] items-center gap-4 border-b border-rule py-[5px]">
      <dt className="w-[80px] shrink-0 text-small text-ink-2">{label}</dt>
      <dd className="flex min-w-0 flex-1 items-center gap-3">{children}</dd>
    </div>
  )
}

export function ReceiptPanel({ receipt }: { receipt: Receipt }) {
  const connection = connectionById(receipt.connectionId)
  const name = connection?.name ?? receipt.connectionId
  const domain = domainOf(receipt.from)

  return (
    <div>
      <p className="font-mono text-mono text-ink-2">receipt {receipt.id}</p>
      <h2 className="mt-2 text-heading font-medium">Read by {name}</h2>
      <p className="mt-1 font-mono text-mono">{fullTime(receipt)}</p>

      <section aria-labelledby="what-left" className="mt-5">
        <SectionHeading id="what-left" title="What left" action={<TextLink to={`/connections/${receipt.connectionId}`}>See exactly what left</TextLink>} />
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
        </dl>
      </section>

      <section aria-labelledby="why-allowed" className="mt-5">
        <SectionHeading id="why-allowed" title="Why it was allowed" />
        <dl>
          <Fact label="Rule">
            <Tag>{receipt.ruleTag}</Tag>
          </Fact>
          <Fact label="Access">
            <span className="text-body">{receipt.approved}</span>
          </Fact>
          <Fact label="Request">
            <span className="font-mono text-mono">{receipt.request}</span>
          </Fact>
        </dl>
      </section>

      <section aria-labelledby="not-wanted" className="mt-5">
        <SectionHeading id="not-wanted" title="Not what you wanted?" />
        <div className="mt-2.5 flex flex-wrap gap-2">
          <ButtonLink to="/never-share" variant="secondary" size="sm">
            Never share {domain}
          </ButtonLink>
          <ButtonLink to={`/rules/${receipt.ruleId}`} variant="secondary" size="sm">
            Change this rule
          </ButtonLink>
        </div>
        <TextLink to={`/connections/${receipt.connectionId}`} tone="danger" className="mt-2.5">
          Revoke {name}'s access
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
              <Fact label="Previous">
                <span className="font-mono text-mono">
                  {previous.id} <span className="text-ink-2">{shortHash(previous.id)}</span>
                </span>
              </Fact>
            )}
          </dl>
          <p className="mt-2.5 text-small text-ink-2">
            Each receipt carries the hash of the one before it, so a changed or missing receipt breaks the chain.
          </p>
        </div>
      )}
    </div>
  )
}
