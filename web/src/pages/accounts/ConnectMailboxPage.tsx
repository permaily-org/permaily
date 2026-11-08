import { ButtonLink, Headline } from '../../components/ui/index.ts'
import { cx } from '../../lib/styles.ts'
import { methods } from './data.ts'
import { FlowHeader } from './FlowHeader.tsx'

export function ConnectMailboxPage() {
  return (
    <div className="mx-auto w-full max-w-[1296px] px-4 pt-6 pb-10 sm:px-8 sm:pt-8 sm:pb-16 lg:px-12">
      <FlowHeader />

      <main>
        <Headline className="mt-10">How should Permaily reach your mail?</Headline>
        <p className="mt-2 text-lead text-ink-2">
          Not sure? Forwarding gives Permaily the least.
        </p>

        <div className="mt-9 grid grid-cols-1 border-t border-rule-strong">
          {methods.map((method, i) => (
            <section
              key={method.id}
              aria-labelledby={`method-${method.id}`}
              className={cx('pt-6.5 pb-6', i === 0 ? 'lg:pr-10' : 'border-t border-rule lg:border-t-0 lg:border-l lg:pl-10')}
            >
              <h2 id={`method-${method.id}`} className="text-heading font-medium">
                {method.title}
              </h2>
              <p className="mt-1 text-body text-ink-2">{method.summary}</p>

              <dl className="mt-4">
                {method.facts.map(([label, value]) => (
                  <div key={label} className="flex min-h-9 items-center gap-4 border-b border-rule py-2">
                    <dt className="w-[104px] shrink-0 sm:w-[146px] text-small text-ink-2">{label}</dt>
                    <dd className="min-w-0 text-small">{value}</dd>
                  </div>
                ))}
              </dl>

              {method.id === 'forwarding' ? (
                <ButtonLink to="/accounts/new/forwarding" className="mt-4.5">
                  Set up forwarding
                </ButtonLink>
              ) : (
                <div className="mt-4.5 flex flex-wrap items-center gap-3.5">
                  <ButtonLink to="/accounts" variant="secondary">
                    Continue with Google
                  </ButtonLink>
                  <span className="font-mono text-mono text-ink-2">Limited while Google reviews Permaily</span>
                </div>
              )}
            </section>
          ))}
        </div>
      </main>
    </div>
  )
}
