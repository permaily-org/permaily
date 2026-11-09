import { type ReactNode, useState } from 'react'
import {
  Button,
  Count,
  Headline,
  StepMarker,
  type StepState,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import { cx } from '../../lib/styles.ts'
import { suggestedRulePath } from '../rules/data.ts'
import { expectations, filterSearch, forwardingAddress, forwardingMailbox } from './data.ts'
import { FlowHeader } from './FlowHeader.tsx'

export function ForwardingSetupPage() {
  const [verified, setVerified] = useState(false)
  const [showAddress, setShowAddress] = useState(false)

  return (
    <div className="flex min-h-svh flex-col xl:flex-row">
      <div className="flex min-w-0 flex-1 flex-col px-4 pt-6 pb-8 sm:px-8 sm:pt-8 lg:px-14">
        <FlowHeader />

        <main className="flex-1">
          <Headline className="mt-7">Forward only what your connections ask for.</Headline>
          <p className="mt-2 flex gap-3 font-mono text-mono">
            <span>Step {verified ? 3 : 2} of 3</span>
            <span className="text-ink-2">{forwardingMailbox.address}</span>
          </p>

          <ol className="mt-7 border-t border-rule">
            <FoldedStep
              number={1}
              title="Forwarding address added in Gmail"
              aside={
                <>
                  <span className="font-mono text-mono text-ink-2">{forwardingAddress}</span>
                  <TextAction aria-expanded={showAddress} onClick={() => setShowAddress((v) => !v)}>
                    {showAddress ? 'Hide' : 'Show'}
                  </TextAction>
                </>
              }
            >
              {showAddress && (
                <p className="pb-4 pl-12 text-body text-ink-2">
                  In Gmail, open Settings, then Forwarding and POP/IMAP, and add{' '}
                  <span className="font-mono text-small text-ink">{forwardingAddress}</span> as a
                  forwarding address.
                </p>
              )}
            </FoldedStep>

            {verified ? (
              <FoldedStep
                number={2}
                title="Gmail's verification message approved"
                aside={<span className="font-mono text-mono text-ink-2">approved just now</span>}
              />
            ) : (
              <OpenStep
                number={2}
                state="current"
                title="Confirm Gmail's verification message"
                aside={<span className="font-mono text-mono">we received it 12 seconds ago</span>}
              >
                <p className="text-body text-ink-2">
                  Gmail sent the confirmation to Permaily. Open it and approve it. Only you can do
                  this step.
                </p>
                <Button className="mt-3.5" onClick={() => setVerified(true)}>
                  Open verification link
                </Button>
              </OpenStep>
            )}

            <OpenStep
              number={3}
              state={verified ? 'current' : 'next'}
              title="Create the filter"
              aside={
                verified ? null : (
                  <span className="font-mono text-mono text-ink-2">unlocks after step 2</span>
                )
              }
            >
              {verified && <FilterInstructions />}
            </OpenStep>
          </ol>
        </main>

        <footer className="mt-12">
          <TextLink to="/accounts">Finish later</TextLink>
        </footer>
      </div>

      <WhatToExpect />
    </div>
  )
}

function FoldedStep({
  number,
  title,
  aside,
  children,
}: {
  number: number
  title: string
  aside: ReactNode
  children?: ReactNode
}) {
  return (
    <li className="border-b border-rule">
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 py-[13px]">
        <StepMarker state="done" number={number} />
        <span className="min-w-0 flex-1 basis-40 text-lead text-ink-2">{title}</span>
        <span className="flex min-w-0 flex-wrap items-center gap-x-5 gap-y-1 break-all max-sm:w-full max-sm:pl-12">
          {aside}
        </span>
      </div>
      {children}
    </li>
  )
}

function OpenStep({
  number,
  state,
  title,
  aside,
  children,
}: {
  number: number
  state: StepState
  title: string
  aside: ReactNode
  children: ReactNode
}) {
  return (
    <li
      className="flex gap-5 border-b border-rule pt-[18px] pb-5"
      aria-current={state === 'current' ? 'step' : undefined}
    >
      <StepMarker state={state} number={number} />
      <div className="min-w-0 flex-1">
        <div className="flex min-h-7 flex-wrap items-center justify-between gap-x-4 gap-y-1">
          <h2 className={cx('text-title font-medium', state === 'next' && 'text-ink-2')}>
            {title}
          </h2>
          {aside}
        </div>
        <div className="mt-2">{children}</div>
      </div>
    </li>
  )
}

function FilterInstructions() {
  const [copy, setCopy] = useState<'idle' | 'copied' | 'failed'>('idle')

  async function copySearch() {
    try {
      await navigator.clipboard.writeText(filterSearch)
      setCopy('copied')
    } catch {
      setCopy('failed')
    }
  }

  return (
    <>
      <p className="text-body text-ink-2">
        In Gmail, open Settings, then Filters and blocked addresses, and create a new filter. Paste
        this search into Has the words, then choose Forward it to{' '}
        <span className="font-mono text-small text-ink">{forwardingAddress}</span>.
      </p>
      <div className="mt-3.5 flex items-start gap-4 bg-sunken px-3 py-2.5">
        <code className="min-w-0 flex-1 font-mono text-mono break-words select-all">
          {filterSearch}
        </code>
        <TextAction size="sm" onClick={copySearch}>
          {copy === 'copied' ? 'Copied' : 'Copy'}
        </TextAction>
      </div>
      <p aria-live="polite" className="mt-1.5 text-small text-ink-2">
        {copy === 'failed' &&
          "Couldn't reach the clipboard. Select the search and copy it yourself."}
      </p>
      <div className="mt-3 border-t border-rule pt-3">
        <p className="font-medium">When the first email arrives</p>
        <p className="mt-0.5 text-small text-ink-2">
          We confirm it here, then suggest the Sign-in and security rule.
        </p>
        <TextLink to={suggestedRulePath} size="sm" className="mt-2.5">
          Review the suggested rule
        </TextLink>
      </div>
    </>
  )
}

function WhatToExpect() {
  return (
    <aside
      aria-label="What to expect"
      className="w-full shrink-0 border-t border-rule bg-raised px-4 pt-8 pb-10 sm:px-8 xl:w-[460px] xl:border-t-0 xl:border-l xl:px-9"
    >
      <p className="font-mono text-mono text-ink-2">What to expect</p>
      <Headline as="h2" className="mt-4">
        About <Count>40</Count> a month.
      </Headline>
      <p className="mt-4.5 text-body text-ink-2">
        Roughly how much mail this filter sends Permaily. Everything else never leaves Gmail.
      </p>
      <ul className="mt-3 border-t border-rule-strong">
        {expectations.map((item) => (
          <li key={item.title} className="border-b border-rule py-2.5">
            <p className="text-body font-medium">{item.title}</p>
            <p className="text-small text-ink-2">{item.detail}</p>
          </li>
        ))}
      </ul>
    </aside>
  )
}
