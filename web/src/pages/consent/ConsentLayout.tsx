import type { ReactNode } from 'react'
import { Button, Tile } from '../../components/ui/index.ts'

/** Consent page: the request and decision on the left, the preview raised on the right. */
export function ConsentLayout({ mailbox, children, preview }: { mailbox: ReactNode; children: ReactNode; preview: ReactNode }) {
  return (
    <main className="flex min-h-svh flex-col lg:flex-row">
      <div className="px-4 pt-7 pb-14 sm:px-14 lg:sticky lg:top-0 lg:max-h-svh lg:w-[600px] lg:shrink-0 lg:self-start lg:overflow-y-auto">
        <header className="flex items-center justify-between gap-4">
          <span className="text-wordmark font-semibold">Permaily</span>
          {mailbox}
        </header>
        {children}
      </div>
      <section aria-label="Preview" className="min-w-0 flex-1 border-t border-rule bg-raised px-4 pt-7 pb-14 sm:px-12 lg:border-t-0 lg:border-l">
        {preview}
      </section>
    </main>
  )
}

/** Who is asking, with the facts that shape how to read the request. */
export function Requester({ initials, line, facts }: { initials: string; line: string; facts: string[] }) {
  return (
    <div className="mt-11 flex items-center gap-2.5">
      <Tile initials={initials} />
      <div className="flex min-w-0 flex-col gap-1">
        <p className="font-mono text-mono">{line}</p>
        <p className="flex flex-wrap gap-x-3.5 font-mono text-micro text-ink-2">
          {facts.map((fact) => (
            <span key={fact}>{fact}</span>
          ))}
        </p>
      </div>
    </div>
  )
}

export function Terms({ children, strong = true }: { children: ReactNode; strong?: boolean }) {
  return <div className={strong ? 'mt-5 border-t border-rule-strong' : 'mt-5'}>{children}</div>
}

/** The decision, right after the terms, with a line that says what follows or why Allow is off. */
export function Decision({
  allowLabel,
  onAllow,
  allowDisabled = false,
  declineLabel,
  onDecline,
  note,
}: {
  allowLabel: string
  onAllow: () => void
  allowDisabled?: boolean
  declineLabel: string
  onDecline: () => void
  note: ReactNode
}) {
  return (
    <>
      <div className="mt-7 flex flex-wrap gap-2.5">
        <Button onClick={onAllow} disabled={allowDisabled} aria-describedby="decision-note">
          {allowLabel}
        </Button>
        <Button variant="secondary" onClick={onDecline}>
          {declineLabel}
        </Button>
      </div>
      <p id="decision-note" aria-live="polite" className="mt-3.5 max-w-[488px] text-small text-ink-2">
        {note}
      </p>
    </>
  )
}
