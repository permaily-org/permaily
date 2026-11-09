import type { ReactNode } from 'react'
import { Button, Tile } from '../../components/ui/index.ts'

/**
 * Consent page: the request and decision, then the preview raised beside them.
 * Side by side from xl, the first width where the preview's mail rows fit next to the request. Stacked below that.
 */
export function ConsentLayout({
  mailbox,
  children,
  preview,
}: {
  mailbox: ReactNode
  children: ReactNode
  preview: ReactNode
}) {
  return (
    <main className="flex min-h-svh flex-col xl:flex-row">
      <div className="w-full max-w-[600px] px-4 pt-6 pb-10 sm:px-8 lg:px-14 lg:pt-7 lg:pb-14 xl:sticky xl:top-0 xl:max-h-svh xl:shrink-0 xl:self-start xl:overflow-y-auto">
        <header className="flex items-center justify-between gap-4">
          <span className="text-wordmark font-semibold">Permaily</span>
          {mailbox}
        </header>
        {children}
      </div>
      <section
        aria-label="Preview"
        className="min-w-0 flex-1 border-t border-rule bg-raised px-4 pt-6 pb-10 sm:px-8 lg:px-14 lg:pt-7 lg:pb-14 xl:border-t-0 xl:border-l xl:px-12"
      >
        {preview}
      </section>
    </main>
  )
}

/** Who is asking, with the facts that shape how to read the request. */
export function Requester({
  initials,
  line,
  facts,
}: {
  initials: string
  line: string
  facts: string[]
}) {
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
      {/* Full width on phones, so both are easy to reach with a thumb. */}
      <div className="mt-7 flex flex-col gap-2.5 sm:flex-row sm:flex-wrap">
        <Button onClick={onAllow} disabled={allowDisabled} aria-describedby="decision-note">
          {allowLabel}
        </Button>
        <Button variant="secondary" onClick={onDecline}>
          {declineLabel}
        </Button>
      </div>
      <p
        id="decision-note"
        aria-live="polite"
        className="mt-3.5 max-w-[488px] text-small text-ink-2"
      >
        {note}
      </p>
    </>
  )
}
