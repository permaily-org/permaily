import { useEffect, useRef, useState } from 'react'
import { Button, Checkbox } from '../../../components/ui/index.ts'

/**
 * Recovery codes, shown the one time the server gives them: it keeps only their hash. They are in this
 * component's memory while they are on screen and nowhere else: not in storage, the address or a log.
 * The person says they kept them before the screen moves on.
 */
export function RecoveryCodes({
  codes,
  headingId,
  replaced,
  onKept,
}: {
  codes: string[]
  headingId: string
  /** True when these took the place of earlier codes, which stopped working. */
  replaced: boolean
  onKept: () => void
}) {
  const [kept, setKept] = useState(false)
  const [copied, setCopied] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const keptRef = useRef<HTMLDivElement>(null)

  // Focus goes to the heading, not to an action: Enter must not send the codes away unread.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  // Closing or reloading the tab would lose the codes for good: the browser asks first.
  useEffect(() => {
    const ask = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener('beforeunload', ask)
    return () => window.removeEventListener('beforeunload', ask)
  }, [])

  async function copy() {
    try {
      await navigator.clipboard.writeText(codes.join('\n'))
      setCopied(`Copied ${codes.length} codes.`)
    } catch {
      setCopied("Couldn't copy. Select the codes and copy them by hand.")
    }
  }

  function done() {
    if (kept) {
      onKept()
      return
    }
    setError("Tick the box once you've kept the codes. They can't be shown again.")
    keptRef.current?.querySelector('input')?.focus()
  }

  return (
    <>
      <h2
        id={headingId}
        ref={headingRef}
        tabIndex={-1}
        data-dialog-focus
        className="text-heading font-medium outline-none"
      >
        Keep your recovery codes
      </h2>
      <p className="mt-3 text-body">
        <strong className="font-medium">These codes won't be shown again.</strong> Each one signs
        you in once, with your email address, when you can't use a passkey.
        {replaced && ' The codes you had before no longer work.'}
      </p>
      <ol className="mt-5 grid grid-cols-1 gap-x-8 gap-y-1 border-y border-rule py-3 font-mono text-body sm:grid-cols-2">
        {codes.map((code, index) => (
          <li key={code} className="flex gap-3">
            {/* The list numbers itself for a screen reader. The digits stay out of a selection. */}
            <span aria-hidden className="w-5 shrink-0 select-none text-right text-ink-2">
              {index + 1}
            </span>
            <span>{code}</span>
          </li>
        ))}
      </ol>
      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
        <Button variant="secondary" size="sm" onClick={copy}>
          Copy all
        </Button>
        <p role="status" className="text-small text-ink-2 empty:hidden">
          {copied}
        </p>
      </div>
      <p className="mt-5 text-body text-ink-2">
        Keep them somewhere other than this device, such as a password manager or a sheet of paper.
      </p>
      <div ref={keptRef} className="mt-4">
        <Checkbox
          label="I've kept these codes"
          checked={kept}
          aria-describedby={`${headingId}-error`}
          aria-invalid={error ? true : undefined}
          onChange={(event) => {
            setKept(event.target.checked)
            setError(null)
          }}
        />
      </div>
      <p
        id={`${headingId}-error`}
        role="alert"
        className="mt-2 text-small text-danger empty:hidden"
      >
        {error}
      </p>
      <div className="mt-6 flex flex-wrap gap-3">
        <Button onClick={done}>Done</Button>
      </div>
    </>
  )
}
